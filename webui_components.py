"""Gradio UI 组件构建函数。

从 webui.py 的 build_ui() 中提取的组件构建逻辑,
每个函数负责构建一个 UI 区域并返回创建的组件,
供 build_ui() 作为薄编排层调用。
"""
from __future__ import annotations

import gradio as gr

import config

# ===== 功能常量 =====
FUNC_ADD_CHORD = "配和弦"
FUNC_TRANSLATE = "翻译歌词"
FUNC_MELISMA = "设计转音"
FUNC_OTHER = "其他要求"

_FUNC_CHOICES = [FUNC_ADD_CHORD, FUNC_TRANSLATE, FUNC_MELISMA, FUNC_OTHER]

_FUNC_FIELDS = {
    FUNC_ADD_CHORD: {"lyrics": False, "lang": False, "note_sw": False, "req": True},
    FUNC_TRANSLATE: {"lyrics": True, "lang": True, "note_sw": False, "req": False},
    FUNC_MELISMA: {"lyrics": True, "lang": False, "note_sw": False, "req": True},
    FUNC_OTHER: {"lyrics": True, "lang": False, "note_sw": True, "req": True},
}


def _build_header() -> None:
    """渲染页面标题和描述。"""
    gr.Markdown("# AI_MIDI · AI 编曲助手")
    gr.Markdown("上传 MIDI → 解析 → 选择任务 → AI 处理 → 下载结果")


def _build_upload_section() -> tuple[gr.File, gr.Button, gr.Textbox]:
    """构建 MIDI 上传与解析区域。

    Returns:
        (midi_file, parse_btn, parse_status)
    """
    midi_file = gr.File(
        label="上传 MIDI 文件",
        file_types=[".mid", ".midi"],
    )
    parse_btn = gr.Button("解析 MIDI", variant="primary")
    parse_status = gr.Textbox(
        label="解析状态",
        value="尚未解析",
        interactive=False,
    )
    return midi_file, parse_btn, parse_status


def _build_function_section() -> tuple:
    """构建功能选择与动态输入区域。

    Returns:
        (func_selector, lyrics_box, lang_row, orig_lang_input,
         target_lang_input, note_sw, req_box)
    """
    func_selector = gr.Radio(
        label="功能",
        choices=_FUNC_CHOICES,
        value=FUNC_ADD_CHORD,
    )
    lyrics_box = gr.Textbox(
        label="歌词",
        lines=3,
        visible=False,
    )
    with gr.Row(visible=False) as lang_row:
        orig_lang_input = gr.Textbox(
            label="原语言",
            placeholder="如: 日语",
            value="",
        )
        target_lang_input = gr.Textbox(
            label="目标语言",
            placeholder="如: 中文",
            value="",
        )
    note_sw = gr.Checkbox(
        label="输出音符数据 (MIDI)",
        value=False,
        visible=False,
    )
    req_box = gr.Textbox(
        label="具体要求",
        lines=3,
        visible=True,
    )
    return (func_selector, lyrics_box, lang_row, orig_lang_input,
            target_lang_input, note_sw, req_box)


def _build_tuning_section() -> tuple[gr.Number, gr.Textbox]:
    """构建 BPM 和拍号输入区域。

    Returns:
        (bpm_input, timesig_input)
    """
    bpm_input = gr.Number(
        label="BPM",
        value=config.DEFAULT_BPM,
        minimum=config.BPM_MIN,
        maximum=config.BPM_MAX,
        step=1,
        precision=0,
    )
    timesig_input = gr.Textbox(
        label="拍号",
        value=config.DEFAULT_TIME_SIGNATURE,
        info="格式: 如 4/4, 3/4, 6/8",
    )
    return bpm_input, timesig_input


def _build_result_section() -> tuple[gr.Textbox, gr.File]:
    """构建结果显示与下载区域 (在右侧 Column 上下文中调用)。

    Returns:
        (result_box, download_file)
    """
    result_box = gr.Textbox(
        label="结果",
        lines=config.RESULT_BOX_LINES,
        max_lines=config.RESULT_BOX_MAX_LINES,
        interactive=False,
    )
    download_file = gr.File(label="下载结果")
    return result_box, download_file


def _build_settings_section(settings: dict) -> tuple:
    """构建设置标签页。

    Args:
        settings: 从 settings.json 加载的用户设置字典。

    Returns:
        (api_key_input, show_key_sw, base_url_input, model_input,
         refresh_model_btn, model_status, max_tokens_input,
         max_completion_tokens_input, reasoning_effort_input,
         thinking_enabled_input, save_cfg_btn, save_cfg_status)
    """
    saved_model = settings.get("model", config.MODEL)

    gr.Markdown("## API 与模型参数")

    with gr.Row():
        api_key_input = gr.Textbox(
            label="API Key",
            type="password",
            placeholder="sk-...",
            value=settings.get("api_key", ""),
            scale=4,
        )
        show_key_sw = gr.Checkbox(
            label="显示 API Key",
            value=False,
            scale=1,
        )

    base_url_input = gr.Textbox(
        label="Base URL",
        value=settings.get("base_url", config.BASE_URL),
    )

    with gr.Row():
        model_input = gr.Dropdown(
            label="模型",
            choices=[saved_model],
            value=saved_model,
            allow_custom_value=True,
            scale=4,
        )
        refresh_model_btn = gr.Button("🔄 刷新", scale=1)

    model_status = gr.Textbox(
        label="模型列表状态",
        interactive=False,
        value="点击刷新按钮从 API 获取模型列表",
    )

    gr.Markdown("## 生成参数")
    with gr.Row():
        max_tokens_input = gr.Number(
            label="最大上下文 (max_tokens)",
            value=settings.get("max_tokens"),
            precision=0,
            info="留空则使用 API 默认值",
        )
        max_completion_tokens_input = gr.Number(
            label="最大输出长度 (max_completion_tokens)",
            value=settings.get("max_completion_tokens"),
            precision=0,
            info="留空则使用 API 默认值",
        )
    reasoning_effort_input = gr.Radio(
        label="推理努力程度 (reasoning_effort)",
        choices=["low", "medium", "max"],
        value=settings.get("reasoning_effort", "max"),
    )
    thinking_enabled_input = gr.Checkbox(
        label="启用 thinking 模式",
        value=settings.get("thinking_enabled", True),
    )

    save_cfg_btn = gr.Button("💾 保存配置", variant="primary")
    save_cfg_status = gr.Textbox(
        label="保存状态",
        interactive=False,
        value="",
    )

    return (api_key_input, show_key_sw, base_url_input, model_input,
            refresh_model_btn, model_status, max_tokens_input,
            max_completion_tokens_input, reasoning_effort_input,
            thinking_enabled_input, save_cfg_btn, save_cfg_status)


def _build_mode_section() -> tuple[gr.Button, gr.Textbox]:
    """构建模式标签页。

    Returns:
        (launch_chat_btn, chat_status)
    """
    gr.Markdown("## 其他工作模式")
    launch_chat_btn = gr.Button("🔄 多轮对话", variant="primary", size="lg")
    chat_status = gr.Textbox(
        label="状态",
        interactive=False,
        value="",
    )
    return launch_chat_btn, chat_status
