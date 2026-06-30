"""多轮对话 Web UI。

基于 Gradio 实现，支持多轮对话、AI 通过 MCP 工具操作 MIDI 文件、
流式输出、撤销等功能。
"""
import copy
import json
import logging
import os
import re
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


# ==================== Library 知识加载 ====================

def _load_library_knowledge() -> str:
    """读取 Library/ 目录下所有 .md 文件，合并为一段知识文本注入 system prompt。"""
    if not _LIBRARY_DIR.exists():
        return ""

    md_files = sorted(_LIBRARY_DIR.glob("*.md"))
    if not md_files:
        return ""

    parts = []
    for path in md_files:
        content = path.read_text(encoding="utf-8").strip()
        parts.append(f"## {path.stem}\n{content}")

    return "\n\n---\n\n".join(parts)


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

    library_knowledge = _load_library_knowledge()

    prompt = (
        "你是一位精通乐理的音乐 AI 助手，帮助用户处理 MIDI 音乐文件。\n\n"
        f"{_NOTE_TABLE_INTRO}\n\n"
    )

    # 工具使用指南（系统会自动将工具定义注册到 function calling 中）
    prompt += (
        "## 工具使用指南\n"
        "你拥有以下工具，可以直接调用它们来操作 MIDI 文件和查阅知识库：\n\n"
        "- **list_midi_files**: 无参数，列出 output 目录下的所有 MIDI 文件\n"
        "- **parse_midi**: 参数 filename（字符串），解析 MIDI 文件返回 note_table\n"
        "- **create_midi**: 参数 filename（字符串）、bpm（整数）、notes（字符串，note_table 格式）\n"
        "  必须同时提供 filename、bpm 和 notes 三个参数\n"
        "  notes 的格式必须是每行一个音符：[note: \"C4\", velocity: \"80\", start: \"1\", end: \"2\"]\n"
        "- **delete_midi**: 参数 filename（字符串），删除指定的 MIDI 文件\n"
        "- **read_library_file**: 参数 filename（字符串），读取 Library 知识库中的指定文件\n\n"
        "## 工具调用规则\n"
        "1. 系统会自动将上述工具注册到你的 function calling 能力中，你只需在需要时调用\n"
        "2. 每次可以调用一个或多个工具，等待系统返回结果\n"
        "3. 工具的执行结果会以新的消息返回给你，你需要根据结果决定下一步\n"
        "4. 如果需要查阅知识库，直接使用 read_library_file 读取对应文件\n"
        "5. 工具调用完毕后，用自然语言向用户汇报结果和你的分析\n\n"
    )

    if library_knowledge:
        prompt += (
            "## 乐理知识库摘要\n"
            "你的知识库包含 20 份文件，涵盖和弦、音阶、转音、节奏、配器、曲式等主题。"
            "具体文件列表如下（详细内容已通过 read_library_file 工具提供）：\n"
            "01_乐理基础 | 02_配和弦指南 | 03_歌词翻译指南 | 04_转音设计指南 | "
            "05_作曲编曲通用技巧 | 06_和弦进阶与风格化 | 07_音阶与即兴创作模板 | "
            "08_和弦进行词典 | 09_音域运用与音程写作 | 10_旋律写作与记忆点 | "
            "11_节奏与律动 | 12_风格化写作与编曲要素 | "
            "13_调性识别与和弦功能分析 | 14_MIDI真实感与演奏润色 | "
            "15_歌词创作指南 | 16_织体关系与声部配合 | "
            "17_调式互换与转调 | 18_对位与多声部写作 | "
            "19_配器法入门 | 20_曲式结构与段落设计\n\n"
            "当用户询问具体的乐理问题时，使用 read_library_file 工具读取对应文件获取详细信息。\n\n"
        )

    return prompt


# ==================== 设置加载 ====================

def _load_settings() -> dict:
    """从 config 加载 API 设置。"""
    settings = config.load_settings()
    return {
        "api_key": settings.get("api_key") or config.get_api_key(),
        "base_url": settings.get("base_url") or config.BASE_URL,
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
            _mcp_process = subprocess.Popen(
                [sys.executable, str(_MCP_SCRIPT)],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                encoding="utf-8",
                bufsize=0,
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


def _mcp_recv(proc: subprocess.Popen) -> dict | None:
    """从 MCP 进程读取一条 JSON-RPC 响应。"""
    line = proc.stdout.readline()
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
    """上传文件，解析为 note_table 并追加到列表。"""
    if not files:
        return current_list, gr.update()
    new_files = []
    for f in files:
        path = f.name if hasattr(f, "name") else str(f)
        basename = os.path.basename(path)
        try:
            note_table = get.get_note(path, save_to_file=False)
            note_table_str = "\n".join(note_table) if note_table else ""
        except Exception:
            note_table_str = ""
        new_files.append({
            "name": basename,
            "path": path,
            "size": os.path.getsize(path) if os.path.isfile(path) else 0,
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


def _make_tool_result_message(tool_call_id: str, result: str) -> dict:
    """构造 tool 角色的消息（OpenAI API 格式）。"""
    return {
        "role": "tool",
        "tool_call_id": tool_call_id,
        "content": result,
    }


def send_message(message, history, midi_files, undo_stack):
    """发送消息，调用 AI 并通过 MCP 工具执行操作。

    支持多轮工具调用：AI 可能连续调用多个工具，
    每次调用后把结果回传给 AI，直到 AI 给出最终回复。
    """
    if not message.strip():
        yield history, "", midi_files, undo_stack, None, gr.update()
        return

    settings = _load_settings()
    if not settings["api_key"]:
        yield history + [
            {"role": "user", "content": message},
            {"role": "assistant", "content": "⚠ 请先在主界面设置页填写并保存 API Key。"},
        ], "", midi_files, undo_stack, None, gr.update()
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

    # 构建对话消息
    system_prompt = _build_system_prompt(midi_files)
    messages = [{"role": "system", "content": system_prompt}]
    for msg in history:
        messages.append(msg)
    messages.append({"role": "user", "content": message})

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

    try:
        for round_idx in range(max_tool_rounds):
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
                result_text = msg_content or ""
                # 追加工具执行日志
                if tool_log:
                    log_text = "\n\n---\n\n".join(tool_log)
                    result_text = (msg_content + "\n\n" + log_text).strip()
                yield (
                    history + [
                        {"role": "user", "content": message},
                        {"role": "assistant", "content": result_text},
                    ],
                    "",
                    updated_files,
                    new_undo_stack,
                    download_path,
                    gr.update(choices=_get_choices(updated_files), value=[]),
                )
                return

            # 执行工具调用
            for tc in msg_tool_calls:
                tc_id = tc.id
                tc_name = tc.function.name
                tc_args_raw = tc.function.arguments

                try:
                    tc_args = json.loads(tc_args_raw) if isinstance(tc_args_raw, str) else tc_args_raw
                except json.JSONDecodeError:
                    tc_args = {}

                result_text, updated_files = _execute_tool_call(
                    {"function": {"name": tc_name, "arguments": tc_args}},
                    updated_files,
                )

                # 记录工具执行日志
                args_str = ", ".join(f"{k}={v}" for k, v in tc_args.items())
                tool_log.append(f"**工具调用** `{tc_name}({args_str})`\n```\n{result_text}\n```")

                # 如果创建了文件，设置下载路径
                if tc_name == "create_midi":
                    filename = tc_args.get("filename", "")
                    filepath = config.OUTPUT_DIR / filename
                    if filepath.exists():
                        download_path = str(filepath)

                # 将工具结果回传给 API
                messages.append(_make_tool_result_message(tc_id, result_text))

        # 达到最大轮数，用当前内容作为最终回复
        final_content = messages[-1].get("content", "（已达到最大工具调用轮数）")
        if tool_log:
            final_content += "\n\n---\n\n" + "\n\n".join(tool_log)
        yield (
            history + [
                {"role": "user", "content": message},
                {"role": "assistant", "content": final_content},
            ],
            "",
            updated_files,
            new_undo_stack,
            download_path,
            gr.update(choices=_get_choices(updated_files), value=[]),
        )

    except Exception as e:
        error_msg = f"⚠ 调用失败: {e}"
        yield (
            history + [
                {"role": "user", "content": message},
                {"role": "assistant", "content": error_msg},
            ],
            "",
            updated_files,
            new_undo_stack,
            None,
            gr.update(),
        )


# ==================== Gradio UI ====================

def build_chat_ui() -> gr.Blocks:
    """构建多轮对话界面。"""
    with gr.Blocks(title="AI_MIDI · 多轮对话") as app:
        gr.Markdown("# AI_MIDI · 多轮对话")
        gr.Markdown("上传 MIDI → 多轮对话 → AI 通过工具操作 → 输出结果")

        with gr.Row():
            with gr.Column(scale=1):
                # ===== MIDI 文件管理区 =====
                gr.Markdown("### MIDI 文件")
                midi_files_state = gr.State([])
                undo_stack = gr.State([])
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

        # ---- 文件管理事件 ----
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

        # ---- 对话事件 ----
        chat_outputs = [chatbot, msg_input, midi_files_state, undo_stack, download_file, file_checkboxes]

        send_btn.click(
            fn=send_message,
            inputs=[msg_input, chatbot, midi_files_state, undo_stack],
            outputs=chat_outputs,
        )
        msg_input.submit(
            fn=send_message,
            inputs=[msg_input, chatbot, midi_files_state, undo_stack],
            outputs=chat_outputs,
        )

        clear_btn.click(fn=lambda: [], inputs=[], outputs=[chatbot])

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
