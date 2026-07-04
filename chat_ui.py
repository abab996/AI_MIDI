"""多轮对话 Web UI。

基于 Gradio 实现，支持多轮对话、AI 通过 MCP 工具操作 MIDI 文件、
流式输出、撤销等功能。
"""
import copy
import json
import logging
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import zipfile
from pathlib import Path

import gradio as gr

import ai_api
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

# 仅允许向已知可信的 API 服务商发送请求,防止密钥被中间人窃取。
_ALLOWED_BASE_URL_DOMAINS = {
    "api.deepseek.com",
    "api.openai.com",
    "openai.azure.com",
    "api.anthropic.com",
    "api.moonshot.cn",
    "api.stepfun.com",
    "api.zhipuai.cn",
    "qianwen.aliyuncs.com",
    "dashscope.aliyuncs.com",
}


def _validate_base_url(base_url: str) -> str:
    """验证 base_url 仅指向允许的域名,不安全时抛出 ValueError。"""
    from urllib.parse import urlparse

    url = base_url.strip()
    if not url:
        return config.BASE_URL
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https"):
        raise ValueError(f"base_url 协议必须为 http 或 https: {parsed.scheme}")
    host = parsed.hostname or ""
    if host in _ALLOWED_BASE_URL_DOMAINS:
        return f"{parsed.scheme}://{host}{parsed.path}".rstrip("/")
    raise ValueError(
        f"base_url 域名不在允许列表中: {host}。"
        f"如需使用其他服务商,请修改 _ALLOWED_BASE_URL_DOMAINS。"
    )


def _load_settings() -> dict:
    """从 config 加载 API 设置。"""
    settings = config.load_settings()
    raw_base_url = settings.get("base_url", "") or config.BASE_URL
    try:
        base_url = _validate_base_url(raw_base_url)
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
                return None
            # 初始化 MCP 握手
            _mcp_handshake(_mcp_process)
            _mcp_initialized = True
            return _mcp_process
        except Exception:
            return None


def _mcp_handshake(proc: subprocess.Popen) -> None:
    """执行 MCP 初始化握手。"""
    # Send initialize request
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
    _mcp_recv(proc)  # initialize result
    # Send initialized notification
    _mcp_send(proc, {"jsonrpc": "2.0", "method": "notifications/initialized"})


def _mcp_send(proc: subprocess.Popen, message: dict) -> None:
    """向 MCP 进程发送 JSON-RPC 消息。"""
    payload = json.dumps(message, ensure_ascii=False)
    line = f"{payload}\n"
    proc.stdin.write(line)
    proc.stdin.flush()


def _mcp_recv(proc: subprocess.Popen, timeout: float = 30.0) -> dict | None:
    """从 MCP 进程读取一条 JSON-RPC 响应（带超时）。"""
    import queue
    import threading

    q: queue.Queue = queue.Queue()

    def _read():
        try:
            line = proc.stdout.readline()
            q.put(line)
        except Exception:
            q.put(None)

    t = threading.Thread(target=_read, daemon=True)
    t.start()
    t.join(timeout)

    if t.is_alive():
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
        if _mcp_process and _mcp_process.poll() is None:
            try:
                _mcp_process.stdin.close()
                _mcp_process.terminate()
                _mcp_process.wait(timeout=5)
            except Exception:
                try:
                    _mcp_process.kill()
                except Exception:
                    pass
        _mcp_process = None
        _mcp_initialized = False


# ==================== MIDI 操作（兼容旧接口） ====================

def _on_upload(files, current_list):
    """上传文件，解析为 note_table 并追加到列表。文件复制到项目 midi 目录。"""
    if not files:
        return current_list, gr.update()
    new_files = []
    # 确定目标目录：项目目录或临时目录
    if _current_project_id:
        dest_dir = project_manager.midi_dir(_current_project_id)
        dest_dir.mkdir(parents=True, exist_ok=True)
    else:
        dest_dir = config.OUTPUT_DIR
        dest_dir.mkdir(exist_ok=True)

    for f in files:
        src_path = f.name if hasattr(f, "name") else str(f)
        basename = os.path.basename(src_path)
        # 复制到项目/输出目录
        dest_path = dest_dir / basename
        shutil.copy2(src_path, dest_path)
        try:
            note_table = get.get_note(str(dest_path), save_to_file=False)
            note_table_str = "\n".join(note_table) if note_table else ""
        except Exception:
            note_table_str = ""
        new_files.append({
            "name": basename,
            "path": str(dest_path),
            "size": dest_path.stat().st_size if dest_path.exists() else 0,
            "note_table": note_table_str,
        })
    updated = current_list + new_files
    return updated, gr.update(choices=_get_choices(updated), value=[])


def _on_delete(current_list, selected):
    """删除勾选的文件。"""
    if not current_list or not selected:
        return current_list, gr.update()
    selected_names = {s.split("  (")[0] for s in selected}
    updated = [f for f in current_list if f["name"] not in selected_names]
    return updated, gr.update(choices=_get_choices(updated), value=[])


def _on_download(current_list, selected):
    """下载勾选的文件。未勾选则下载全部。"""
    if not current_list:
        return None

    if selected:
        selected_names = {s.split("  (")[0] for s in selected}
        files = [f for f in current_list if f["name"] in selected_names]
    else:
        files = list(current_list)

    if not files:
        return None

    if len(files) == 1 and os.path.isfile(files[0]["path"]):
        return files[0]["path"]

    zip_path = os.path.join(tempfile.gettempdir(), "AI_MIDI_files.zip")
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
        for f in files:
            if os.path.isfile(f["path"]):
                zf.write(f["path"], f["name"])
    return zip_path


def _on_undo(undo_stack, current_files):
    """撤销最近一次操作。"""
    if not undo_stack:
        return current_files, [], gr.update(choices=_get_choices(current_files))
    restored = undo_stack.pop()
    return restored, undo_stack, gr.update(choices=_get_choices(restored), value=[])


def _get_choices(files: list[dict]) -> list[str]:
    """生成 CheckboxGroup 选项列表。"""
    return [
        f"{f['name']}  ({max(1, f.get('size', 0) // 1024)} KB)"
        for f in files
    ]


# ==================== 上下文压缩 ====================

# 压缩阈值（估算 tokens ≈ chars / 3）
_MAX_CONTEXT_CHARS = 400_000  # 约为 133K tokens，适配大多数模型的 256K 上限
_COMPACT_KEEP_RECENT = 3       # 保留最近 3 轮对话


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
            snippet = content[:100].replace("\n", " ")
            lines.append(f"- 用户: {snippet}...")
        elif role == "assistant" and content:
            # 截取 AI 回复的前 80 字
            snippet = content[:80].replace("\n", " ")
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
        if _current_project_id:
            filepath = project_manager.resolve_midi_path(_current_project_id, filename)
        else:
            filepath = config.OUTPUT_DIR / filename
        if filepath.exists():
            note_data = func_args.get("notes", "")
            updated_files.append({
                "name": filename,
                "path": str(filepath),
                "size": filepath.stat().st_size,
                "note_table": note_data,
            })

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


def _format_single_tool_entry(tc_name: str, args_str: str, result_text: str) -> str:
    """格式化单个工具调用为可折叠 HTML details 标签。"""
    return (
        f"<details>\n"
        f"<summary>🔧 调用 `{tc_name}({args_str})`</summary>\n\n"
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
    if not message.strip():
        yield history, "", midi_files, undo_stack, full_history, None, gr.update()
        return

    settings = _load_settings()
    if not settings["api_key"]:
        yield history + [
            {"role": "user", "content": message},
            {"role": "assistant", "content": "⚠ 请先在主界面设置页填写并保存 API Key。"},
        ], "", midi_files, undo_stack, full_history, None, gr.update()
        return

    # 快照当前文件列表（用于撤销）
    snapshot = copy.deepcopy(midi_files)
    new_undo_stack = undo_stack + [snapshot]

    # 获取 MCP 工具定义
    openai_tools = _mcp_list_tools()
    logger.info("MCP 工具数量: %d", len(openai_tools))
    if openai_tools:
        logger.info("MCP 工具列表: %s", [t["function"]["name"] for t in openai_tools])
    else:
        logger.warning("MCP 工具列表为空！tools 参数不会传给 API")

    # 构建对话消息：使用完整 API 历史作为上下文
    system_prompt = _build_system_prompt(midi_files)
    messages = [{"role": "system", "content": system_prompt}]
    for msg in full_history:
        messages.append(msg)
    messages.append({"role": "user", "content": message})

    # 记录本轮起始索引（之后新增的 assistant/tool 消息才是本轮产物）
    _round_start_idx = len(messages)  # user 消息在最后

    client = ai_api.get_client(
        api_key=settings["api_key"],
        base_url=settings["base_url"],
    )

    # 构建 API 参数
    kwargs = {
        "model": settings["model"],
        "messages": messages,
        "stream": False,
    }
    if openai_tools:
        kwargs["tools"] = openai_tools
    if settings["max_tokens"]:
        kwargs["max_tokens"] = int(settings["max_tokens"])
    if settings["max_completion_tokens"]:
        kwargs["max_completion_tokens"] = int(settings["max_completion_tokens"])
    if settings["reasoning_effort"]:
        kwargs["reasoning_effort"] = settings["reasoning_effort"]
    if settings["thinking_enabled"]:
        kwargs["extra_body"] = {"thinking": {"type": "enabled"}}

    updated_files = list(midi_files)
    download_path = None
    tool_log: list[str] = []
    max_tool_rounds = 10
    chat_display = list(history)
    # 预添加本轮用户消息（所有路径共享，避免后续重复添加）
    chat_display.append({"role": "user", "content": message})

    try:
        for round_idx in range(max_tool_rounds):
            # 上下文压缩：检查并压缩过大历史
            if _should_compact(messages, full_history):
                logger.warning(
                    "上下文过大 (约 %d tokens)，执行压缩...",
                    sum(len(str(m.get("content", ""))) for m in messages) // 3,
                )
                full_history = _compact_full_history(full_history)
                # 重建 messages：system + compacted_full_history + user
                messages = [{"role": "system", "content": system_prompt}]
                for msg in full_history:
                    messages.append(msg)
                messages.append({"role": "user", "content": message})
                kwargs["messages"] = messages
                _round_start_idx = len(messages)
                logger.info("压缩完成，full_history 长度: %d", len(full_history))
            # 更新 system prompt 以反映当前文件列表
            kwargs["messages"][0] = {"role": "system", "content": _build_system_prompt(updated_files)}

            response = client.chat.completions.create(**kwargs)
            choice = response.choices[0]
            assistant_msg = choice.message

            logger.info("API 返回: finish_reason=%s, tool_calls=%d, content=%s",
                       choice.finish_reason,
                       len(getattr(assistant_msg, "tool_calls", None) or []),
                       (assistant_msg.content or "")[:100])

            # 构造助手消息
            msg_content = assistant_msg.content if assistant_msg.content else None
            msg_tool_calls = getattr(assistant_msg, "tool_calls", None) or []

            assistant_message = {
                "role": "assistant",
                "content": msg_content,
            }
            if msg_tool_calls:
                assistant_message["tool_calls"] = [
                    {
                        "id": tc.id,
                        "type": tc.type or "function",
                        "function": {
                            "name": tc.function.name,
                            "arguments": tc.function.arguments,
                        },
                    }
                    for tc in msg_tool_calls
                ]

            messages.append(assistant_message)

            # 如果没有工具调用，说明 AI 给出了最终回复
            if not msg_tool_calls:
                # 构建完整 API 历史
                new_full_history = full_history + [
                    {"role": "user", "content": message},
                ]
                for api_msg in messages[_round_start_idx:]:
                    if api_msg.get("role") in ("assistant", "tool"):
                        new_full_history.append(api_msg)

                # 构建最终显示内容：AI 回复（工具 details 已在 chat_display 中）
                final_display = msg_content or ""

                # 逐字流式显示
                displayed = ""
                # 添加一条空的 assistant 消息作为流式输出的占位
                chat_display.append({"role": "assistant", "content": ""})
                for ch in final_display:
                    displayed += ch
                    chat_display[-1] = {"role": "assistant", "content": displayed}
                    yield (
                        list(chat_display),
                        "",
                        updated_files,
                        new_undo_stack,
                        new_full_history,
                        download_path,
                        gr.update(choices=_get_choices(updated_files), value=[]),
                    )

                # 保存历史
                if _current_project_id:
                    project_manager.save_history(_current_project_id, new_full_history, updated_files)

                return

            # 如果 AI 在调用工具前有说明文字，先显示
            if msg_content and msg_content.strip():
                chat_display.append({"role": "assistant", "content": msg_content})
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    gr.update(choices=_get_choices(updated_files), value=[]),
                )

            # 执行工具调用
            for tc in msg_tool_calls:
                tc_id = tc.id
                tc_name = tc.function.name
                tc_args_raw = tc.function.arguments

                # 显示"正在调用工具"状态
                chat_display.append({
                    "role": "assistant",
                    "content": f"🔧 正在调用 `{tc_name}`...",
                })
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    gr.update(choices=_get_choices(updated_files), value=[]),
                )

                try:
                    tc_args = json.loads(tc_args_raw) if isinstance(tc_args_raw, str) else tc_args_raw
                except json.JSONDecodeError:
                    tc_args = {}

                result_text, updated_files = _execute_tool_call(
                    {"function": {"name": tc_name, "arguments": tc_args}},
                    updated_files,
                )

                # 构建 args_str 和 details 标签
                args_str = ", ".join(f"{k}={v}" for k, v in tc_args.items())
                entry = _format_single_tool_entry(tc_name, args_str, result_text)

                # 记录工具执行日志（使用 details 标签格式，与 chat_display 一致）
                tool_log.append(entry)

                # 如果创建了文件，设置下载路径
                if tc_name == "create_midi":
                    filename = tc_args.get("filename", "")
                    if _current_project_id:
                        filepath = project_manager.resolve_midi_path(_current_project_id, filename)
                    else:
                        filepath = config.OUTPUT_DIR / filename
                    if filepath.exists():
                        download_path = str(filepath)

                # 将工具结果回传给 API
                messages.append(_make_tool_result_message(tc_id, result_text))

                # 更新 chat_display：将"正在调用..."替换为包含结果的 details 标签
                if chat_display and chat_display[-1].get("role") == "assistant":
                    # 替换最后一条 assistant 消息
                    chat_display[-1] = {"role": "assistant", "content": entry}
                else:
                    chat_display.append({"role": "assistant", "content": entry})
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    gr.update(choices=_get_choices(updated_files), value=[]),
                )

        # 达到最大轮数，用当前内容作为最终回复
        tool_log_html = _format_tool_log(tool_log)
        final_content = messages[-1].get("content", "（已达到最大工具调用轮数）")

        # 构建完整 API 历史
        new_full_history = full_history + [{"role": "user", "content": message}]
        for api_msg in messages[_round_start_idx:]:
            if api_msg.get("role") in ("assistant", "tool"):
                new_full_history.append(api_msg)

        # 构建最终显示内容（工具 details 已在 chat_display 中）
        full_display = final_content

        # 逐字流式显示
        displayed = ""
        # 添加一条空的 assistant 消息作为流式输出的占位
        chat_display.append({"role": "assistant", "content": ""})
        for ch in full_display:
            displayed += ch
            chat_display[-1] = {"role": "assistant", "content": displayed}
            yield (
                list(chat_display),
                "",
                updated_files,
                new_undo_stack,
                new_full_history,
                download_path,
                gr.update(choices=_get_choices(updated_files), value=[]),
            )

        # 保存历史
        if _current_project_id:
            project_manager.save_history(_current_project_id, new_full_history, updated_files)

    except Exception as e:
        # 如果是 GeneratorExit，说明用户取消了操作，不修改 history
        if isinstance(e, GeneratorExit):
            raise
        tool_log_html = _format_tool_log(tool_log)
        # 不直接 str(e) 暴露给 UI，避免 API 异常可能包含敏感请求信息
        error_base = (
            f"⚠ 调用失败: {type(e).__name__}"
            + (f" (HTTP {e.status_code})" if hasattr(e, 'status_code') else "")
        )
        error_msg = (error_base + "\n\n" + tool_log_html).strip() if tool_log_html else error_base
        chat_display.append({"role": "assistant", "content": error_msg})
        new_error_history = full_history + [{"role": "user", "content": message}]
        for api_msg in messages[_round_start_idx:]:
            if api_msg.get("role") in ("assistant", "tool"):
                new_error_history.append(api_msg)
        if _current_project_id:
            project_manager.save_history(_current_project_id, new_error_history, updated_files)
        yield (
            list(chat_display),
            "",
            updated_files,
            new_undo_stack,
            new_error_history,
            None,
            gr.update(),
        )


# ==================== 项目生命周期 ====================

def _restart_mcp_for_project(project_id: str) -> None:
    """关闭现有 MCP 子进程，下次 _ensure_mcp_process() 将以新项目目录重启。"""
    global _current_project_id
    _current_project_id = project_id
    _close_mcp_process()


def _open_project(project_id: str) -> tuple:
    """加载项目历史和 MIDI 文件，重启 MCP，返回 Gradio 更新。"""
    global _current_project_id
    _current_project_id = project_id
    _close_mcp_process()

    all_messages, midi_files = project_manager.load_history(project_id)
    meta = project_manager.load_project(project_id)
    choices = _get_choices(midi_files)

    # 只保留 user/assistant 消息用于 Chatbot 显示（过滤 tool 角色消息）
    display_messages = [
        m for m in all_messages
        if m.get("role") in ("user", "assistant") and m.get("content")
    ]

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


def _close_project() -> tuple:
    """保存当前状态，返回项目浏览器并刷新项目列表。"""
    global _current_project_id
    _current_project_id = None
    _close_mcp_process()
    choices, ids = _refresh_project_list()
    return (
        gr.update(visible=True),       # 显示项目浏览器
        gr.update(visible=False),      # 隐藏聊天面板
        None,                           # project_id_state
        gr.update(choices=choices),     # project_radio
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


def _refresh_project_list() -> tuple[list[str], list[str]]:
    """从索引加载项目列表，返回 (下拉选项列表, 项目ID列表)。"""
    projects = project_manager.list_projects()
    choices = []
    ids = []
    for p in projects:
        name = p.get("name", "未命名")
        msg_count = p.get("message_count", 0)
        label = f"{name}  ({msg_count} 条消息)" if msg_count else name
        choices.append(label)
        ids.append(p["id"])
    return choices, ids


def _extract_project_name(dropdown_label: str) -> str:
    """从下拉选项标签中提取项目名称（去掉 '  (N 条消息)' 后缀）。"""
    if "  (" in dropdown_label:
        return dropdown_label[: dropdown_label.rfind("  (")]
    return dropdown_label


def _resolve_project_id(selected_name: str, project_ids: list[str]) -> str | None:
    """根据下拉选项标签和 ID 列表，解析出项目 ID。"""
    if not selected_name or not project_ids:
        return None
    # 先尝试精确匹配（名称本身就在 choices 中）
    # 然后通过索引匹配（choices 和 ids 是平行数组）
    name = _extract_project_name(selected_name)
    # 按索引查找：遍历 ids，通过 list_projects 的顺序匹配
    projects = project_manager.list_projects()
    for i, p in enumerate(projects):
        if i < len(project_ids) and p["name"] == name:
            return project_ids[i]
    return None


def _on_open_project(selected_name: str, project_ids: list[str]) -> tuple:
    """打开选中的项目。"""
    pid = _resolve_project_id(selected_name, project_ids)
    if not pid:
        return tuple([gr.update()] * 9)
    return _open_project(pid)


def _on_delete_project(selected_name: str, project_ids: list[str]) -> tuple:
    """删除选中的项目。"""
    pid = _resolve_project_id(selected_name, project_ids)
    if not pid:
        return gr.update(), []
    project_manager.delete_project(pid)
    choices, ids = _refresh_project_list()
    return gr.update(choices=choices), ids


def _on_rename_show() -> tuple:
    """显示重命名输入框。"""
    return gr.update(visible=True), gr.update(visible=True)


def _on_confirm_rename(new_name: str, selected_name: str, project_ids: list[str]) -> tuple:
    """确认重命名。"""
    if not new_name.strip():
        return gr.update(), [], gr.update(visible=False), gr.update(value="", visible=False)
    pid = _resolve_project_id(selected_name, project_ids)
    if not pid:
        return gr.update(), [], gr.update(visible=False), gr.update(value="", visible=False)
    project_manager.rename_project(pid, new_name.strip())
    choices, ids = _refresh_project_list()
    return gr.update(choices=choices), ids, gr.update(visible=False), gr.update(value="", visible=False)


def _on_copy_project(selected_name: str, project_ids: list[str]) -> tuple:
    """复制选中的项目。"""
    pid = _resolve_project_id(selected_name, project_ids)
    if not pid:
        return gr.update(), []
    src_meta = project_manager.load_project(pid)
    new_name = f"{src_meta.get('name', '未命名')} (副本)"
    project_manager.copy_project(pid, new_name)
    choices, ids = _refresh_project_list()
    return gr.update(choices=choices), ids


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


def _on_clear_chat(project_id: str | None) -> tuple[list, list]:
    """清空对话并持久化。"""
    if project_id:
        project_manager.save_history(project_id, [], [])
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
            inputs=[],
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
            inputs=[upload_btn, midi_files_state],
            outputs=[midi_files_state, file_checkboxes],
        )

        delete_btn.click(
            fn=_on_delete,
            inputs=[midi_files_state, file_checkboxes],
            outputs=[midi_files_state, file_checkboxes],
        )

        download_btn.click(
            fn=_on_download,
            inputs=[midi_files_state, file_checkboxes],
            outputs=[download_file],
        )

        undo_btn.click(
            fn=_on_undo,
            inputs=[undo_stack, midi_files_state],
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
            inputs=[project_id_state],
            outputs=[chatbot, full_history_state],
        )

    return app


def main() -> None:
    """启动多轮对话服务。"""
    import argparse
    import signal

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
