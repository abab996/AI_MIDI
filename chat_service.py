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

# 当前打开的项目 ID（None 表示在项目浏览器）
_current_project_id: str | None = None


# ==================== 会话状态 ====================

# project_id -> {"full_history": [...], "midi_files": [...],
#                "undo_stack": [...], "chat_display": [...]}
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
    global _mcp_process, _mcp_initialized

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


def _mcp_recv(proc: subprocess.Popen, timeout: float = config.MCP_RESPONSE_TIMEOUT) -> dict | None:
    """从 MCP 进程读取一条 JSON-RPC 响应（带超时）。"""
    import queue

    q: queue.Queue = queue.Queue()

    def _read():
        try:
            line = proc.stdout.readline()
            q.put(line)
        except (OSError, ValueError):
            q.put(None)

    t = threading.Thread(target=_read, daemon=True)
    t.start()
    t.join(timeout)

    if t.is_alive():
        # 超时：让孤儿线程继续运行，下次调用可能会读到这条响应
        # （_mcp_call_tool 会按 id 匹配，未匹配的会被忽略）
        return None

    line = q.get()
    if not line:
        return None
    try:
        return json.loads(line.strip())
    except json.JSONDecodeError:
        return None


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
        response = _mcp_recv(proc)
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
    """获取 MCP Server 的工具列表，转换为 OpenAI function definitions。"""
    proc = _ensure_mcp_process()
    if not proc:
        return []

    global _mcp_request_id
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
        response = _mcp_recv(proc)
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
            return openai_tools
    return []


def _close_mcp_process() -> None:
    """关闭 MCP 子进程。"""
    global _mcp_process
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

def _on_upload(files, current_list, undo_stack, project_id, full_history):
    """上传文件，解析为 note_table 并追加到列表。文件复制到项目 midi 目录。

    兼容旧接口签名；files 为带 .name 路径的文件对象列表（server 端已转临时路径）。
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

    for f in files:
        src_path = f.name if hasattr(f, "name") else str(f)
        basename = os.path.basename(src_path)
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
        # 镜像双写（绑定工作区时同步到 projects/<id>/midi）
        if mirror_dir is not None:
            try:
                mp = mirror_dir / dest_path.relative_to(dest_dir)
                mp.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(dest_path, mp)
            except OSError:
                logger.exception("镜像同步上传文件失败: %s", dest_path)
        # 验证文件是合法 MIDI：mido 解析失败的文件直接拒绝
        try:
            mido.MidiFile(str(dest_path))
        except (OSError, ValueError, EOFError):
            logger.warning("文件不是合法 MIDI,已删除: %s", dest_path)
            try:
                dest_path.unlink()
            except OSError:
                pass
            continue
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
    new_undo_stack = undo_stack + [copy.deepcopy(current_list)] if new_files else undo_stack
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, _get_choices(updated)


def _on_delete(current_list, selected, undo_stack, project_id, full_history):
    """删除勾选的文件。"""
    current_list = current_list or []
    undo_stack = undo_stack or []
    selected_file_list = _selected_files(current_list, selected)
    if not current_list or not selected_file_list:
        return current_list, undo_stack, _get_choices(current_list)
    selected_values = {_file_choice_value(file_info) for file_info in selected_file_list}
    updated = [f for f in current_list if _file_choice_value(f) not in selected_values]
    new_undo_stack = undo_stack + [copy.deepcopy(current_list)]
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, _get_choices(updated)


def _on_undo(undo_stack, current_files, project_id, full_history):
    """撤销最近一次操作。"""
    current_files = current_files or []
    undo_stack = undo_stack or []
    if not undo_stack:
        return current_files, [], _get_choices(current_files)
    restored = undo_stack.pop()
    _persist_project_state(project_id, full_history, restored)
    return restored, undo_stack, _get_choices(restored)


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


def chat_stream(project_id: str, message: str):
    """多轮对话 SSE 事件流（生成器）。

    事件类型：
    - {"type": "chat", "messages": [...]}      完整对话显示列表
    - {"type": "files", "files": [...]}        文件列表变更
    - {"type": "download", "url": "..."}       新生成的 MIDI 可下载
    - {"type": "error", "message": "..."}      致命错误
    - {"type": "done"}                         结束
    """
    global _current_project_id

    # ── 早期返回：空消息 ──
    if not message.strip():
        yield _sse_event({"type": "error", "message": "消息不能为空"})
        return

    # ── 早期返回：未配置 API Key ──
    settings = _load_settings()
    if not settings["api_key"]:
        yield _sse_event({
            "type": "error",
            "message": "⚠ 请先在设置页填写并保存 API Key。",
        })
        return

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
    session["undo_stack"].append(copy.deepcopy(session["midi_files"]))

    # ── 消息已发送，草稿清除 ──
    project_manager.save_draft(project_id, "")

    # Lazy import to avoid circular dependency at module level
    from chat_pipeline import _execute_tool_loop, _prepare_context  # noqa: E402

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
    try:
        for gradio_tuple in _execute_tool_loop(
            ctx, session["undo_stack"], session["full_history"], message
        ):
            display, _, updated_files, undo_stack, full_history, download_path, _ = gradio_tuple
            session["undo_stack"] = undo_stack
            session["full_history"] = full_history
            session["midi_files"] = updated_files
            session["chat_display"] = display

            now = time.time()
            if now - last_emit < 0.025:
                continue
            last_emit = now

            yield _sse_event({"type": "chat", "messages": display})
            yield _sse_event({"type": "files", "files": _public_files(updated_files)})
            if download_path:
                yield _sse_event({"type": "download", "url": download_url(download_path)})
    except GeneratorExit:
        raise
    except Exception:  # noqa: BLE001
        logger.exception("chat_stream 异常")
        yield _sse_event({"type": "error", "message": "对话处理发生内部错误"})

    yield _sse_event({"type": "done"})


def download_url(filepath: str | Path) -> str:
    """把服务器本地文件路径转成下载 URL。"""
    from urllib.parse import quote

    p = Path(filepath)
    rel = str(p.resolve().relative_to(config.PROJECT_ROOT.resolve()))
    return f"/api/files/download?path={quote(rel)}"


# ==================== 项目生命周期 ====================

def _restart_mcp_for_project(project_id: str) -> None:
    """关闭现有 MCP 子进程，下次 _ensure_mcp_process() 将以新项目目录重启。"""
    global _current_project_id
    _current_project_id = project_id
    _close_mcp_process()


def _rebuild_display_from_history(all_messages: list[dict]) -> list[dict]:
    """从完整 API 历史（含 tool 调用）重建聊天显示列表。"""
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
            # 工具调用前的说明文字（若存在且非空）
            if content and content.strip():
                display.append({"role": "assistant", "content": content})
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
    }


def unbind_workspace(project_id: str) -> dict:
    """解绑工作区：不动工作区文件，读取改回 projects，重启 MCP。"""
    session = _get_session(project_id)
    session["midi_files"] = project_manager.unbind_workspace(project_id)
    _close_mcp_process()
    _persist_project_state(project_id, session["full_history"], session["midi_files"])
    return {"midi_files": _public_files(session["midi_files"])}


def refresh_workspace(project_id: str) -> dict:
    """手动刷新：完全同步工作区->projects，刷新文件列表。"""
    session = _get_session(project_id)
    session["midi_files"] = project_manager.sync_workspace_to_projects(
        project_id, session["midi_files"] or []
    )
    _persist_project_state(project_id, session["full_history"], session["midi_files"])
    return {"midi_files": _public_files(session["midi_files"])}


def clear_chat(project_id: str) -> dict:
    """清空对话并保留 MIDI 文件元数据。"""
    session = _get_session(project_id)
    session["full_history"] = []
    session["chat_display"] = []
    session["undo_stack"] = []
    project_manager.save_history(project_id, [], session["midi_files"] or [])
    return {"messages": []}
