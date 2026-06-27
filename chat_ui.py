"""多轮对话 Web UI。

基于 Gradio 实现，支持多轮对话、AI 复用项目模块操作 MIDI 文件、
流式输出、撤销等功能。
"""
import copy
import os
import re
import tempfile
import zipfile

import gradio as gr

import ai_api
import config
import get
import out


# ==================== 常量 ====================

_SYSTEM_PROMPT_TEMPLATE = """\
你是一位精通乐理的音乐 AI 助手，可以帮助用户处理 MIDI 音乐文件。

## note_table 格式说明
{note_table_intro}

## 当前 MIDI 文件列表
{file_list}

## 工具
你可以通过在回复中嵌入 <midi_tool> 标记来操作 MIDI 文件：

### 创建/修改 MIDI 文件
<midi_tool>创建/修改文件: 文件名.mid, BPM: 120</midi_tool>
<create_midi filename="文件名.mid" bpm="120">
[note: "C4", velocity: "80", start: "1", end: "2"]
</create_midi>

### 删除 MIDI 文件
<midi_tool>删除文件: 文件名.mid</midi_tool>
<delete_midi filename="文件名.mid"/>

## 注意事项
- 修改文件时，先获取该文件的 note_table 数据，在此基础上修改
- 回复中可以正常说明你的思路，操作记录会在聊天中以折叠区块展示
- 可以同时创建/删除多个文件
- 如果用户没有上传 MIDI 文件，你可以根据用户要求从零创建
"""

_NOTE_TABLE_INTRO = (
    '由于无法直接上传midi文件，我们会使用类似midi文件的"note_table"格式来记录音符信息，'
    '以下为"note_table"的格式介绍：\n'
    '[note: "<音符键名>", velocity: "<音符力度>", start: "<音符的开始时间（拍）>", '
    'end: "<音符的结束时间（拍）>" ]\n'
    '示例 ：\n'
    '[note: "C4", velocity: "80", start: "1", end: "2" ] \n'
    '表示音符对应的按键是C4，演奏力度80，时间是第一拍到第二拍。'
)

_CREATE_MIDI_PATTERN = re.compile(
    r'<create_midi\s+filename="([^"]+)"(?:\s+bpm="(\d+)")?\s*>(.*?)</create_midi>',
    re.DOTALL,
)

_DELETE_MIDI_PATTERN = re.compile(
    r'<delete_midi\s+filename="([^"]+)"\s*/?>',
)

_MIDI_TOOL_PATTERN = re.compile(
    r'<midi_tool>(.*?)</midi_tool>',
    re.DOTALL,
)


# ==================== 系统 Prompt ====================

def _build_system_prompt(files: list[dict]) -> str:
    """构建包含 MIDI 文件上下文的 system prompt。"""
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

    return _SYSTEM_PROMPT_TEMPLATE.format(
        note_table_intro=_NOTE_TABLE_INTRO,
        file_list=file_list,
    )


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


# ==================== 标记解析 ====================

def _parse_create_midi_tags(response: str) -> list[dict]:
    """从 AI 回复中解析 <create_midi> 标记。"""
    results = []
    for match in _CREATE_MIDI_PATTERN.finditer(response):
        filename = match.group(1)
        bpm = match.group(2) or str(config.DEFAULT_BPM)
        note_data = match.group(3).strip()
        results.append({
            "filename": filename,
            "bpm": bpm,
            "note_data": note_data,
        })
    return results


def _parse_delete_midi_tags(response: str) -> list[str]:
    """从 AI 回复中解析 <delete_midi> 标记。"""
    return _DELETE_MIDI_PATTERN.findall(response)


# ==================== MIDI 操作 ====================

def _execute_create_midi(tags, current_files):
    """执行 <create_midi> 标记，返回 (更新后的文件列表, 工具展示文本)。"""
    updated = list(current_files)
    display_parts = []

    for tag in tags:
        filename = tag["filename"]
        bpm = tag["bpm"]
        note_data = tag["note_data"]
        note_count = len([l for l in note_data.split("\n") if l.strip()])

        output_path = os.path.join(str(config.OUTPUT_DIR), filename)
        os.makedirs(config.OUTPUT_DIR, exist_ok=True)
        try:
            out.txt_to_midi(note_data, output_path, bpm)
            size_kb = max(1, os.path.getsize(output_path) // 1024)
            display_parts.append(
                f"✅ 创建文件: **{filename}**\n"
                f"   - BPM: {bpm}\n"
                f"   - 音符数: {note_count}\n"
                f"   - 大小: {size_kb} KB"
            )
            found = False
            for i, f in enumerate(updated):
                if f["name"] == filename:
                    updated[i] = {
                        "name": filename,
                        "path": output_path,
                        "size": os.path.getsize(output_path),
                        "note_table": note_data,
                    }
                    found = True
                    break
            if not found:
                updated.append({
                    "name": filename,
                    "path": output_path,
                    "size": os.path.getsize(output_path),
                    "note_table": note_data,
                })
        except Exception as e:
            display_parts.append(f"❌ 创建文件失败: **{filename}**\n   - 错误: {e}")

    return updated, "\n".join(display_parts)


def _execute_delete_midi(filenames, current_files):
    """执行 <delete_midi> 标记，返回 (更新后的文件列表, 工具展示文本)。"""
    if not filenames:
        return current_files, ""
    delete_names = set(filenames)
    updated = [f for f in current_files if f["name"] not in delete_names]
    display = "\n".join(
        f"🗑 删除文件: **{name}**" for name in filenames
    )
    return updated, display


# ==================== 核心事件处理 ====================

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


# ==================== AI 对话（Generator 流式） ====================

def send_message(message, history, midi_files, undo_stack):
    """发送消息，流式接收 AI 回复，解析并执行文件操作。

    Generator 函数，yield 多次更新 Chatbot。
    AI 回复中的操作内容会被 <midi_tool> 标记包裹，
    在聊天中显示为可折叠的操作记录区块。
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

    kwargs = {
        "model": settings["model"],
        "messages": messages,
        "stream": True,
    }
    if settings["max_tokens"]:
        kwargs["max_tokens"] = int(settings["max_tokens"])
    if settings["max_completion_tokens"]:
        kwargs["max_completion_tokens"] = int(settings["max_completion_tokens"])
    if settings["reasoning_effort"]:
        kwargs["reasoning_effort"] = settings["reasoning_effort"]
    if settings["thinking_enabled"]:
        kwargs["extra_body"] = {"thinking": {"type": "enabled"}}

    # 流式接收
    partial = ""
    new_history = history + [
        {"role": "user", "content": message},
        {"role": "assistant", "content": ""},
    ]

    try:
        stream = client.chat.completions.create(**kwargs)
        for chunk in stream:
            delta = chunk.choices[0].delta if chunk.choices else None
            if not delta:
                continue
            content = delta.content
            if content is None:
                continue
            partial += content
            new_history[-1]["content"] = partial
            yield new_history, "", midi_files, new_undo_stack, None, gr.update()
    except Exception as e:
        new_history[-1]["content"] = f"⚠ AI 调用失败: {e}"
        yield new_history, "", midi_files, new_undo_stack, None, gr.update()
        return

    # 解析 AI 回复中的操作标记
    create_tags = _parse_create_midi_tags(partial)
    delete_tags = _parse_delete_midi_tags(partial)

    download_path = None
    updated_files = list(midi_files)

    if create_tags or delete_tags:
        # 执行创建操作（只执行一次）
        if create_tags:
            updated_files, create_display = _execute_create_midi(create_tags, updated_files)
            if not download_path:
                download_path = os.path.join(str(config.OUTPUT_DIR), create_tags[0]["filename"])

        # 执行删除操作（只执行一次）
        if delete_tags:
            updated_files, delete_display = _execute_delete_midi(delete_tags, updated_files)
        else:
            delete_display = ""

        # 保留原始 <midi_tool> 标记（Gradio reasoning_tags 自动提取为折叠区块）
        # 在消息末尾追加执行结果摘要
        result_parts = []
        if create_tags:
            result_parts.append(create_display)
        if delete_tags:
            result_parts.append(delete_display)

        if result_parts:
            partial += "\n\n---\n" + "\n\n".join(result_parts)

        new_history[-1]["content"] = partial

        yield (
            new_history,
            "",
            updated_files,
            new_undo_stack,
            download_path if download_path else None,
            gr.update(choices=_get_choices(updated_files), value=[]),
        )
    else:
        yield new_history, "", midi_files, new_undo_stack, None, gr.update()


# ==================== Gradio UI ====================

def build_chat_ui() -> gr.Blocks:
    """构建多轮对话界面。"""
    with gr.Blocks(title="AI_MIDI · 多轮对话") as app:
        gr.Markdown("# AI_MIDI · 多轮对话")
        gr.Markdown("解析 MIDI → 多轮对话 → AI 处理 → 输出结果")

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
                    reasoning_tags=[("<midi_tool>", "</midi_tool>")],
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

        # ---- 对话事件（流式 Generator） ----
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
