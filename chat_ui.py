"""多轮对话 Web UI。

基于 Gradio 实现，支持多轮对话、AI 通过 MCP 工具操作 MIDI 文件、
流式输出、撤销等功能。
"""
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

import gradio as gr
import mido

import config
import get
import out
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
    prompt += "- **`delete_midi`**：删除 MIDI 文件。参数：`filename`\n\n"

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
    try:
        base_url = config.validate_base_url(raw_base_url)
    except ValueError:
        logger.warning("base_url 验证失败，使用默认值: %s", config.BASE_URL)
        base_url = config.BASE_URL
    return {
        "api_key": settings.get("api_key") or config.get_api_key(),
        "base_url": base_url,
        "model": settings.get("model") or config.MODEL,
        "max_tokens": settings.get("max_tokens"),
        "max_completion_tokens": settings.get("max_completion_tokens"),
        "reasoning_effort": settings.get("reasoning_effort"),
        "thinking_enabled": settings.get("thinking_enabled", True),
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
                env["AI_MIDI_OUTPUT_DIR"] = str(project_manager.midi_dir(_current_project_id))
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
            time.sleep(0.5)
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


def _mcp_recv(proc: subprocess.Popen, timeout: float = 30.0) -> dict | None:
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
    deadline = time.time() + 30
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

    deadline = time.time() + 15
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


# ==================== MIDI 操作（兼容旧接口） ====================

def _on_upload(files, current_list, undo_stack, project_id, full_history):
    """上传文件，解析为 note_table 并追加到列表。文件复制到项目 midi 目录。"""
    current_list = current_list or []
    undo_stack = undo_stack or []
    if not files:
        return current_list, undo_stack, gr.update(choices=_get_choices(current_list))
    new_files = []
    # 确定目标目录：项目目录或临时目录
    if project_id:
        dest_dir = project_manager.midi_dir(project_id)
        dest_dir.mkdir(parents=True, exist_ok=True)
    else:
        dest_dir = config.OUTPUT_DIR
        dest_dir.mkdir(exist_ok=True)

    for f in files:
        src_path = f.name if hasattr(f, "name") else str(f)
        basename = os.path.basename(src_path)
        # 仅接受 .mid/.midi 扩展名（防止用户绕过前端 file_types 限制）
        if not basename.lower().endswith((".mid", ".midi")):
            logger.warning("跳过非 MIDI 文件: %s", basename)
            continue
        # 复制到项目/输出目录
        dest_path = _unique_dest_path(dest_dir, basename)
        try:
            shutil.copy2(src_path, dest_path)
        except OSError:
            logger.exception("复制 MIDI 文件失败: %s -> %s", src_path, dest_path)
            continue
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
        new_files.append({
            "name": dest_path.name,
            "path": str(dest_path),
            "size": dest_path.stat().st_size if dest_path.exists() else 0,
            "note_table": note_table_str,
        })
    updated = current_list + new_files
    new_undo_stack = undo_stack + [copy.deepcopy(current_list)] if new_files else undo_stack
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, gr.update(choices=_get_choices(updated), value=[])


def _on_delete(current_list, selected, undo_stack, project_id, full_history):
    """删除勾选的文件。"""
    current_list = current_list or []
    undo_stack = undo_stack or []
    selected_file_list = _selected_files(current_list, selected)
    if not current_list or not selected_file_list:
        return current_list, undo_stack, gr.update(choices=_get_choices(current_list))
    selected_values = {_file_choice_value(file_info) for file_info in selected_file_list}
    updated = [f for f in current_list if _file_choice_value(f) not in selected_values]
    new_undo_stack = undo_stack + [copy.deepcopy(current_list)]
    _persist_project_state(project_id, full_history, updated)
    return updated, new_undo_stack, gr.update(choices=_get_choices(updated), value=[])


def _on_download(current_list, selected):
    """下载勾选的文件。未勾选则下载全部。"""
    if not current_list:
        return None

    if selected:
        files = _selected_files(current_list, selected)
    else:
        files = list(current_list)

    if not files:
        return None

    if len(files) == 1 and os.path.isfile(files[0]["path"]):
        return files[0]["path"]

    zip_path = os.path.join(tempfile.gettempdir(), "AI_MIDI_files.zip")
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


def _on_undo(undo_stack, current_files, project_id, full_history):
    """撤销最近一次操作。"""
    current_files = current_files or []
    undo_stack = undo_stack or []
    if not undo_stack:
        return current_files, [], gr.update(choices=_get_choices(current_files))
    restored = undo_stack.pop()
    _persist_project_state(project_id, full_history, restored)
    return restored, undo_stack, gr.update(choices=_get_choices(restored), value=[])


def _get_choices(files: list[dict]) -> list[tuple[str, str]]:
    """生成 CheckboxGroup 选项列表。"""
    return [(_file_label(file_info), _file_choice_value(file_info)) for file_info in files]


# ==================== 上下文压缩 ====================

# 压缩阈值（估算 tokens ≈ chars / 3）
_MAX_CONTEXT_CHARS = config.MAX_CONTEXT_CHARS
_COMPACT_KEEP_RECENT = config.COMPACT_KEEP_RECENT_MESSAGES


def _compact_full_history(full_history: list[dict]) -> list[dict]:
    """压缩完整历史，保留最近几轮对话，旧内容替换为摘要。

    策略：遍历 full_history，找出 user 消息作为分界点。
    保留最后 _COMPACT_KEEP_RECENT 个 user 之后的全部内容，
    前面的内容替换为一条 compact_summary 消息。
    """
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
    """为旧消息生成摘要。

    简单策略：提取关键信息，不调用 AI（避免额外 API 调用）。
    改为保留一条简短的用户消息作为上下文提示。
    """
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
            # （AI 可能用 notes 或 note_table 两种参数名，且原始输入经过
            #  _normalize_note_data 转换后格式会变；统一从文件读回最准确）
            try:
                note_data = get.get_note(str(filepath), save_to_file=False)
            except Exception:
                note_data = func_args.get("notes", "") or func_args.get("note_table", "")
            file_info = {
                "name": filepath.name,
                "path": str(filepath),
                "size": filepath.stat().st_size,
                "note_table": note_data,
            }
            updated_files = [f for f in updated_files if _file_choice_value(f) != str(filepath)]
            updated_files.append(file_info)

    # 如果删除了文件，更新文件列表
    elif func_name == "delete_midi":
        filename = func_args.get("filename", "")
        updated_files = [f for f in updated_files if f["name"] != filename]

    # 如果解析了文件，更新 note_table
    elif func_name == "parse_midi":
        filename = func_args.get("filename", "")
        for f in updated_files:
            if f["name"] == filename:
                f["note_table"] = result_text
                break

    return result_text, updated_files


def _format_tool_log(tool_log: list[str]) -> str:
    """将工具调用日志格式化为可折叠的 HTML details 标签。
    每个工具调用独立为一个标签，方便用户逐个展开查看。
    """
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
    """格式化单个工具调用为可折叠 HTML details 标签。

    Args:
        tc_args: 工具参数。支持 dict（推荐，可做长参数截断）或已格式化的
            字符串（向后兼容，直接原样显示）。
        result_text: 工具执行结果文本。

    对超长参数（如 create_midi 的 notes）进行截断，summary 行只保留短摘要，
    完整参数单独放在 <details> 展开区，用户点开才能看到全部内容。
    """
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


def _make_tool_result_message(tool_call_id: str, result: str) -> dict:
    """构造 tool 角色的消息（OpenAI API 格式）。"""
    return {
        "role": "tool",
        "tool_call_id": tool_call_id,
        "content": result,
    }


def send_message(message, history, midi_files, undo_stack, full_history):
    """发送消息，调用 AI 并通过 MCP 工具执行操作。

    支持多轮工具调用：AI 可能连续调用多个工具，
    每次调用后把结果回传给 AI，直到 AI 给出最终回复。

    Args:
        full_history: 完整 API 消息历史（含 tool 调用），用于构建 API 上下文。
    """
    # ── 早期返回：空消息 ──
    if not message.strip():
        yield history, "", midi_files, undo_stack, full_history, None, gr.update()
        return

    # ── 早期返回：未配置 API Key ──
    settings = _load_settings()
    if not settings["api_key"]:
        yield history + [
            {"role": "user", "content": message},
            {"role": "assistant", "content": "⚠ 请先在主界面设置页填写并保存 API Key。"},
        ], "", midi_files, undo_stack, full_history, None, gr.update()
        return

    # ── 快照（用于撤销）──
    snapshot = copy.deepcopy(midi_files)
    new_undo_stack = undo_stack + [snapshot]

    # Lazy import to avoid circular dependency at module level
    from chat_pipeline import _prepare_context, _execute_tool_loop  # noqa: E402

    # ── 准备上下文 ──
    ctx = _prepare_context(message, full_history, midi_files, settings, history)

    # ── 执行工具循环（所有后续 yield 均来自这里）──
    yield from _execute_tool_loop(ctx, new_undo_stack, full_history, message)


# ==================== 项目生命周期 ====================

def _restart_mcp_for_project(project_id: str) -> None:
    """关闭现有 MCP 子进程，下次 _ensure_mcp_process() 将以新项目目录重启。"""
    global _current_project_id
    _current_project_id = project_id
    _close_mcp_process()


def _rebuild_display_from_history(all_messages: list[dict]) -> list[dict]:
    """从完整 API 历史（含 tool 调用）重建 Chatbot 显示列表。

    full_history 中保存的是原始 API 消息：带 tool_calls 的 assistant 消息
    和 tool 角色的结果消息。这些在实时对话时会被 _format_single_tool_entry
    渲染为可折叠的 <details> 标签，但该渲染结果只存在于临时的 chat_display，
    并未单独持久化。重新打开项目时需要在这里重新格式化，否则工具调用标签会消失。

    策略：
    - user 消息原样保留
    - assistant 纯文本回复原样保留
    - assistant 带 tool_calls：先输出其 content（工具调用前的说明文字），
      再把每个 tool_call 与配套的 tool 结果消息组合为 <details> 标签
    - tool 角色消息已被上面的 tool_calls 消费，跳过
    """
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


def _open_project(project_id: str) -> tuple:
    """加载项目历史和 MIDI 文件，重启 MCP，返回 Gradio 更新。"""
    global _current_project_id
    _current_project_id = project_id
    _close_mcp_process()

    all_messages, midi_files = project_manager.load_history(project_id)
    meta = project_manager.load_project(project_id)
    choices = _get_choices(midi_files)

    # 从完整 API 历史重建 Chatbot 显示列表（含工具调用折叠标签）
    display_messages = _rebuild_display_from_history(all_messages)

    return (
        gr.update(visible=False),                   # 隐藏项目浏览器
        gr.update(visible=True),                    # 显示聊天面板
        project_id,                                  # project_id_state
        display_messages,                            # chatbot
        midi_files,                                   # midi_files_state
        [],                                           # undo_stack
        all_messages,                                  # full_history_state
        gr.update(choices=choices, value=[]),         # file_checkboxes
        f"### 当前项目: {meta.get('name', '未命名')}",  # project_name_display
    )


def _close_project(project_id: str | None, full_history, midi_files) -> tuple:
    """保存当前状态，返回项目浏览器并刷新项目列表。"""
    global _current_project_id
    _persist_project_state(project_id, full_history, midi_files or [])
    _current_project_id = None
    _close_mcp_process()
    choices, ids = _refresh_project_list()
    return (
        gr.update(visible=True),       # 显示项目浏览器
        gr.update(visible=False),      # 隐藏聊天面板
        None,                           # project_id_state
        gr.update(choices=choices, value=None),     # project_radio
        ids,                            # project_list_ids
    )


def _on_new_project(name: str) -> tuple:
    """创建新项目并切换到聊天面板。"""
    if not name.strip():
        from datetime import datetime
        name = f"新项目 {datetime.now().strftime('%Y-%m-%d %H:%M')}"
    meta = project_manager.create_project(name.strip())
    pid = meta["id"]
    return _open_project(pid)


def _refresh_project_list() -> tuple[list[tuple[str, str]], list[str]]:
    """从索引加载项目列表，返回 (下拉选项列表, 项目ID列表)。"""
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
    """根据 Radio 选中的稳定项目 ID 解析项目。"""
    if not selected_project or not project_ids:
        return None
    if selected_project in project_ids:
        return selected_project
    return None


def _on_open_project(selected_project: str, project_ids: list[str]) -> tuple:
    """打开选中的项目。"""
    pid = _resolve_project_id(selected_project, project_ids)
    if not pid:
        return tuple([gr.update()] * 9)
    return _open_project(pid)


def _on_delete_project(selected_project: str, project_ids: list[str]) -> tuple:
    """删除选中的项目。"""
    pid = _resolve_project_id(selected_project, project_ids)
    if not pid:
        return gr.update(), []
    project_manager.delete_project(pid)
    choices, ids = _refresh_project_list()
    return gr.update(choices=choices, value=None), ids


def _on_rename_show() -> tuple:
    """显示重命名输入框。"""
    return gr.update(visible=True), gr.update(visible=True)


def _on_confirm_rename(new_name: str, selected_project: str, project_ids: list[str]) -> tuple:
    """确认重命名。"""
    if not new_name.strip():
        return gr.update(), [], gr.update(visible=False), gr.update(value="", visible=False)
    pid = _resolve_project_id(selected_project, project_ids)
    if not pid:
        return gr.update(), [], gr.update(visible=False), gr.update(value="", visible=False)
    project_manager.rename_project(pid, new_name.strip())
    choices, ids = _refresh_project_list()
    return gr.update(choices=choices, value=pid), ids, gr.update(visible=False), gr.update(value="", visible=False)


def _on_copy_project(selected_project: str, project_ids: list[str]) -> tuple:
    """复制选中的项目。"""
    pid = _resolve_project_id(selected_project, project_ids)
    if not pid:
        return gr.update(), []
    src_meta = project_manager.load_project(pid)
    new_name = f"{src_meta.get('name', '未命名')} (副本)"
    copied = project_manager.copy_project(pid, new_name)
    choices, ids = _refresh_project_list()
    return gr.update(choices=choices, value=copied.get("id")), ids


def _on_search(query: str) -> tuple:
    """搜索项目对话内容。"""
    if not query.strip():
        return gr.update(visible=False), []
    results, result_ids = project_manager.search_projects(query)
    if not results:
        return gr.update(visible=True, value=[["", "未找到匹配内容"]]), []
    return gr.update(visible=True, value=results), result_ids


def _on_open_from_search(evt: gr.SelectData, search_result_ids: list[str]) -> tuple:
    """从搜索结果打开项目。"""
    try:
        idx = evt.index[0] if isinstance(evt.index, (list, tuple)) else evt.index
        if idx < 0 or idx >= len(search_result_ids):
            return tuple([gr.update()] * 9)
        pid = search_result_ids[idx]
    except (IndexError, TypeError, ValueError):
        return tuple([gr.update()] * 9)
    return _open_project(pid)


def _on_clear_chat(project_id: str | None, midi_files: list[dict]) -> tuple[list, list]:
    """清空对话并保留 MIDI 文件元数据。"""
    if project_id:
        project_manager.save_history(project_id, [], midi_files or [])
    return [], []


# ==================== Gradio UI ====================

def build_chat_ui() -> gr.Blocks:
    """构建多轮对话界面（含项目浏览器）。"""

    # 预先加载项目列表，避免 app.load 在后台启动模式下不触发
    _initial_choices, _initial_ids = _refresh_project_list()

    with gr.Blocks(title="AI_MIDI · 多轮对话") as app:
        gr.Markdown("# AI_MIDI · 多轮对话")

        # ---- 全局状态 ----
        project_id_state = gr.State(None)
        project_list_ids = gr.State(value=_initial_ids)
        search_result_ids = gr.State([])

        # ==================== 项目浏览器 ====================
        with gr.Column(visible=True) as project_browser:
            gr.Markdown("### 选择项目")

            with gr.Row():
                new_project_btn = gr.Button("＋ 新建项目", variant="primary", scale=1)
                search_input = gr.Textbox(placeholder="搜索对话内容…", scale=3, show_label=False)
                search_btn = gr.Button("🔍 搜索", scale=1)

            project_radio = gr.Radio(
                label="历史项目",
                choices=_initial_choices,
                interactive=True,
            )

            with gr.Row():
                open_project_btn = gr.Button("📂 打开", variant="primary", scale=2)
                rename_project_btn = gr.Button("✏ 重命名", scale=1)
                copy_project_btn = gr.Button("📋 复制", scale=1)
                delete_project_btn = gr.Button("🗑 删除", variant="stop", scale=1)

            # 重命名区域（默认隐藏）
            with gr.Row(visible=False) as rename_row:
                rename_input = gr.Textbox(label="新名称", placeholder="输入新项目名称…", scale=3)
                rename_confirm_btn = gr.Button("确认重命名", variant="primary", scale=1)

            # 新建项目名称输入
            with gr.Row(visible=False) as new_project_row:
                new_project_input = gr.Textbox(label="项目名称", placeholder="输入项目名称…", scale=3)
                new_project_confirm_btn = gr.Button("创建", variant="primary", scale=1)

            # 搜索结果
            search_results = gr.Dataframe(
                headers=["项目名称", "匹配内容"],
                datatype=["str", "str"],
                interactive=False,
                label="搜索结果",
                visible=False,
            )

        # ==================== 聊天面板 ====================
        with gr.Column(visible=False) as chat_panel:
            with gr.Row():
                back_btn = gr.Button("← 返回项目列表", scale=1)
                project_name_display = gr.Markdown("### 当前项目: (未选择)", scale=4)

            with gr.Row():
                with gr.Column(scale=1):
                    # ===== MIDI 文件管理区 =====
                    gr.Markdown("### MIDI 文件")
                    midi_files_state = gr.State([])
                    undo_stack = gr.State([])
                    full_history_state = gr.State([])  # 完整 API 消息（含 tool 调用）
                    file_checkboxes = gr.CheckboxGroup(
                        label="勾选要操作的文件",
                        choices=[],
                        interactive=True,
                    )

                    with gr.Row():
                        upload_btn = gr.UploadButton(
                            "📁 上传",
                            file_types=[".mid", ".midi"],
                            file_count="multiple",
                            scale=3,
                        )
                        download_btn = gr.Button("💾 下载", scale=2)
                        delete_btn = gr.Button("🗑 删除", variant="stop", scale=2)
                    undo_btn = gr.Button("↩ 撤销")

                    download_file = gr.File(label="下载文件", visible=True)

                    # ===== 对话设置 =====
                    gr.Markdown("---")
                    gr.Markdown("### 对话设置")
                    gr.Markdown("- 模型：step-3.7-flash\n- 上下文长度：自动\n- 思考强度：max")

                with gr.Column(scale=3):
                    chatbot = gr.Chatbot(
                        label="对话",
                        height=500,
                    )
                    with gr.Row():
                        msg_input = gr.Textbox(
                            label="",
                            placeholder="输入你的要求…",
                            lines=2,
                            scale=4,
                        )
                        send_btn = gr.Button("发送", variant="primary", scale=1)
                    clear_btn = gr.Button("清空对话")

        # ==================== 事件绑定 ====================

        # ---- 项目浏览器事件 ----

        # 页面加载时刷新项目列表
        app.load(
            fn=_refresh_project_list,
            inputs=[],
            outputs=[project_radio, project_list_ids],
        )

        # 新建项目
        def _show_new_project_input():
            return gr.update(visible=True)

        new_project_btn.click(
            fn=_show_new_project_input,
            inputs=[],
            outputs=[new_project_row],
        )

        # 项目浏览器输出列表：project_browser, chat_panel, project_id_state,
        # chatbot, midi_files_state, undo_stack, full_history_state,
        # file_checkboxes, project_name_display
        project_outputs = [
            project_browser, chat_panel, project_id_state,
            chatbot, midi_files_state, undo_stack, full_history_state,
            file_checkboxes, project_name_display,
        ]

        new_project_confirm_btn.click(
            fn=_on_new_project,
            inputs=[new_project_input],
            outputs=project_outputs,
        ).then(
            fn=lambda: (gr.update(value="", visible=False), gr.update(value="")),
            inputs=[],
            outputs=[new_project_row, new_project_input],
        )

        # 打开项目
        open_project_btn.click(
            fn=_on_open_project,
            inputs=[project_radio, project_list_ids],
            outputs=project_outputs,
        )

        # 删除项目
        delete_project_btn.click(
            fn=_on_delete_project,
            inputs=[project_radio, project_list_ids],
            outputs=[project_radio, project_list_ids],
        )

        # 重命名
        rename_project_btn.click(
            fn=_on_rename_show,
            inputs=[],
            outputs=[rename_row, rename_input],
        )

        rename_confirm_btn.click(
            fn=_on_confirm_rename,
            inputs=[rename_input, project_radio, project_list_ids],
            outputs=[project_radio, project_list_ids, rename_row, rename_input],
        )

        # 复制项目
        copy_project_btn.click(
            fn=_on_copy_project,
            inputs=[project_radio, project_list_ids],
            outputs=[project_radio, project_list_ids],
        )

        # 搜索
        search_btn.click(
            fn=_on_search,
            inputs=[search_input],
            outputs=[search_results, search_result_ids],
        )

        search_results.select(
            fn=_on_open_from_search,
            inputs=[search_result_ids],
            outputs=project_outputs,
        )

        # ---- 聊天面板事件 ----

        # 返回项目列表
        back_btn.click(
            fn=_close_project,
            inputs=[project_id_state, full_history_state, midi_files_state],
            outputs=[
                project_browser,
                chat_panel,
                project_id_state,
                project_radio,
                project_list_ids,
            ],
        )

        # 文件管理
        upload_btn.change(
            fn=_on_upload,
            inputs=[upload_btn, midi_files_state, undo_stack, project_id_state, full_history_state],
            outputs=[midi_files_state, undo_stack, file_checkboxes],
        )

        delete_btn.click(
            fn=_on_delete,
            inputs=[midi_files_state, file_checkboxes, undo_stack, project_id_state, full_history_state],
            outputs=[midi_files_state, undo_stack, file_checkboxes],
        )

        download_btn.click(
            fn=_on_download,
            inputs=[midi_files_state, file_checkboxes],
            outputs=[download_file],
        )

        undo_btn.click(
            fn=_on_undo,
            inputs=[undo_stack, midi_files_state, project_id_state, full_history_state],
            outputs=[midi_files_state, undo_stack, file_checkboxes],
        )

        # 对话事件
        chat_outputs = [chatbot, msg_input, midi_files_state, undo_stack, full_history_state, download_file, file_checkboxes]

        send_btn.click(
            fn=send_message,
            inputs=[msg_input, chatbot, midi_files_state, undo_stack, full_history_state],
            outputs=chat_outputs,
        )
        msg_input.submit(
            fn=send_message,
            inputs=[msg_input, chatbot, midi_files_state, undo_stack, full_history_state],
            outputs=chat_outputs,
        )

        clear_btn.click(
            fn=_on_clear_chat,
            inputs=[project_id_state, midi_files_state],
            outputs=[chatbot, full_history_state],
        )

    return app


def main() -> None:
    """启动多轮对话服务。"""
    import argparse

    parser = argparse.ArgumentParser(description="AI_MIDI Chat UI")
    parser.add_argument("--port", type=int, default=7861, help="服务端口")
    parser.add_argument("--browser", action="store_true", help="自动打开浏览器")
    args = parser.parse_args()

    app = build_chat_ui()
    app.launch(
        server_name="127.0.0.1",
        server_port=args.port,
        share=False,
        inbrowser=args.browser,
    )


if __name__ == "__main__":
    main()
