"""多轮对话服务层（原 chat_ui.py 的逻辑部分，去掉 Gradio 依赖）。

提供 MCP 子进程管理、消息工具循环（SSE 事件流）、项目生命周期、
文件管理、工作区绑定与草稿存档，供 FastAPI (server.py) 调用。
chat_pipeline.py 通过 `import chat_service as _chat_ui` 复用本模块接口。
"""
from __future__ import annotations

import copy
import json
import logging
import os
import queue
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import zipfile
from pathlib import Path

import mido

import config
import get
import project_manager

logger = logging.getLogger("ai_midi")


# ==================== 常量 ====================

_NOTE_TABLE_INTRO = (
    '由于无法直接上传midi文件，我们会使用类似midi文件的"note_table"格式来记录音符信息，'
    '以下为"note_table"的格式介绍：\n'
    '[note: "<音符键名>", velocity: "<音符力度>", start: "<音符的开始时间（拍）>", '
    'end: "<音符的结束时间（拍）>" ]\n'
    '示例 ：\n'
    '[note: "C4", velocity: "80", start: "1", end: "2" ] \n'
    '表示音符对应的按键是C4，演奏力度80，时间是第一拍到第二拍。'
)

_LIBRARY_DIR = config.PROJECT_ROOT / "Library"

# MCP 子进程相关
_MCP_SCRIPT = config.PROJECT_ROOT / "mcp_server.py"
_mcp_process: subprocess.Popen | None = None
_mcp_lock = threading.Lock()
_mcp_request_id = 0
_mcp_initialized = False

# MCP 后台 reader 单例：进程级唯一读线程 + 队列，避免每次读响应都新建线程、
# 超时留下永久阻塞的孤儿线程
_mcp_reader_state: dict = {"proc": None, "thread": None, "queue": None}
_MCP_EOF = object()  # 队列哨兵：reader 已读到 EOF

# MCP 工具列表缓存（进程存活期间复用，进程重启/关闭时失效）
_mcp_tools_cache: list[dict] | None = None

# 当前打开的项目 ID（None 表示在项目浏览器）
_current_project_id: str | None = None

# 对话串行锁：chat_stream 整体持锁执行，避免两个并发对话互相覆盖
# 全局 _current_project_id / 会话状态 / MCP 响应队列（数据串号）。
# StreamingResponse 对同步生成器逐 next() 调度，可能落在不同线程；
# threading.Lock 无属主限制（非 RLock），跨线程 release 是安全的，
# GeneratorExit（客户端断开/主动停止）也会走 finally 释放。
_chat_lock = threading.Lock()


# ==================== 会话状态 ====================

# project_id -> {"full_history": [...], "midi_files": [...],
#                "undo_stack": [...], "chat_display": [...],
#                "pending_edit": {...} | None, "edit_history": [...]}
_sessions: dict[str, dict] = {}


def _get_session(project_id: str) -> dict:
    """获取（或从磁盘水合）项目会话状态。"""
    session = _sessions.get(project_id)
    if session is None:
        full_history, midi_files = project_manager.load_history(project_id)
        session = {
            "full_history": full_history,
            "midi_files": midi_files,
            "undo_stack": [],
            "chat_display": _rebuild_display_from_history(full_history),
            "pending_edit": None,
            "edit_history": project_manager.load_edit_history(project_id),
        }
        _sessions[project_id] = session
    return session


def _drop_session(project_id: str) -> None:
    """项目删除/复制后丢弃内存会话。"""
    _sessions.pop(project_id, None)


def _public_files(files: list[dict]) -> list[dict]:
    """向前端暴露的文件元数据（不含超大 note_table）。"""
    return [
        {
            "name": f.get("name", ""),
            "path": f.get("path", ""),
            "size": f.get("size", 0),
        }
        for f in files
    ]


# ==================== Library 知识加载 ====================

def _list_library_files() -> list[str]:
    """返回 Library 目录下所有 .md 文件名列表。"""
    if not _LIBRARY_DIR.exists():
        return []
    return sorted(p.name for p in _LIBRARY_DIR.glob("*.md"))


# ==================== System Prompt 构建 ====================

def _build_system_prompt(files: list[dict]) -> str:
    """构建包含 MIDI 文件上下文和 Library 知识库的 system prompt。"""
    if files:
        file_lines = []
        for i, f in enumerate(files, 1):
            size_kb = max(1, f.get("size", 0) // 1024)
            note_data = f.get("note_table", "")
            if note_data:
                file_lines.append(
                    f"{i}. {f['name']} ({size_kb} KB)\n"
                    f"   note_table 数据:\n{note_data}"
                )
            else:
                file_lines.append(f"{i}. {f['name']} ({size_kb} KB)")
        file_list = "\n\n".join(file_lines)
    else:
        file_list = "（暂无文件）"

    library_files = _list_library_files()

    prompt = (
        "你是一位精通乐理的音乐 AI 助手，帮助用户处理 MIDI 音乐文件。"
        "你的核心工作原则是：**在没有查阅 Library 知识库文件之前，绝对不允许进行任何音乐创作或回答专业乐理问题。**\n\n"
        "## 铁律（不可违背）\n"
        "1. **必须先读文件**：任何涉及音乐理论、创作技巧、编曲方法的回答，"
        "都必须先调用 `read_library_file` 读取对应文件。禁止凭记忆回答。\n"
        "2. **禁止跳过工具**：如果用户要求你创作音乐（配和弦、写旋律、设计转音、编曲等），"
        "你的**第一步**必须是读取相关知识文件，否则你无法获得正确的创作指导。\n"
        "3. **多文件并行**：可以一次性调用多个 `read_library_file` 同时读取多个相关文件。\n"
        "4. **内容优先**：读取文件后，必须严格遵循文件中的方法论和指导原则进行创作。\n\n"
    )

    prompt += "## 标准工作流程\n"
    prompt += "1. 用户提出请求（如'帮我配和弦'）\n"
    prompt += "2. ⚡ 判断请求类型 → 确定需要读取哪些 Library 文件（见下方映射表）\n"
    prompt += "3. ⚡ 调用 `read_library_file` 读取这些文件\n"
    prompt += "4. 基于文件内容，调用 `create_midi` 或其他工具完成创作\n"
    prompt += "5. 用自然语言向用户解释你的创作思路和结果\n\n"

    prompt += f"{_NOTE_TABLE_INTRO}\n\n"

    prompt += "## 乐理知识库文件映射\n"
    prompt += "以下是你拥有的知识文件，**你必须在对应场景下主动读取**：\n\n"

    if library_files:
        for i, fname in enumerate(library_files, 1):
            # Extract a simple description from filename
            desc = fname.replace(".md", "").replace("_", " ")
            prompt += f"{i}. `{fname}` → {desc}\n"
        prompt += "\n"

    prompt += "### 强制读取映射（优先级从高到低）\n"
    prompt += "- **配和弦/和声** → `02_配和弦指南.md` + `08_和弦进行词典.md` + `06_和弦进阶与风格化.md`\n"
    prompt += "- **转音/花腔设计** → `04_转音设计指南.md`\n"
    prompt += "- **歌词翻译** → `03_歌词翻译指南.md`\n"
    prompt += "- **旋律写作** → `10_旋律写作与记忆点.md` + `09_音域运用与音程写作.md`\n"
    prompt += "- **节奏设计** → `11_节奏与律动.md`\n"
    prompt += "- **编曲/配器** → `19_配器法入门.md` + `16_织体关系与声部配合.md`\n"
    prompt += "- **调性/调式分析** → `13_调性识别与和弦功能分析.md` + `17_调式互换与转调.md`\n"
    prompt += "- **风格化创作** → `12_风格化写作与编曲要素.md` + `05_作曲编曲通用技巧.md`\n"
    prompt += "- **多声部写作** → `18_对位与多声部写作.md` + `16_织体关系与声部配合.md`\n"
    prompt += "- **演奏润色/MIDI 真实感** → `14_MIDI真实感与演奏润色.md`\n"
    prompt += "- **曲式结构** → `20_曲式结构与段落设计.md`\n"
    prompt += "- **歌词创作** → `15_歌词创作指南.md`\n"
    prompt += "- **即兴创作** → `07_音阶与即兴创作模板.md`\n"
    prompt += "- **基础乐理** → `01_乐理基础.md`\n\n"

    prompt += "## 错误示例（绝对禁止）\n"
    prompt += "❌ 用户：'帮我写段 C 大调的旋律' → AI 直接开始创作（未读取文件）\n"
    prompt += "✅ 正确：先读取 `10_旋律写作与记忆点.md` 和 `09_音域运用与音程写作.md`，再创作\n\n"
    prompt += "❌ 用户：'这首歌用什么和弦好' → AI 凭记忆回答（未读取文件）\n"
    prompt += "✅ 正确：先读取 `02_配和弦指南.md` 和 `08_和弦进行词典.md`，再回答\n\n"

    prompt += "## 工具使用规范\n"
    prompt += "你拥有以下工具，全部通过 `tools/call` 调用：\n\n"
    prompt += "- **`read_library_file`**：**[最常用]** 读取知识文件。参数：`filename`（文件名，如 `02_配和弦指南.md`）\n"
    prompt += "- **`list_midi_files`**：列出当前项目的 MIDI 文件。无参数。\n"
    prompt += "- **`parse_midi`**：解析 MIDI 文件为 note_table。参数：`filename`\n"
    prompt += "- **`create_midi`**：从 note_table 创建 MIDI 文件。参数：`filename`, `bpm`, `notes`\n"
    prompt += "- **`delete_midi`**：删除 MIDI 文件。参数：`filename`\n"
    prompt += "- **`create_folder`**：创建文件夹（支持多级子目录）。参数：`name`（如 `drums`、`sectionA/drums`）\n"
    prompt += "- **`list_project_structure`**：以树状图查看项目所有 MIDI 文件的目录层级。无参数。\n\n"
    prompt += "**文件路径说明**：`filename`/`name` 可含子目录路径（如 `drums/beat.mid`），文件会保存到对应子目录并在工作区与项目目录双写。\n\n"

    prompt += "## 重要提醒\n"
    prompt += "- **你现在的身份是乐理专家，不是通用 AI**。所有专业问题都必须基于 Library 文件回答。\n"
    prompt += "- 如果用户的问题涉及多个方面（如'帮我写首流行歌'），请同时读取所有相关文件。\n"
    prompt += "- 读取文件后，请在回复中说明'我查阅了 XX 文件，其中提到...'，让用户知道你的依据。\n"
    prompt += "- **如果用户只是闲聊（如'你好'），不需要读取文件。但一旦涉及创作任务，必须读取。**\n\n"

    return prompt


# ==================== 设置加载 ====================

def _load_settings() -> dict:
    """从 config 加载 API 设置。"""
    settings = config.load_settings()
    raw_base_url = settings.get("base_url", "") or config.BASE_URL
    raw_api_path = settings.get("api_path", "") or config.API_PATH
    try:
        base_url = config.validate_base_url(raw_base_url, raw_api_path)
    except ValueError:
        logger.warning("base_url 验证失败，使用默认值: %s", config.BASE_URL)
        base_url = config.BASE_URL
    return {
        "api_key": settings.get("api_key") or config.get_api_key(),
        "base_url": base_url,
        "api_path": settings.get("api_path", ""),
        "model": settings.get("model") or config.MODEL,
        "max_tokens": settings.get("max_tokens"),
        "max_completion_tokens": settings.get("max_completion_tokens"),
        "reasoning_effort": settings.get("reasoning_effort"),
        "thinking_enabled": settings.get("thinking_enabled", True),
    }


def _settings_summary() -> dict:
    """对话侧栏展示的设置摘要。"""
    s = _load_settings()
    return {
        "model": s["model"],
        "reasoning_effort": s["reasoning_effort"] or "auto",
        "context": "auto",
    }


# ==================== MCP 子进程管理 ====================

def _ensure_mcp_process() -> subprocess.Popen | None:
    """确保 MCP 子进程正在运行，返回 Popen 对象或 None。"""
    global _mcp_process, _mcp_initialized, _mcp_tools_cache

    with _mcp_lock:
        if _mcp_process and _mcp_process.poll() is None:
            return _mcp_process

        try:
            env = os.environ.copy()
            if _current_project_id:
                env["AI_MIDI_OUTPUT_DIR"] = str(
                    project_manager.get_midi_base_dir(_current_project_id)
                )
                mirror = project_manager.get_midi_mirror_dir(_current_project_id)
                if mirror is not None:
                    env["AI_MIDI_MIRROR_DIR"] = str(mirror)
            _mcp_process = subprocess.Popen(
                [sys.executable, str(_MCP_SCRIPT)],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
                encoding="utf-8",
                bufsize=0,
                env=env,
            )
            _mcp_initialized = False
            # 新进程的工具列表缓存失效（reader 状态由 _mcp_recv 懒重建）
            _mcp_tools_cache = None
            # 等待进程就绪
            time.sleep(config.MCP_STARTUP_SLEEP)
            if _mcp_process.poll() is not None:
                logger.error("MCP 进程启动后立即退出")
                _mcp_process = None
                return None
            # 初始化 MCP 握手，失败则清理进程
            if not _mcp_handshake(_mcp_process):
                logger.error("MCP 握手失败，关闭子进程")
                _close_mcp_process()
                return None
            _mcp_initialized = True
            return _mcp_process
        except (OSError, subprocess.SubprocessError):
            logger.exception("启动 MCP 进程失败")
            _mcp_process = None
            return None


def _mcp_handshake(proc: subprocess.Popen) -> bool:
    """执行 MCP 初始化握手。返回是否成功。"""
    init_request = {
        "jsonrpc": "2.0",
        "id": 0,
        "method": "initialize",
        "params": {
            "protocolVersion": "2024-11-05",
            "capabilities": {},
            "clientInfo": {"name": "ai-midi-client", "version": "1.0.0"},
        },
    }
    _mcp_send(proc, init_request)
    init_result = _mcp_recv(proc)  # initialize result
    if init_result is None or "error" in init_result:
        logger.error("MCP initialize 失败: %s", init_result)
        return False
    # Send initialized notification
    _mcp_send(proc, {"jsonrpc": "2.0", "method": "notifications/initialized"})
    return True


def _mcp_send(proc: subprocess.Popen, message: dict) -> None:
    """向 MCP 进程发送 JSON-RPC 消息。"""
    payload = json.dumps(message, ensure_ascii=False)
    line = f"{payload}\n"
    proc.stdin.write(line)
    proc.stdin.flush()


def _ensure_mcp_reader(proc: subprocess.Popen | None) -> bool:
    """确保为当前 MCP 进程启动唯一的后台 reader 线程。

    进程切换后旧线程自然退出（daemon + 旧 stdout EOF），新进程启动新线程。
    """
    st = _mcp_reader_state
    if st["proc"] is proc and st["thread"] and st["thread"].is_alive():
        return True
    if proc is None or proc.poll() is not None:
        return False
    q: queue.Queue = queue.Queue()
    st["proc"] = proc
    st["queue"] = q

    def _read_loop():
        # 闭包捕获当前 q：旧进程的线程只写旧队列，不会污染新进程的队列
        try:
            while True:
                line = proc.stdout.readline()
                if not line:  # EOF（进程关闭/崩溃）
                    q.put(_MCP_EOF)
                    return
                q.put(line)
        except (OSError, ValueError):
            q.put(_MCP_EOF)

    t = threading.Thread(target=_read_loop, daemon=True, name="mcp-reader")
    t.start()
    st["thread"] = t
    return True


def _mcp_recv(proc: subprocess.Popen, timeout: float = config.MCP_RESPONSE_TIMEOUT) -> dict | None:
    """从 MCP 进程读取一条 JSON-RPC 响应（带超时）。

    由进程级单例 reader 线程负责 readline，本函数只做队列轮询；
    超时直接返回 None，不会留下阻塞线程。
    """
    if not _ensure_mcp_reader(proc):
        return None
    q = _mcp_reader_state["queue"]
    deadline = time.time() + timeout
    while True:
        remaining = deadline - time.time()
        if remaining <= 0:
            return None
        try:
            item = q.get(timeout=remaining)
        except queue.Empty:
            return None
        if item is _MCP_EOF:
            return None
        try:
            return json.loads(item.strip())
        except json.JSONDecodeError:
            continue  # 跳过坏行，继续等待下一条


def _mcp_call_tool(name: str, arguments: dict) -> str:
    """调用 MCP Server 上的一个工具，返回文本结果。"""
    proc = _ensure_mcp_process()
    if not proc:
        return "错误：MCP 服务未启动"

    global _mcp_request_id
    with _mcp_lock:
        _mcp_request_id += 1
        req_id = _mcp_request_id

    request = {
        "jsonrpc": "2.0",
        "id": req_id,
        "method": "tools/call",
        "params": {"name": name, "arguments": arguments},
    }
    _mcp_send(proc, request)

    # 读取响应（可能有中间通知，需要找到匹配 id 的响应）
    deadline = time.time() + config.MCP_RESPONSE_TIMEOUT
    while time.time() < deadline:
        response = _mcp_recv(proc, timeout=deadline - time.time())
        if response is None:
            break
        if response.get("id") == req_id:
            if "error" in response:
                return f"错误：MCP 工具返回错误 — {response['error']}"
            result = response.get("result", {})
            content = result.get("content", [])
            if content and isinstance(content, list):
                for item in content:
                    if item.get("type") == "text":
                        return item.get("text", "")
            return str(result)
    return "错误：MCP 工具调用超时"


def _mcp_list_tools() -> list[dict]:
    """获取 MCP Server 的工具列表，转换为 OpenAI function definitions。

    结果在进程存活期间缓存（工具列表是静态的），进程重启时由
    _close_mcp_process / _ensure_mcp_process 清空。
    """
    global _mcp_request_id, _mcp_tools_cache
    if _mcp_tools_cache is not None:
        return _mcp_tools_cache

    proc = _ensure_mcp_process()
    if not proc:
        return []

    with _mcp_lock:
        _mcp_request_id += 1
        req_id = _mcp_request_id

    request = {
        "jsonrpc": "2.0",
        "id": req_id,
        "method": "tools/list",
        "params": {},
    }
    _mcp_send(proc, request)

    deadline = time.time() + config.MCP_LIST_TIMEOUT
    while time.time() < deadline:
        response = _mcp_recv(proc, timeout=deadline - time.time())
        if response is None:
            break
        if response.get("id") == req_id:
            if "error" in response:
                logger.error("MCP tools/list 失败: %s", response["error"])
                return []
            result = response.get("result", {})
            mcp_tools = result.get("tools", [])
            # 转换为 OpenAI function calling 格式
            openai_tools = []
            for tool in mcp_tools:
                func = {
                    "type": "function",
                    "function": {
                        "name": tool.get("name", ""),
                        "description": tool.get("description", ""),
                        "parameters": tool.get("inputSchema", {"type": "object", "properties": {}}),
                    },
                }
                openai_tools.append(func)
            _mcp_tools_cache = openai_tools
            return openai_tools
    return []


def _close_mcp_process() -> None:
    """关闭 MCP 子进程。"""
    global _mcp_process, _mcp_tools_cache
    with _mcp_lock:
        if _mcp_process:
            try:
                if _mcp_process.stdin and not _mcp_process.stdin.closed:
                    _mcp_process.stdin.close()
            except OSError:
                pass
            if _mcp_process.poll() is None:
                try:
                    _mcp_process.terminate()
                    _mcp_process.wait(timeout=2)
                except (OSError, subprocess.TimeoutExpired):
                    try:
                        _mcp_process.kill()
                        _mcp_process.wait(timeout=2)
                    except (OSError, subprocess.TimeoutExpired):
                        logger.warning("MCP 进程 kill 后未能 wait 退出")
        _mcp_process = None
        _mcp_initialized = False
        # 进程关闭：工具缓存与 reader 单例状态一并失效
        # （旧 reader 线程是 daemon，读到 EOF 后自行退出）
        _mcp_tools_cache = None
        _mcp_reader_state["proc"] = None
        _mcp_reader_state["thread"] = None
        _mcp_reader_state["queue"] = None


# ==================== 文件工具 ====================

def _unique_dest_path(dest_dir, basename: str):
    stem, suffix = os.path.splitext(basename)
    candidate = dest_dir / basename
    counter = 2
    while candidate.exists():
        candidate = dest_dir / f"{stem}_{counter}{suffix}"
        counter += 1
    return candidate


def _file_choice_value(file_info: dict) -> str:
    return str(file_info.get("path") or file_info.get("name", ""))


def _file_label(file_info: dict) -> str:
    size_kb = max(1, file_info.get("size", 0) // 1024)
    return f"{file_info.get('name', '')}  ({size_kb} KB)"


def _selected_files(files: list[dict], selected) -> list[dict]:
    if not selected:
        return []
    selected_values = {str(item[1] if isinstance(item, (list, tuple)) else item) for item in selected}
    selected_names = {value.split("  (")[0] for value in selected_values}
    return [
        file_info for file_info in files
        if _file_choice_value(file_info) in selected_values or file_info.get("name") in selected_names
    ]


def _persist_project_state(project_id: str | None, full_history, midi_files: list[dict]) -> None:
    if project_id:
        project_manager.save_history(project_id, full_history or [], midi_files or [])


def _resolve_created_midi_path(filename: str) -> Path:
    """Resolve a tool-created MIDI path without allowing output-dir escape."""
    if _current_project_id:
        return project_manager.resolve_midi_path(_current_project_id, filename)
    if not filename:
        raise ValueError("filename 不能为空")

    output_dir = config.OUTPUT_DIR.resolve()
    filepath = (output_dir / filename).resolve()
    if filepath == output_dir or not filepath.is_relative_to(output_dir):
        raise ValueError(f"filename 不能逃逸 output 目录: {filename}")
    return filepath


def _filepath_relpath(filepath) -> str:
    """把绝对路径转成相对项目主目录的 posix 路径（含子目录）；无项目时返回文件名。"""
    if _current_project_id:
        try:
            base = project_manager.get_midi_base_dir(_current_project_id)
            return Path(filepath).resolve().relative_to(base).as_posix()
        except (ValueError, OSError):
            pass
    return Path(filepath).name


# ==================== MIDI 文件管理 ====================

# 项目回收站目录名（删除的文件移入此处，撤销时移回）
_TRASH_DIRNAME = ".trash"


def _trash_dir(project_id: str) -> Path:
    """项目回收站目录：projects/<id>/.trash。"""
    return project_manager.project_dir(project_id) / _TRASH_DIRNAME


def _clear_trash(project_id: str) -> None:
    """清空项目回收站。

    每次 push 新撤销快照时调用：撤销只回退最近一次操作，
    旧快照对应的回收站内容随即作废。
    """
    trash = _trash_dir(project_id)
    if trash.is_dir():
        shutil.rmtree(trash, ignore_errors=True)


def _cleanup_empty_parents(dirpath: Path, stop: Path) -> None:
    """自 dirpath 向上删除空目录，直到 stop 为止（含 stop 自身不删）。"""
    p = Path(dirpath)
    stop = Path(stop)
    while p != stop and p.is_dir():
        try:
            p.rmdir()
        except OSError:
            break
        p = p.parent


def _move_to_trash(project_id: str, file_info: dict) -> str | None:
    """把文件移入项目回收站（保留相对路径）。

    绑定工作区时主文件即工作区文件；镜像由同步机制自动清理
    （工作区无该文件 -> 同步删除镜像）。移动失败（如文件被占用）
    返回 None，调用方应保留该文件在清单中。
    """
    src = file_info.get("path")
    name = file_info.get("name", "")
    if not src or not name or not os.path.isfile(src):
        return None
    trash = _trash_dir(project_id)
    dst = trash / name
    try:
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(src, dst)
    except OSError:
        logger.exception("移入回收站失败: %s", src)
        return None
    # 注意：不清理源空父目录——文件夹与文件完全独立，
    # 删除文件不影响文件夹（空文件夹保留）
    return name


def _restore_from_trash(project_id: str, trash_rels: list[str]) -> None:
    """把回收站中的文件移回主目录（撤销删除时调用）。

    绑定工作区时主目录即工作区，镜像由下次同步自动复制回来。
    """
    base = project_manager.get_midi_base_dir(project_id)
    trash = _trash_dir(project_id)
    for rel in trash_rels or []:
        src = trash / rel
        if not src.is_file():
            continue
        dst = base / rel
        try:
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.move(str(src), str(dst))
        except OSError:
            logger.exception("回收站恢复失败: %s", rel)
        # 清理该文件在回收站中的空父目录（逐级向上到回收站根）
        _cleanup_empty_parents(src.parent, trash)
    # 回收站根目录若已空则一并删除（恢复失败的文件保留其中，rmdir 失败跳过）
    if trash.is_dir():
        try:
            trash.rmdir()
        except OSError:
            pass


# 撤销栈上限：快照深拷贝整个 midi_files（含 note_table 文本），
# 不设上限长期对话内存无界增长。超限时丢弃最旧快照
_UNDO_STACK_LIMIT = 20


def _push_undo(project_id: str, undo_stack: list, files: list[dict], trash_rels: list[str] | None = None) -> list:
    """入栈撤销快照（新格式 {"files", "trash"}），并作废旧回收站内容。

    兼容旧格式：旧条目为纯清单快照列表，_on_undo 弹出时按类型区分。
    """
    _clear_trash(project_id)
    new_stack = undo_stack + [{"files": copy.deepcopy(files), "trash": trash_rels or []}]
    if len(new_stack) > _UNDO_STACK_LIMIT:
        new_stack = new_stack[-_UNDO_STACK_LIMIT:]
    return new_stack


def _on_upload(files, current_list, undo_stack, project_id, full_history, names=None):
    """上传文件，解析为 note_table 并追加到列表。文件复制到项目 midi 目录。

    兼容旧接口签名；files 为带 .name 路径的文件对象列表（server 端已转临时路径）。
    names 为与 files 一一对应的原始文件名（mkstemp 临时名不能作为最终文件名）。
    """
    current_list = current_list or []
    undo_stack = undo_stack or []
    if not files:
        return current_list, undo_stack, _get_choices(current_list)
    new_files = []
    # 确定目标目录：绑定工作区->工作区(主)，否则项目 midi 目录；并准备镜像
    if project_id:
        dest_dir = project_manager.get_midi_base_dir(project_id)
        dest_dir.mkdir(parents=True, exist_ok=True)
        mirror_dir = project_manager.get_midi_mirror_dir(project_id)
    else:
        dest_dir = config.OUTPUT_DIR
        dest_dir.mkdir(exist_ok=True)
        mirror_dir = None

    for i, f in enumerate(files):
        src_path = f.name if hasattr(f, "name") else str(f)
        basename = os.path.basename(names[i]) if names and i < len(names) else os.path.basename(src_path)
        # 仅接受 .mid/.midi 扩展名（防止用户绕过前端 file_types 限制）
        if not basename.lower().endswith((".mid", ".midi")):
            logger.warning("跳过非 MIDI 文件: %s", basename)
            continue
        # 复制到主目录（项目 midi 或工作区）
        dest_path = _unique_dest_path(dest_dir, basename)
        try:
            shutil.copy2(src_path, dest_path)
        except OSError:
            logger.exception("复制 MIDI 文件失败: %s -> %s", src_path, dest_path)
            continue
        # 验证文件是合法 MIDI：mido 解析失败的文件直接拒绝。
        # 校验必须在镜像写之前——否则失败时只删了主副本，
        # 镜像副本残留（镜像与主目录不一致）
        try:
            mido.MidiFile(str(dest_path))
        except (OSError, ValueError, EOFError):
            logger.warning("文件不是合法 MIDI,已删除: %s", dest_path)
            try:
                dest_path.unlink()
            except OSError:
                pass
            continue
        # 镜像双写（绑定工作区时同步到 projects/<id>/midi）
        if mirror_dir is not None:
            try:
                mp = mirror_dir / dest_path.relative_to(dest_dir)
                mp.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(dest_path, mp)
            except OSError:
                logger.exception("镜像同步上传文件失败: %s", dest_path)
        try:
            note_table = get.get_note(str(dest_path), save_to_file=False)
        except OSError:
            logger.exception("读取 MIDI 文件失败: %s", dest_path)
            note_table = []
        note_table_str = "\n".join(note_table) if note_table else ""
        rel = dest_path.relative_to(dest_dir).as_posix()
        new_files.append({
            "name": rel,
            "path": str(dest_path),
            "size": dest_path.stat().st_size if dest_path.exists() else 0,
            "note_table": note_table_str,
        })
    updated = current_list + new_files
    new_undo_stack = _push_undo(project_id, undo_stack, current_list) if new_files else undo_stack
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, _get_choices(updated)


def _on_delete(current_list, selected, undo_stack, project_id, full_history):
    """删除勾选的文件（磁盘上移入项目回收站，撤销时移回）。

    返回 (updated, undo_stack, choices, failed)：failed 为磁盘删除失败
    （如文件被占用）仍保留在清单中的文件名列表。
    """
    current_list = current_list or []
    undo_stack = undo_stack or []
    selected_file_list = _selected_files(current_list, selected)
    if not current_list or not selected_file_list:
        return current_list, undo_stack, _get_choices(current_list), []
    selected_values = {_file_choice_value(file_info) for file_info in selected_file_list}

    # 磁盘删除：移入回收站；失败（被占用等）的文件保留在清单
    _clear_trash(project_id)
    trash_rels = []
    kept = []
    failed = []
    for file_info in current_list:
        if _file_choice_value(file_info) in selected_values:
            rel = _move_to_trash(project_id, file_info)
            if rel:
                trash_rels.append(rel)
            else:
                kept.append(file_info)
                failed.append(file_info.get("name", ""))
        else:
            kept.append(file_info)

    if not trash_rels:
        # 全部删除失败：不做任何变更，不产生撤销条目
        return current_list, undo_stack, _get_choices(current_list), failed

    updated = kept
    new_undo_stack = undo_stack + [{
        "files": copy.deepcopy(current_list),
        "trash": trash_rels,
    }]
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, _get_choices(updated), failed


def _on_undo(undo_stack, current_files, project_id, full_history):
    """撤销最近一次操作（上传/删除/移动/发送快照）。

    删除类条目（新格式 dict）会同时把回收站中的文件移回磁盘、
    把移动过的文件移回原位；旧格式条目（纯清单快照）仅恢复清单，
    保持兼容。
    """
    current_files = current_files or []
    undo_stack = undo_stack or []
    if not undo_stack:
        return current_files, [], _get_choices(current_files)
    entry = undo_stack.pop()
    if isinstance(entry, dict):
        restored = entry.get("files", current_files)
        _restore_from_trash(project_id, entry.get("trash") or [])
        _undo_moves(
            project_id,
            entry.get("moves") or [],
            entry.get("dir_moves") or [],
        )
        # 重建被删文件夹的空子目录（含文件夹本身；trash 恢复已重建含文件的层级）
        for rel in entry.get("mkdirs") or []:
            for root in (project_manager.get_midi_base_dir(project_id),
                         project_manager.get_midi_mirror_dir(project_id)):
                if root is None:
                    continue
                try:
                    (root / rel).mkdir(parents=True, exist_ok=True)
                except OSError:
                    logger.exception("撤销时重建文件夹失败: %s", rel)
    else:
        restored = entry
    _persist_project_state(project_id, full_history, restored)
    return restored, undo_stack, _get_choices(restored)


# ==================== 文件夹与移动 ====================

def _dirs_for(project_id: str) -> list[str]:
    """当前主目录下的全部子目录相对路径（前端文件树显示空文件夹用）。"""
    return project_manager.scan_dirs(project_manager.get_midi_base_dir(project_id))


def _on_create_folder(project_id: str, name: str, parent: str = "") -> dict:
    """在当前选中层级下新建文件夹（主目录 + 镜像双写）。

    返回 {"dirs": [...]}；名称非法或路径已存在时抛 ValueError。
    """
    name = (name or "").strip().strip("/")
    if not name:
        raise ValueError("文件夹名不能为空")
    if ".." in name or "/" in name or "\\" in name:
        raise ValueError("文件夹名不能包含路径分隔符或 ..")
    parent = (parent or "").strip().strip("/")
    rel = f"{parent}/{name}" if parent else name

    base = project_manager.get_midi_base_dir(project_id)
    target = (base / rel).resolve()
    if target != base and not target.is_relative_to(base):
        raise ValueError("文件夹路径非法")
    if target.exists():
        if target.is_dir():
            raise ValueError(f"文件夹已存在: {rel}")
        raise ValueError(f"路径已存在且不是文件夹: {rel}")

    target.mkdir(parents=True, exist_ok=True)
    mirror = project_manager.get_midi_mirror_dir(project_id)
    if mirror is not None:
        try:
            (mirror / rel).mkdir(parents=True, exist_ok=True)
        except OSError:
            logger.exception("镜像创建文件夹失败: %s", rel)
    return {"dirs": _dirs_for(project_id)}


def _on_rename_folder(project_id: str, old_rel: str, new_name: str,
                      current_list, undo_stack, full_history):
    """重命名文件夹（主目录 + 镜像目录级 move，清单 name/path 前缀同步替换）。

    old_rel 为文件夹相对项目根目录的路径（如 "drums" 或 "a/b"），new_name
    只允许单个名字（不含路径分隔符/..）。返回 (updated, undo_stack, error)，
    error 非空表示校验/执行失败（此时无任何变更）。
    """
    current_list = current_list or []
    undo_stack = undo_stack or []
    old_rel = (old_rel or "").strip().strip("/")
    new_name = (new_name or "").strip().strip("/")
    if not old_rel:
        return current_list, undo_stack, "文件夹路径为空"
    if not new_name or "/" in new_name or "\\" in new_name or ".." in new_name:
        return current_list, undo_stack, "文件夹名不能为空或包含路径分隔符/.."
    base = project_manager.get_midi_base_dir(project_id)
    src = base / old_rel
    if not src.is_dir():
        return current_list, undo_stack, f"文件夹不存在: {old_rel}"
    parent_rel = os.path.dirname(old_rel)
    new_rel = f"{parent_rel}/{new_name}" if parent_rel else new_name
    if new_rel == old_rel:
        return current_list, undo_stack, ""
    dst = base / new_rel
    if dst.exists():
        return current_list, undo_stack, f"目标已存在: {new_rel}"

    try:
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(src), str(dst))
    except OSError:
        logger.exception("重命名文件夹失败: %s -> %s", old_rel, new_rel)
        return current_list, undo_stack, f"重命名失败: {old_rel}"

    # 镜像同步重命名（存在才移动；不存在则由工作区同步补齐）
    mirror = project_manager.get_midi_mirror_dir(project_id)
    if mirror is not None:
        m_src = mirror / old_rel
        m_dst = mirror / new_rel
        if m_src.is_dir() and not m_dst.exists():
            try:
                shutil.move(str(m_src), str(m_dst))
            except OSError:
                logger.exception("镜像重命名文件夹失败: %s -> %s", old_rel, new_rel)

    # 清单前缀替换：old_rel/xxx.mid -> new_rel/xxx.mid（path 同步指向新位置）
    prefix = old_rel + "/"
    updated = [dict(f) for f in current_list]
    for f in updated:
        fname = f.get("name", "")
        if fname.startswith(prefix):
            f["name"] = new_rel + fname[len(old_rel):]
            f["path"] = str(base / f["name"])

    new_undo_stack = undo_stack + [{
        "files": copy.deepcopy(current_list),
        "trash": [],
        "moves": [],
        "dir_moves": [{"from": old_rel, "to": new_rel}],
    }]
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, ""


def _on_delete_folder(project_id: str, folder_rel: str, current_list,
                      undo_stack, full_history):
    """删除文件夹（显式操作）：其中文件逐个移入回收站（保留相对路径），
    空目录（含子目录）rmdir 删除；撤销时 trash 恢复自动重建整个层级。

    返回 (updated, undo_stack, error)：error 非空表示有文件被占用无法删除，
    此时保留文件夹及全部文件，不产生撤销条目。
    """
    current_list = current_list or []
    undo_stack = undo_stack or []
    folder_rel = (folder_rel or "").strip().strip("/")
    if not folder_rel:
        return current_list, undo_stack, "文件夹路径为空"
    base = project_manager.get_midi_base_dir(project_id)
    folder = base / folder_rel
    if not folder.is_dir():
        return current_list, undo_stack, f"文件夹不存在: {folder_rel}"

    prefix = folder_rel + "/"
    outside = [f for f in current_list if not f.get("name", "").startswith(prefix)]

    # 文件夹内全部文件移入回收站（trash 保留相对路径，撤销时自动重建文件夹）
    _clear_trash(project_id)
    trash_rels = []
    for f in current_list:
        if f.get("name", "").startswith(prefix):
            rel = _move_to_trash(project_id, f)
            if not rel:
                # 文件被占用：回滚已移入回收站的文件（移回原路径），
                # 兑现"失败时文件夹与全部文件原样保留"的契约——否则
                # 前 N-1 个文件已离开磁盘，前端清单却仍显示它们（幽灵文件）
                _restore_from_trash(project_id, trash_rels)
                return current_list, undo_stack, f"无法删除被占用的文件: {f.get('name')}"
            trash_rels.append(rel)

    # 记录全部子目录（含空目录），撤销时逐级重建（统一正斜杠相对路径）
    sub_dirs = [str(p.relative_to(base)).replace(os.sep, "/")
                for p in folder.rglob("*") if p.is_dir()]
    if folder.is_dir():
        try:
            shutil.rmtree(folder)
        except OSError:
            logger.exception("删除文件夹失败: %s", folder_rel)
            # 目录删不掉（残留空目录）不影响撤销：文件已在回收站，
            # 恢复时自动重建整个层级

    # 镜像同步删除（不存在则跳过，由工作区同步补齐）
    mirror = project_manager.get_midi_mirror_dir(project_id)
    if mirror is not None:
        mf = mirror / folder_rel
        if mf.is_dir():
            try:
                shutil.rmtree(mf)
            except OSError:
                logger.exception("镜像删除文件夹失败: %s", folder_rel)

    new_undo_stack = undo_stack + [{
        "files": copy.deepcopy(current_list),
        "trash": trash_rels,
        "mkdirs": [folder_rel] + sub_dirs,
    }]
    _persist_project_state(project_id, full_history, outside)
    return outside, new_undo_stack, ""


def _undo_moves(project_id: str, moves: list[dict], dir_moves: list[dict] | None = None) -> None:
    """撤销移动：把文件从目标位置移回原位（磁盘，主目录 + 镜像）；
    目录级撤销（文件夹重命名）：目录整体移回。"""
    base = project_manager.get_midi_base_dir(project_id)
    mirror = project_manager.get_midi_mirror_dir(project_id)
    for mv in moves or []:
        from_name = mv.get("from", "")
        to_name = mv.get("to", "")
        if not from_name or not to_name:
            continue
        for root in (base, mirror):
            if root is None:
                continue
            src = root / to_name
            dst = root / from_name
            if not src.is_file():
                continue
            try:
                dst.parent.mkdir(parents=True, exist_ok=True)
                shutil.move(str(src), str(dst))
            except OSError:
                logger.exception("撤销移动失败: %s -> %s", to_name, from_name)
    # 目录级撤销（文件夹重命名）：目录整体移回
    for dmv in dir_moves or []:
        d_from = dmv.get("from", "")
        d_to = dmv.get("to", "")
        if not d_from or not d_to:
            continue
        for root in (base, mirror):
            if root is None:
                continue
            src = root / d_to
            dst = root / d_from
            if not src.is_dir():
                continue
            try:
                dst.parent.mkdir(parents=True, exist_ok=True)
                shutil.move(str(src), str(dst))
            except OSError:
                logger.exception("撤销文件夹重命名失败: %s -> %s", d_to, d_from)


def _on_move(project_id: str, moves: list[dict], current_list, undo_stack, full_history):
    """移动/重命名文件（磁盘主目录 + 镜像，清单 name/path 同步更新）。

    moves: [{"name": "drums/a.mid", "target": "sectionA"}]，target 为空串
    表示移到根目录；可选 "rename" 字段提供新文件名（重命名，
    与 target 组合可实现"移动并改名"）。
    返回 (updated, undo_stack, failed)：failed 为失败仍保留原位置的文件名列表。
    """
    current_list = current_list or []
    undo_stack = undo_stack or []
    if not moves:
        return current_list, undo_stack, []
    base = project_manager.get_midi_base_dir(project_id)
    mirror = project_manager.get_midi_mirror_dir(project_id)
    by_name = {f.get("name"): f for f in current_list}

    updated = [dict(f) for f in current_list]
    undo_moves = []
    failed = []
    changed = False

    for mv in moves:
        old_name = mv.get("name", "")
        target = (mv.get("target") or "").strip().strip("/")
        rename = mv.get("rename")
        info = by_name.get(old_name)
        if not info:
            failed.append(old_name)
            continue
        # 目标目录防穿越：任意路径段含 ..（含反斜杠形式）一律拒绝。
        # 与 rename 的校验（1052 行）同规则；另外下方 resolve 后还有
        # containment 兜底（防绝对路径/盘符覆盖等 pathlib 拼接逃逸）
        if any(seg == ".." for seg in re.split(r"[/\\]", target)):
            failed.append(old_name)
            continue
        if rename is not None:
            # 重命名：新文件名来自 rename（同目录改名或移动并改名）
            rename = str(rename).strip().strip("/")
            if not rename or "/" in rename or "\\" in rename or ".." in rename:
                failed.append(old_name)
                continue
            new_name = f"{target}/{rename}" if target else rename
        else:
            basename = os.path.basename(old_name)
            new_name = f"{target}/{basename}" if target else basename
        if new_name == old_name:
            continue  # 目标就是当前位置
        # 目标冲突检查（磁盘与清单）
        if any(f.get("name") == new_name for f in updated):
            failed.append(old_name)
            continue
        src = (base / old_name).resolve()
        dst = (base / new_name).resolve()
        base_resolved = base.resolve()
        # 兜底防穿越：源/目标都必须落在项目主目录内（pathlib 拼接
        # 遇到绝对路径段/盘符会整体覆盖 base，strip 校验挡不住）
        if src == base_resolved or not src.is_relative_to(base_resolved) \
                or dst == base_resolved or not dst.is_relative_to(base_resolved):
            failed.append(old_name)
            continue
        if dst.exists():
            failed.append(old_name)
            continue
        if not src.is_file():
            failed.append(old_name)
            continue
        try:
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.move(str(src), str(dst))
        except OSError:
            logger.exception("移动文件失败: %s -> %s", old_name, new_name)
            failed.append(old_name)
            continue
        # 注意：不清理源空父目录——文件夹与文件完全独立，
        # 移动文件不影响文件夹（源空文件夹保留）
        # 镜像同步移动（存在才移动；不存在则跳过，由工作区同步补齐）
        if mirror is not None:
            m_src = mirror / old_name
            m_dst = mirror / new_name
            if m_src.is_file() and not m_dst.exists():
                try:
                    m_dst.parent.mkdir(parents=True, exist_ok=True)
                    shutil.move(str(m_src), str(m_dst))
                except OSError:
                    logger.exception("镜像移动失败: %s", old_name)
        # 更新清单条目（note_table 保留）
        for f in updated:
            if f.get("name") == old_name:
                f["name"] = new_name
                f["path"] = str(dst)
                break
        undo_moves.append({"from": old_name, "to": new_name})
        changed = True

    if not changed:
        return current_list, undo_stack, failed
    new_undo_stack = undo_stack + [{
        "files": copy.deepcopy(current_list),
        "trash": [],
        "moves": undo_moves,
    }]
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, failed


def _on_clear_chat(project_id: str | None, midi_files: list[dict]) -> tuple[list, list]:
    """清空对话并保留 MIDI 文件元数据。"""
    if project_id:
        project_manager.save_history(project_id, [], midi_files or [])
    return [], []


def _get_choices(files: list[dict]) -> list[tuple[str, str]]:
    """生成文件选择列表选项（兼容旧接口）。"""
    return [(_file_label(file_info), _file_choice_value(file_info)) for file_info in files]


def _download_path_for_names(project_id: str, names: list[str]) -> str | None:
    """按文件名（含子目录）解析项目内文件绝对路径。"""
    session = _get_session(project_id)
    name_set = set(names)
    for f in session["midi_files"]:
        if f.get("name") in name_set or os.path.basename(f.get("name", "")) in name_set:
            path = f.get("path")
            if path and os.path.isfile(path):
                return str(path)
    return None


def _download_files(project_id: str, names: list[str]) -> str | None:
    """下载勾选的文件；未勾选则下载全部。返回文件路径或 zip 路径。"""
    session = _get_session(project_id)
    if not session["midi_files"]:
        return None

    if names:
        files = []
        name_set = set(names)
        for f in session["midi_files"]:
            if f.get("name") in name_set or os.path.basename(f.get("name", "")) in name_set:
                files.append(f)
    else:
        files = list(session["midi_files"])

    if not files:
        return None

    if len(files) == 1 and os.path.isfile(files[0]["path"]):
        return files[0]["path"]

    zip_path = os.path.join(tempfile.gettempdir(), f"AI_MIDI_{project_id}_files.zip")
    used_names = set()
    try:
        with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
            for f in files:
                if os.path.isfile(f["path"]):
                    arcname = f["name"]
                    stem, suffix = os.path.splitext(arcname)
                    counter = 2
                    while arcname in used_names:
                        arcname = f"{stem}_{counter}{suffix}"
                        counter += 1
                    used_names.add(arcname)
                    zf.write(f["path"], arcname)
    except OSError:
        logger.exception("创建 zip 失败: %s", zip_path)
        if os.path.exists(zip_path):
            try:
                os.unlink(zip_path)
            except OSError:
                pass
        return None
    return zip_path


# ==================== 上下文压缩 ====================

# 压缩阈值（估算 tokens ≈ chars / 3）
_MAX_CONTEXT_CHARS = config.MAX_CONTEXT_CHARS
_COMPACT_KEEP_RECENT = config.COMPACT_KEEP_RECENT_MESSAGES


def _compact_full_history(full_history: list[dict]) -> list[dict]:
    """压缩完整历史，保留最近几轮对话，旧内容替换为摘要。"""
    # 找出所有 user 消息的索引位置
    user_indices = []
    for i, msg in enumerate(full_history):
        if msg.get("role") == "user":
            user_indices.append(i)

    if len(user_indices) <= _COMPACT_KEEP_RECENT:
        return full_history  # 对话轮数少，不需要压缩

    # 保留最近的 N 轮
    keep_from = user_indices[-(_COMPACT_KEEP_RECENT)]  # 倒数第 N 个 user 消息的位置

    # 提取需要摘要的内容（keep_from 之前的所有消息）
    old_messages = full_history[:keep_from]
    recent_messages = full_history[keep_from:]

    # 生成摘要
    summary = _generate_summary(old_messages)

    # 构建压缩后的历史：摘要 + 最近消息
    compacted = [summary] + recent_messages
    return compacted


def _generate_summary(old_messages: list[dict]) -> dict:
    """为旧消息生成摘要（不调用 AI，避免额外 API 调用）。"""
    lines = []
    for msg in old_messages:
        role = msg.get("role", "")
        content = msg.get("content", "") or ""
        if role == "user" and content:
            # 截取用户请求的前 100 字
            snippet = content[:config.SUMMARY_USER_TRUNCATE_CHARS].replace("\n", " ")
            lines.append(f"- 用户: {snippet}...")
        elif role == "assistant" and content:
            # 截取 AI 回复的前 80 字
            snippet = content[:config.SUMMARY_AI_TRUNCATE_CHARS].replace("\n", " ")
            lines.append(f"  AI: {snippet}...")

    summary_text = (
        "[上下文摘要 — 早期对话已压缩]\n"
        "以下是之前对话的摘要，你只需知道之前讨论过以下内容即可:\n"
        + "\n".join(lines)
    )
    return {"role": "user", "content": summary_text}


def _should_compact(messages: list[dict], full_history: list[dict]) -> bool:
    """判断是否需要压缩上下文。"""
    total_chars = sum(
        len(str(m.get("content", "")))
        for m in messages
    )
    # 也要考虑 system prompt
    return total_chars > _MAX_CONTEXT_CHARS


# ==================== AI 对话（MCP 工具调用） ====================

def _execute_tool_call(tool_call, midi_files: list[dict]) -> tuple[str, list[dict]]:
    """执行单个 MCP 工具调用，返回 (结果文本, 更新后的文件列表)。"""
    func_name = tool_call.get("function", {}).get("name", "")
    func_args = tool_call.get("function", {}).get("arguments", {})

    if isinstance(func_args, str):
        try:
            func_args = json.loads(func_args)
        except json.JSONDecodeError:
            return f"错误：工具参数解析失败 — {func_args}", midi_files

    result_text = _mcp_call_tool(func_name, func_args)
    updated_files = list(midi_files)

    # 如果创建了文件，更新文件列表
    if func_name == "create_midi":
        filename = func_args.get("filename", "")
        try:
            filepath = _resolve_created_midi_path(filename)
        except ValueError as exc:
            return f"错误：{exc}", updated_files
        if filepath.exists():
            # 从实际写入的文件重新解析，确保 UI 显示内容与 AI 读到的一致
            try:
                note_data = get.get_note(str(filepath), save_to_file=False)
            except Exception:
                note_data = func_args.get("notes", "") or func_args.get("note_table", "")
            rel = _filepath_relpath(filepath)
            file_info = {
                "name": rel,
                "path": str(filepath),
                "size": filepath.stat().st_size,
                "note_table": note_data,
            }
            updated_files = [f for f in updated_files if f.get("name") != rel and _file_choice_value(f) != str(filepath)]
            updated_files.append(file_info)

    # 如果删除了文件，更新文件列表
    elif func_name == "delete_midi":
        filename = func_args.get("filename", "")
        updated_files = [
            f for f in updated_files
            if f.get("name") != filename
            and os.path.basename(f.get("name", "")) != filename
        ]

    # 如果解析了文件，更新 note_table
    elif func_name == "parse_midi":
        filename = func_args.get("filename", "")
        for f in updated_files:
            if f.get("name") == filename or os.path.basename(f.get("name", "")) == filename:
                f["note_table"] = result_text
                break

    return result_text, updated_files


def _format_tool_log(tool_log: list[str]) -> str:
    """将工具调用日志格式化为可折叠的 HTML details 标签。"""
    if not tool_log:
        return ""
    return "\n\n".join(tool_log)


# summary 行参数值截断阈值：超过此长度的参数只显示短摘要，
# 完整内容放到 <details> 展开区里，避免折叠标签外露出一大段音符
_TOOL_ARG_SUMMARY_LIMIT = 80


def _truncate_arg_value(value) -> str:
    """把参数值转成短字符串，超长则截断并标注总长度。"""
    s = str(value)
    if len(s) <= _TOOL_ARG_SUMMARY_LIMIT:
        return s
    return s[:_TOOL_ARG_SUMMARY_LIMIT] + f"…（共 {len(s)} 字符）"


def _format_single_tool_entry(tc_name: str, tc_args, result_text: str) -> str:
    """格式化单个工具调用为可折叠 HTML details 标签。"""
    if isinstance(tc_args, dict):
        short_parts = ", ".join(
            f"{k}={_truncate_arg_value(v)}" for k, v in tc_args.items()
        )
        # 若有参数被截断，在展开区补一份完整参数
        has_long = any(
            len(str(v)) > _TOOL_ARG_SUMMARY_LIMIT for v in tc_args.values()
        )
        if has_long:
            full_args_lines = "\n".join(f"{k}: {v}" for k, v in tc_args.items())
            full_args_block = (
                f"**完整参数:**\n```\n{full_args_lines}\n```\n\n"
            )
        else:
            full_args_block = ""
    else:
        # 向后兼容：直接传字符串的情况
        short_parts = str(tc_args)
        full_args_block = ""

    return (
        f"<details>\n"
        f"<summary>🔧 调用 `{tc_name}({short_parts})`</summary>\n\n"
        f"{full_args_block}"
        f"```\n{result_text}\n```\n"
        f"</details>"
    )


def _format_pending_tool_entry(tc_name: str, tc_args) -> str:
    """工具「执行中」占位块：summary 与完成块完全一致（用户第一时间看到
    同样的工具标签 + ⏳ 进度标记），正文显示执行状态；完成后被完成块替换。"""
    if isinstance(tc_args, dict):
        short_parts = ", ".join(
            f"{k}={_truncate_arg_value(v)}" for k, v in tc_args.items()
        )
    else:
        # 向后兼容：直接传字符串的情况
        short_parts = str(tc_args)

    return (
        f"<details>\n"
        f"<summary>🔧 调用 `{tc_name}({short_parts})` ⏳</summary>\n\n"
        f'<div class="tool-pending">⏳ 正在执行 `{tc_name}`…</div>\n'
        f"</details>"
    )


def _make_tool_result_message(tool_call_id: str, result: str, name: str | None = None) -> dict:
    """构造 tool 角色的消息（OpenAI API 格式）。"""
    msg = {
        "role": "tool",
        "tool_call_id": tool_call_id,
        "content": result,
    }
    if name:
        msg["name"] = name
    return msg


# ==================== SSE 事件流 ====================

def _sse_event(data: dict) -> str:
    """把事件 dict 序列化为 SSE 数据帧。"""
    return f"data: {json.dumps(data, ensure_ascii=False)}\n\n"


def chat_stream(project_id: str, message: str, edit: bool = False):
    """多轮对话 SSE 事件流（生成器）。

    edit=True 且会话处于修改模式（pending_edit 存在）时，被编辑的
    用户消息（截断后会话末尾的那条）被新消息替换；否则清空修改状态
    后按正常消息追加。

    事件类型：
    - {"type": "chat", "messages": [...]}      完整对话显示列表
    - {"type": "files", "files": [...]}        文件列表变更
    - {"type": "download", "url": "..."}       新生成的 MIDI 可下载
    - {"type": "error", "message": "..."}      致命错误
    - {"type": "done"}                         结束
    """
    # ── 早期返回：空消息 ──
    if not message.strip():
        yield _sse_event({"type": "error", "message": "消息不能为空"})
        yield _sse_event({"type": "done"})
        return

    # ── 早期返回：未配置 API Key ──
    settings = _load_settings()
    if not settings["api_key"]:
        yield _sse_event({
            "type": "error",
            "message": "⚠ 请先在设置页填写并保存 API Key。",
        })
        yield _sse_event({"type": "done"})
        return

    # 并发对话串行化：持锁期间，任何其他 chat_stream 的首次 next() 阻塞，
    # 直到本流结束/断开（finally 释放，GeneratorExit 同样生效）
    _chat_lock.acquire()
    try:
        yield from _chat_stream_locked(project_id, message, edit)
    finally:
        _chat_lock.release()


def _chat_stream_locked(project_id: str, message: str, edit: bool = False):
    """chat_stream 的锁内主体（事件类型见 chat_stream 文档）。"""
    global _current_project_id

    # 重新读取设置（包装器只校验了 api_key 存在性；主体需要完整设置
    # 传给 _prepare_context 构造请求）
    settings = _load_settings()
    session = _get_session(project_id)
    _current_project_id = project_id

    # ── 绑定工作区：发送前完全同步工作区->projects，刷新文件列表 ──
    if project_manager.get_workspace_dir(project_id):
        try:
            session["midi_files"] = project_manager.sync_workspace_to_projects(
                project_id, session["midi_files"] or []
            )
        except Exception:  # noqa: BLE001
            logger.exception("发送消息前工作区同步失败")

    # ── 快照（用于撤销）──
    # 新格式条目（含 trash 字段）；入栈前作废旧回收站内容
    session["undo_stack"] = _push_undo(project_id, session["undo_stack"], session["midi_files"])

    # ── 消息已发送，草稿清除 ──
    project_manager.save_draft(project_id, "")

    # Lazy import to avoid circular dependency at module level
    from chat_pipeline import _execute_tool_loop, _prepare_context  # noqa: E402

    # ── 编辑发送：替换被编辑的用户消息 ──
    # 修改模式下 full_history / chat_display 已截断到该消息（含）。
    # 去掉最后一条被编辑的 user 消息后再追加新消息 = 替换语义，
    # 后续 _prepare_context / _finalize_response 自动以截断点为界
    # 重建 API 上下文并落盘（AI 不会看到该消息之后的内容）。
    # 进入编辑前的完整状态快照移入修改历史并落盘（不再丢弃）：
    # 供「撤回修改」把所有内容退回到这条消息发送之前（重启后仍可用）。
    # 放在早期返回（空消息/未配置 API Key）之后：发送失败不消费编辑状态。
    if edit and session.get("pending_edit"):
        if session["full_history"] and session["full_history"][-1].get("role") == "user":
            session["full_history"] = session["full_history"][:-1]
        if session["chat_display"] and session["chat_display"][-1].get("role") == "user":
            session["chat_display"] = session["chat_display"][:-1]
        _push_edit_history(project_id, session, session.pop("pending_edit", None))
    else:
        session.pop("pending_edit", None)

    # ── 准备上下文 ──
    ctx = _prepare_context(
        message,
        session["full_history"],
        session["midi_files"],
        settings,
        session["chat_display"],
    )

    # ── 执行工具循环（SSE 节流：每事件间隔 <25ms 的丢弃，因事件携带全量状态）──
    last_emit = 0.0
    last_chat_display = None    # 最近一次 chat 帧（节流吞帧时最后补发）
    last_files_payload: str | None = None
    last_download_url: str | None = None
    try:
        for gradio_tuple in _execute_tool_loop(
            ctx, session["undo_stack"], session["full_history"], message, project_id
        ):
            display, _, updated_files, undo_stack, full_history, download_path, _ = gradio_tuple
            session["undo_stack"] = undo_stack
            session["full_history"] = full_history
            session["midi_files"] = updated_files
            session["chat_display"] = display
            last_chat_display = display

            # download 事件不参与 25ms 节流：若携带新下载路径的帧被节流吞掉，
            # 前端会收不到下载链接提示（生成 MIDI 后"没有下载链接"）
            if download_path:
                url = download_url(download_path)
                if url != last_download_url:
                    last_download_url = url
                    yield _sse_event({"type": "download", "url": url})

            now = time.time()
            # 工具「执行中」占位帧跳过 25ms 节流：快工具瞬间完成时占位帧
            # 也要送达前端（用户要求调用开始即显示工具标签与 ⏳ 进度）
            last_content = display[-1].get("content", "") if display else ""
            if now - last_emit < 0.025 and "tool-pending" not in last_content:
                continue
            last_emit = now

            yield _sse_event({"type": "chat", "messages": display})
            # files 事件在文件列表或目录结构（dirs）任一变化时发送——
            # AI 用 create_folder 创建空文件夹（文件列表不变）也能即时刷新
            pub_files = _public_files(updated_files)
            dirs = _dirs_for(project_id)
            files_payload = json.dumps({"files": pub_files, "dirs": dirs}, ensure_ascii=False)
            if files_payload != last_files_payload:
                last_files_payload = files_payload
                yield _sse_event({"type": "files", "files": pub_files, "dirs": dirs})
    except GeneratorExit:
        raise
    except Exception:  # noqa: BLE001
        logger.exception("chat_stream 异常")
        yield _sse_event({"type": "error", "message": "对话处理发生内部错误"})

    # 节流吞帧兜底：工具轮/收尾帧连发间隔 <25ms 时最后几帧会被吞掉，
    # 前端 chatDone 用 lastMessages 重渲染将丢失工具块/最终内容——
    # 循环结束后补发最新一帧（全量状态，幂等）
    if last_chat_display is not None:
        yield _sse_event({"type": "chat", "messages": last_chat_display})

    yield _sse_event({"type": "done"})


def download_url(filepath: str | Path) -> str:
    """把服务器本地文件路径转成下载 URL。"""
    from urllib.parse import quote

    p = Path(filepath)
    rel = str(p.resolve().relative_to(config.PROJECT_ROOT.resolve()))
    return f"/api/files/download?path={quote(rel)}"


# ==================== 消息编辑（修改 / 撤回） ====================

# 修改历史栈限长：只保留最近 N 次编辑发送前的快照，防止长期对话内存膨胀
_EDIT_HISTORY_LIMIT = 10


def _locate_user_in_history(session: dict, index: int) -> int:
    """由显示列表 index 定位 full_history 中同一条 user 消息的下标。

    显示列表与 full_history 中的 user 消息按顺序一一对应（AI 消息/
    工具块在显示列表占位，两者下标不同）。先数出该消息是第几个
    user 消息，再定位 full_history 中同一条。
    """
    display = session["chat_display"]
    ordinal = sum(1 for m in display[: index + 1] if m.get("role") == "user")
    user_positions = [
        i for i, m in enumerate(session["full_history"]) if m.get("role") == "user"
    ]
    if ordinal - 1 >= len(user_positions):
        raise ValueError("消息位置无效")
    return user_positions[ordinal - 1]


def _push_edit_history(project_id: str, session: dict, pending: dict | None) -> None:
    """编辑发送成功后把「进入编辑前」完整状态快照压入修改历史栈并落盘。

    - 同一条用户消息只保留最近一次快照（新快照覆盖旧记录）。
    - 限长保留最近 _EDIT_HISTORY_LIMIT 条。
    - 落盘到 edit_history.json：服务重启后「撤回修改」仍可用。
    """
    if not pending:
        return
    history = session.setdefault("edit_history", [])
    history = [h for h in history if h.get("index") != pending.get("index")]
    history.append(pending)
    session["edit_history"] = history[-_EDIT_HISTORY_LIMIT:]
    project_manager.save_edit_history(project_id, session["edit_history"])


def _find_edit_history(session: dict, index: int) -> dict | None:
    """取该条用户消息最近一次编辑发送前的快照（倒序找同 index 记录）。"""
    for h in reversed(session.get("edit_history", [])):
        if h.get("index") == index:
            return h
    return None


def edit_message(project_id: str, index: int) -> dict:
    """修改模式：截断第 index 条用户消息之后的所有对话。

    - 截断仅作用于内存中的会话（不落盘），进入编辑前的完整状态快照
      存入 pending_edit 供撤回恢复；未发送/未撤回时磁盘保持原状，中途
      关闭窗口 = 编辑自然放弃，重开仍是完整对话。
    - 返回截断后的显示列表与被编辑消息原文，供前端渲染并填入输入框。
    """
    session = _get_session(project_id)
    display = session["chat_display"]
    if index < 0 or index >= len(display) or display[index].get("role") != "user":
        raise ValueError("消息位置无效")

    hist_idx = _locate_user_in_history(session, index)

    session["pending_edit"] = {
        "index": index,
        "text": display[index].get("content", ""),
        "full_history": copy.deepcopy(session["full_history"]),
        "chat_display": copy.deepcopy(display),
    }
    session["full_history"] = session["full_history"][:hist_idx + 1]
    session["chat_display"] = display[:index + 1]
    return {"messages": session["chat_display"], "text": session["pending_edit"]["text"]}


def recall_edit(project_id: str) -> dict:
    """撤回修改：恢复进入编辑模式前的完整对话并落盘。

    无进行中的修改时幂等返回当前列表。
    """
    session = _get_session(project_id)
    pending = session.pop("pending_edit", None)
    if not pending:
        return {"messages": session["chat_display"]}
    session["full_history"] = pending["full_history"]
    session["chat_display"] = pending["chat_display"]
    _persist_project_state(project_id, session["full_history"], session["midi_files"])
    return {"messages": session["chat_display"]}


def edit_info(project_id: str, index: int) -> dict:
    """查询该条消息是否有可撤回的修改历史（前端据此启用「撤回修改」选项）。"""
    session = _get_session(project_id)
    display = session["chat_display"]
    if index < 0 or index >= len(display) or display[index].get("role") != "user":
        raise ValueError("消息位置无效")
    return {"has_edit_history": _find_edit_history(session, index) is not None}


def undo_edit(project_id: str, index: int) -> dict:
    """撤回修改：把所有内容退回到这条消息发送之前，再进入修改模式。

    - 从修改历史栈取该消息最近一次编辑发送前的完整快照：该消息恢复
      原文，其后的内容恢复当时的原始版本（含用户后来做的修改一并还原）。
    - 执行前的完整状态存入 pending_edit——编辑条上的「↩ 撤回」即可
      撤回这次撤回（恢复到执行前状态）。
    - 不落盘（与 edit_message 一致）：发送/撤回时才落盘。
    """
    session = _get_session(project_id)
    display = session["chat_display"]
    if index < 0 or index >= len(display) or display[index].get("role") != "user":
        raise ValueError("消息位置无效")
    snap = _find_edit_history(session, index)
    if snap is None:
        raise ValueError("该消息没有可撤回的修改")

    # 先保存执行前状态（供「↩ 撤回」恢复），再退回历史版本
    session["pending_edit"] = {
        "index": index,
        "text": display[index].get("content", ""),
        "full_history": copy.deepcopy(session["full_history"]),
        "chat_display": copy.deepcopy(display),
    }
    session["full_history"] = copy.deepcopy(snap["full_history"])
    session["chat_display"] = copy.deepcopy(snap["chat_display"])

    # 退回后截断到该消息（含），进入修改模式
    hist_idx = _locate_user_in_history(session, index)
    session["full_history"] = session["full_history"][:hist_idx + 1]
    session["chat_display"] = session["chat_display"][:index + 1]
    return {"messages": session["chat_display"], "text": snap["text"]}


# ==================== 项目生命周期 ====================

def _restart_mcp_for_project(project_id: str) -> None:
    """关闭现有 MCP 子进程，下次 _ensure_mcp_process() 将以新项目目录重启。"""
    global _current_project_id
    _current_project_id = project_id
    _close_mcp_process()


def _rebuild_display_from_history(all_messages: list[dict]) -> list[dict]:
    """从完整 API 历史（含 tool 调用）重建聊天显示列表。"""
    from chat_pipeline import _format_display_message

    # 建立 tool_call_id -> 结果文本 映射
    tool_results: dict[str, str] = {}
    for m in all_messages:
        if m.get("role") == "tool":
            tcid = m.get("tool_call_id", "")
            if tcid:
                tool_results[tcid] = m.get("content", "") or ""

    display: list[dict] = []
    for m in all_messages:
        role = m.get("role", "")
        content = m.get("content")
        tool_calls = m.get("tool_calls") or []

        if role == "user":
            if content:
                display.append({"role": "user", "content": content})
        elif role == "assistant":
            # 持久化的推理过程（reasoning_content）重建为可折叠思考过程
            reasoning = m.get("reasoning_content") or ""
            if content and content.strip():
                display.append({
                    "role": "assistant",
                    "content": _format_display_message(reasoning, content),
                })
            elif reasoning and reasoning.strip():
                display.append({
                    "role": "assistant",
                    "content": _format_display_message(reasoning, ""),
                })
            # 把每个工具调用重新格式化为可折叠标签
            for tc in tool_calls:
                tc_id = tc.get("id", "")
                func = tc.get("function", {})
                tc_name = func.get("name", "")
                args_raw = func.get("arguments", "")
                try:
                    tc_args = (
                        json.loads(args_raw)
                        if isinstance(args_raw, str)
                        else (args_raw or {})
                    )
                except json.JSONDecodeError:
                    tc_args = {}
                result_text = tool_results.get(tc_id, "（未找到工具结果）")
                entry = _format_single_tool_entry(tc_name, tc_args, result_text)
                display.append({"role": "assistant", "content": entry})
        # role == "tool" 跳过（已被上面的 tool_calls 消费）

    return display


def open_project(project_id: str) -> dict:
    """打开项目：加载历史与文件，重启 MCP，返回前端渲染所需数据。"""
    global _current_project_id
    _current_project_id = project_id
    _close_mcp_process()

    session = _get_session(project_id)
    # 绑定工作区时：完全同步工作区->projects，并按工作区扫描刷新文件列表
    if project_manager.get_workspace_dir(project_id):
        try:
            session["midi_files"] = project_manager.sync_workspace_to_projects(
                project_id, session["midi_files"]
            )
        except Exception:  # noqa: BLE001
            logger.exception("打开项目时工作区同步失败")
    meta = project_manager.load_project(project_id)
    ws = project_manager.get_workspace_dir(project_id)

    return {
        "meta": meta,
        "display_messages": session["chat_display"],
        "midi_files": _public_files(session["midi_files"]),
        "dirs": _dirs_for(project_id),
        "workspace": {
            "bound": bool(ws),
            "path": ws or "",
        },
        "draft": project_manager.load_draft(project_id),
        "settings": _settings_summary(),
    }


def close_project(project_id: str | None) -> list[dict]:
    """保存当前状态并返回项目列表（供前端回到档案库）。"""
    global _current_project_id
    if project_id:
        session = _get_session(project_id)
        # 绑定工作区时，关闭前同步一次，保证 projects 镜像最新
        if project_manager.get_workspace_dir(project_id):
            try:
                session["midi_files"] = project_manager.sync_workspace_to_projects(
                    project_id, session["midi_files"] or []
                )
            except Exception:  # noqa: BLE001
                logger.exception("关闭项目时工作区同步失败")
        _persist_project_state(project_id, session["full_history"], session["midi_files"])
    _current_project_id = None
    _close_mcp_process()
    return refresh_project_list()


def refresh_project_list() -> list[dict]:
    """返回项目列表（前端卡片渲染用）。"""
    return project_manager.list_projects()


def _refresh_project_list() -> tuple[list[tuple[str, str]], list[str]]:
    """兼容旧接口：返回 (下拉选项列表, 项目ID列表)。"""
    projects = project_manager.list_projects()
    choices = []
    ids = []
    for p in projects:
        name = p.get("name", "未命名")
        msg_count = p.get("message_count", 0)
        label = f"{name}  ({msg_count} 条消息)" if msg_count else name
        choices.append((label, p["id"]))
        ids.append(p["id"])
    return choices, ids


def _resolve_project_id(selected_project: str, project_ids: list[str]) -> str | None:
    """兼容旧接口：根据选中值解析稳定的项目 ID。"""
    if not selected_project or not project_ids:
        return None
    if selected_project in project_ids:
        return selected_project
    return None


def create_project(name: str) -> dict:
    """创建新项目并打开，返回 open_project 载荷。"""
    if not name.strip():
        from datetime import datetime

        name = f"新项目 {datetime.now().strftime('%Y-%m-%d %H:%M')}"
    meta = project_manager.create_project(name.strip())
    return open_project(meta["id"])


def delete_project(project_id: str) -> None:
    """删除项目并清理会话与 MCP。"""
    global _current_project_id
    if project_id == _current_project_id:
        _current_project_id = None
        _close_mcp_process()
    project_manager.delete_project(project_id)
    _drop_session(project_id)


def rename_project(project_id: str, new_name: str) -> None:
    """重命名项目。"""
    project_manager.rename_project(project_id, new_name.strip())


def copy_project(project_id: str, new_name: str) -> dict:
    """复制项目，返回新项目 meta。"""
    src_meta = project_manager.load_project(project_id)
    name = new_name or f"{src_meta.get('name', '未命名')} (副本)"
    return project_manager.copy_project(project_id, name)


# ==================== 工作区绑定 ====================

# IFileOpenDialog (Vista 风格现代对话框) 的 C# COM 互操作源码，由 PowerShell Add-Type 编译。
_PICK_FOLDER_CS = """
using System;
using System.IO;
using System.Runtime.InteropServices;

public static class NativeFolderPicker
{
    [ComImport, ClassInterface(ClassInterfaceType.None), TypeLibType(TypeLibTypeFlags.FCanCreate), Guid("DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7")]
    internal class FileOpenDialogRCW { }

    [ComImport, Guid("42F85136-DB7E-439C-85F1-E4075D135FC8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    internal interface IFileDialog
    {
        [PreserveSig] uint Show(IntPtr hwndOwner);
        uint SetFileTypes(uint cFileTypes, IntPtr rgFilterSpec);
        uint SetFileTypeIndex(uint iFileType);
        uint GetFileTypeIndex(out uint piFileType);
        uint Advise(IntPtr pfde, out uint pdwCookie);
        uint Unadvise(uint dwCookie);
        uint SetOptions(uint fos);
        uint GetOptions(out uint fos);
        void SetDefaultFolder(IShellItem psi);
        uint SetFolder(IShellItem psi);
        uint GetFolder(out IShellItem ppsi);
        uint GetCurrentSelection(out IShellItem ppsi);
        uint SetFileName(string pszName);
        uint GetFileName(out string pszName);
        uint SetTitle(string pszTitle);
        uint SetOkButtonLabel(string pszText);
        uint SetFileNameLabel(string pszLabel);
        uint GetResult(out IShellItem ppsi);
        uint AddPlace(IShellItem psi, uint fdap);
        uint SetDefaultExtension(string pszDefaultExtension);
        uint Close(uint hr);
        uint SetClientGuid(ref Guid guid);
        uint ClearClientData();
        uint SetFilter(IntPtr pFilter);
    }

    [ComImport, Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    internal interface IShellItem
    {
        uint BindToHandler(IntPtr pbc, ref Guid rbhid, ref Guid riid, out IntPtr ppvOut);
        uint GetParent(out IShellItem ppsi);
        uint GetDisplayName(uint sigdnName, out IntPtr ppszName);
        uint GetAttributes(uint sfgaoMask, out uint psfgaoAttribs);
        uint Compare(IShellItem psi, uint hint, out int piOrder);
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    internal static extern int SHCreateItemFromParsingName(string pszPath, IntPtr pbc, ref Guid riid, out IShellItem ppv);

    private const uint FOS_PICKFOLDERS = 0x00000020;
    private const uint FOS_FORCEFILESYSTEM = 0x00000040;
    private const uint SIGDN_FILESYSPATH = 0x80058000;
    private static readonly Guid IID_IShellItem = new Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE");

    public static string PickFolder(string title, string initialDirectory)
    {
        IFileDialog dialog = (IFileDialog)(new FileOpenDialogRCW());
        uint options = FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM;
        dialog.GetOptions(out options);
        options |= FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM;
        dialog.SetOptions(options);
        if (!string.IsNullOrEmpty(title))
            dialog.SetTitle(title);
        if (!string.IsNullOrEmpty(initialDirectory) && Directory.Exists(initialDirectory))
        {
            Guid iid = IID_IShellItem;
            IShellItem dirItem;
            if (SHCreateItemFromParsingName(initialDirectory, IntPtr.Zero, ref iid, out dirItem) == 0)
                dialog.SetFolder(dirItem);
        }
        if (dialog.Show(IntPtr.Zero) != 0)
            return null;
        IShellItem shellItem;
        if (dialog.GetResult(out shellItem) != 0)
            return null;
        IntPtr pszString;
        if (shellItem.GetDisplayName(SIGDN_FILESYSPATH, out pszString) != 0 || pszString == IntPtr.Zero)
            return null;
        try
        {
            return Marshal.PtrToStringUni(pszString);
        }
        finally
        {
            Marshal.FreeCoTaskMem(pszString);
        }
    }
}
"""


def _pick_folder_dialog() -> str:
    """弹出 Windows Vista 风格（IFileOpenDialog）原生文件夹选择对话框，返回所选路径；取消返回空串。"""
    ps_script = (
        "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "
        "$src = @'\n"
        + _PICK_FOLDER_CS
        + "\n'@\n"
        "Add-Type -TypeDefinition $src\n"
        "[NativeFolderPicker]::PickFolder('请选择工作区文件夹', $null)"
    )
    try:
        result = subprocess.run(
            ["powershell", "-NoProfile", "-STA", "-Command", ps_script],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=180,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )
    except (OSError, subprocess.SubprocessError, subprocess.TimeoutExpired):
        logger.exception("打开文件夹选择对话框失败")
        return ""
    path = result.stdout.strip()
    return path if path and os.path.isdir(path) else ""


def bind_workspace(project_id: str, path: str) -> dict:
    """绑定工作区目录：初始化同步 projects->工作区，刷新文件列表，重启 MCP。"""
    result = project_manager.bind_workspace(project_id, path)
    _close_mcp_process()
    session = _get_session(project_id)
    session["midi_files"] = result["midi_files"]
    _persist_project_state(project_id, session["full_history"], session["midi_files"])
    return {
        "midi_files": _public_files(session["midi_files"]),
        "renamed": result["renamed"],
        "path": project_manager.get_workspace_dir(project_id) or "",
        "dirs": _dirs_for(project_id),
    }


def unbind_workspace(project_id: str) -> dict:
    """解绑工作区：不动工作区文件，读取改回 projects，重启 MCP。"""
    session = _get_session(project_id)
    session["midi_files"] = project_manager.unbind_workspace(project_id)
    _close_mcp_process()
    _persist_project_state(project_id, session["full_history"], session["midi_files"])
    return {"midi_files": _public_files(session["midi_files"]), "dirs": _dirs_for(project_id)}


def refresh_workspace(project_id: str) -> dict:
    """手动刷新：完全同步工作区->projects，刷新文件列表。"""
    session = _get_session(project_id)
    session["midi_files"] = project_manager.sync_workspace_to_projects(
        project_id, session["midi_files"] or []
    )
    _persist_project_state(project_id, session["full_history"], session["midi_files"])
    return {"midi_files": _public_files(session["midi_files"]), "dirs": _dirs_for(project_id)}


def open_workspace(project_id: str) -> dict:
    """在系统文件管理器中打开工作区（绑定=工作区目录；未绑定=项目默认 midi 目录）。

    目录不存在则先创建。返回 {"ok": True, "path": ...}；打开失败抛 OSError。
    """
    path = project_manager.get_midi_base_dir(project_id)
    path.mkdir(parents=True, exist_ok=True)
    os.startfile(str(path))  # noqa: S606 - Windows 专属，打开目录由系统决定关联程序
    return {"ok": True, "path": str(path)}


def clear_chat(project_id: str) -> dict:
    """清空对话并保留 MIDI 文件元数据。"""
    session = _get_session(project_id)
    session["full_history"] = []
    session["chat_display"] = []
    session["undo_stack"] = []
    session.pop("pending_edit", None)
    session["edit_history"] = []
    project_manager.save_edit_history(project_id, [])
    project_manager.save_history(project_id, [], session["midi_files"] or [])
    return {"messages": []}
