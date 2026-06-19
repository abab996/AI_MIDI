"""AI_MIDI Web UI 入口。

基于 Gradio 实现,作为 gui.py 的跨平台替代,可在浏览器中操作。

用法:
    python webui.py
"""
import os
import shutil
import time
from pathlib import Path

import gradio as gr

import ai_api
import config
import get
import out

# ===== 功能常量 =====
FUNC_ADD_CHORD = "配和弦"
FUNC_TRANSLATE = "翻译歌词"
FUNC_MELISMA = "设计转音"
FUNC_OTHER = "其他要求"

_FUNC_CHOICES = [FUNC_ADD_CHORD, FUNC_TRANSLATE, FUNC_MELISMA, FUNC_OTHER]

# 各功能需要哪些字段
_FUNC_FIELDS = {
    FUNC_ADD_CHORD: {"lyrics": False, "lang": False, "note_sw": False, "req": True},
    FUNC_TRANSLATE: {"lyrics": True, "lang": True, "note_sw": False, "req": False},
    FUNC_MELISMA: {"lyrics": True, "lang": False, "note_sw": False, "req": True},
    FUNC_OTHER: {"lyrics": True, "lang": False, "note_sw": True, "req": True},
}


def _note_to_text(note_table: list[str]) -> str:
    """把 note_table 列表合并成字符串。"""
    return "\n".join(note_table)


def _fetch_models(api_key: str, base_url: str) -> tuple[list[str], str]:
    """从 API 服务拉取模型列表,返回 (模型 id 列表, 状态信息)。"""
    key = api_key.strip() or os.environ.get("DEEPSEEK_API_KEY", "")
    url = base_url.strip() or config.BASE_URL

    if not key:
        return [], "⚠ 请先填写 API Key 或设置环境变量 DEEPSEEK_API_KEY"

    try:
        client = ai_api.get_client(api_key=key, base_url=url)
        models = client.models.list()
        ids = sorted([m.id for m in models.data])
        if not ids:
            return [], "⚠ 未获取到任何模型"
        return ids, f"✓ 已获取 {len(ids)} 个模型"
    except Exception as e:  # noqa: BLE001
        return [], f"✗ 获取模型失败: {e}"


def _parse_midi(file_path: str | None) -> tuple[str, list[str]]:
    """解析上传的 MIDI 文件,返回 (状态文本, note_table 列表)。"""
    if not file_path:
        return "请先上传 MIDI 文件。", []

    src = Path(file_path)
    if not src.exists():
        return f"文件不存在: {file_path}", []

    # 同步到默认输入路径,方便下游模块直接读取
    os.makedirs(config.INPUT_MIDI.parent, exist_ok=True)
    shutil.copy2(src, config.INPUT_MIDI)

    try:
        note_table = get.get_note(str(config.INPUT_MIDI), save_to_file=False)
    except Exception as e:  # noqa: BLE001
        return f"解析失败: {e}", []

    if not note_table:
        return "未解析出音符,请检查 MIDI 文件是否有效。", []

    return f"✓ 已解析 {len(note_table)} 个音符", note_table


def _update_func_visibility(func: str) -> tuple[dict, dict, dict, dict]:
    """根据选择的功能返回各字段的 visible 更新。"""
    rows = _FUNC_FIELDS[func]
    return (
        gr.update(visible=rows["lyrics"]),
        gr.update(visible=rows["lang"]),   # 语言行整体
        gr.update(visible=rows["note_sw"]),
        gr.update(visible=rows["req"]),
    )


def _run_task(
    func: str,
    note_table: list[str],
    bpm: str,
    time_signature: str,
    lyrics: str,
    original_language: str,
    target_language: str,
    note_output: bool,
    requirements: str,
    api_key: str,
    base_url: str,
    model: str,
    max_tokens: int | None,
    max_completion_tokens: int | None,
    reasoning_effort: str,
    thinking_enabled: bool,
) -> tuple[str, str | None, str]:
    """调用 AI API 并返回 (结果文本, 可下载文件路径, 状态文本)。"""
    start_time = time.time()

    def _elapsed(msg: str) -> str:
        return f"{msg}（耗时 {time.time() - start_time:.2f} 秒）"

    # 除"其他要求"外,其余功能都需要先解析 MIDI
    if func != FUNC_OTHER and not note_table:
        return "", None, _elapsed("⚠ 请先解析 MIDI 文件。")

    bpm = bpm.strip() or str(config.DEFAULT_BPM)
    time_signature = time_signature.strip() or config.DEFAULT_TIME_SIGNATURE
    note_text = _note_to_text(note_table) if note_table else ""

    # 组装公共 API 参数,空值不传入,让 ai_api 使用默认值
    api_kwargs: dict = {}
    if api_key.strip():
        api_kwargs["api_key"] = api_key.strip()
    if base_url.strip():
        api_kwargs["base_url"] = base_url.strip()
    if model.strip():
        api_kwargs["model"] = model.strip()
    if max_tokens is not None:
        api_kwargs["max_tokens"] = int(max_tokens)
    if max_completion_tokens is not None:
        api_kwargs["max_completion_tokens"] = int(max_completion_tokens)
    if reasoning_effort.strip():
        api_kwargs["reasoning_effort"] = reasoning_effort.strip()
    api_kwargs["thinking_enabled"] = thinking_enabled

    try:
        if func == FUNC_ADD_CHORD:
            result = ai_api.add_chord(
                note_text, bpm, time_signature, requirements, **api_kwargs
            )

        elif func == FUNC_TRANSLATE:
            if not original_language.strip() or not target_language.strip():
                return "", None, _elapsed("⚠ 请填写原语言和目标语言。")
            result = ai_api.translate_lyrics(
                note_text, lyrics, bpm, time_signature,
                original_language, target_language, **api_kwargs
            )

        elif func == FUNC_MELISMA:
            result = ai_api.design_melisma(
                note_text, lyrics, bpm, time_signature, requirements, **api_kwargs
            )

        else:  # FUNC_OTHER
            if not requirements.strip():
                return "", None, _elapsed("⚠ 请填写具体要求。")
            result = ai_api.other_requirements(
                note_text, lyrics, bpm, time_signature,
                requirements, note_output, **api_kwargs
            )
    except Exception as e:  # noqa: BLE001
        return "", None, _elapsed(f"✗ 调用失败: {e}")

    if not result:
        return "", None, _elapsed("✗ AI 未返回内容或调用失败。")

    # 保存结果
    os.makedirs(config.OUTPUT_DIR, exist_ok=True)
    download_path: str | None = None
    status_msg = ""

    try:
        if func == FUNC_TRANSLATE:
            save_path = config.OUTPUT_DIR / "translated_lyrics.txt"
            save_path.write_text(result, encoding="utf-8")
            download_path = str(save_path)
            status_msg = "✓ 歌词已保存"

        elif func == FUNC_OTHER and not note_output:
            save_path = config.OUTPUT_DIR / "other_requirements_result.txt"
            save_path.write_text(result, encoding="utf-8")
            download_path = str(save_path)
            status_msg = "✓ 结果已保存"

        else:
            out.out_note(result, bpm)
            download_path = str(config.OUTPUT_MIDI)
            status_msg = f"✓ MIDI 已生成: {config.OUTPUT_MIDI.name}"

    except Exception as e:  # noqa: BLE001
        return result, None, _elapsed(f"✓ AI 返回结果,但保存失败: {e}")

    return result, download_path, _elapsed(status_msg)


def build_ui() -> gr.Blocks:
    """构建并返回 Gradio 应用。"""
    with gr.Blocks(title="AI_MIDI · AI 编曲助手") as app:
        gr.Markdown("# AI_MIDI · AI 编曲助手")
        gr.Markdown("上传 MIDI → 解析 → 选择任务 → AI 处理 → 下载结果")

        # 运行时状态
        note_table_state = gr.State(value=[])

        with gr.Tabs():
            # ==================== 处理标签页 ====================
            with gr.Tab("处理"):
                with gr.Row():
                    with gr.Column(scale=1):
                        # MIDI 上传与解析
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

                        # 功能选择
                        func_selector = gr.Radio(
                            label="功能",
                            choices=_FUNC_CHOICES,
                            value=FUNC_ADD_CHORD,
                        )

                        # 公共参数
                        bpm_input = gr.Textbox(
                            label="BPM",
                            value=str(config.DEFAULT_BPM),
                        )
                        timesig_input = gr.Textbox(
                            label="拍号",
                            value=config.DEFAULT_TIME_SIGNATURE,
                        )

                        # 歌词
                        lyrics_box = gr.Textbox(
                            label="歌词",
                            lines=3,
                            visible=False,
                        )

                        # 语言(仅翻译歌词)
                        with gr.Row(visible=False) as lang_row:
                            orig_lang_input = gr.Textbox(
                                label="原语言",
                                placeholder="如:日语",
                            )
                            target_lang_input = gr.Textbox(
                                label="目标语言",
                                placeholder="如:中文",
                            )

                        # 输出音符开关(仅其他要求)
                        note_sw = gr.Checkbox(
                            label="输出音符数据 (MIDI)",
                            value=False,
                            visible=False,
                        )

                        # 具体要求
                        req_box = gr.Textbox(
                            label="具体要求",
                            lines=3,
                            visible=True,
                        )

                        start_btn = gr.Button("▶ 开始", variant="primary")
                        status_text = gr.Textbox(
                            label="状态",
                            interactive=False,
                        )

                    with gr.Column(scale=2):
                        result_box = gr.Textbox(
                            label="结果",
                            lines=20,
                            interactive=False,
                        )
                        download_file = gr.File(label="下载结果")

            # ==================== 设置标签页 ====================
            with gr.Tab("设置"):
                gr.Markdown("## API 与模型参数")

                # API Key 与显隐开关
                with gr.Row():
                    api_key_input = gr.Textbox(
                        label="API Key",
                        type="password",
                        placeholder="sk-...",
                        value=os.environ.get("DEEPSEEK_API_KEY", ""),
                        scale=4,
                    )
                    show_key_sw = gr.Checkbox(
                        label="显示 API Key",
                        value=False,
                        scale=1,
                    )

                base_url_input = gr.Textbox(
                    label="Base URL",
                    value=config.BASE_URL,
                )

                # 模型:下拉选择 + 允许手动输入 + 刷新按钮
                with gr.Row():
                    model_input = gr.Dropdown(
                        label="模型",
                        choices=[config.MODEL],
                        value=config.MODEL,
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
                        value=None,
                        precision=0,
                        info="留空则使用 API 默认值",
                    )
                    max_completion_tokens_input = gr.Number(
                        label="最大输出长度 (max_completion_tokens)",
                        value=None,
                        precision=0,
                        info="留空则使用 API 默认值",
                    )
                reasoning_effort_input = gr.Radio(
                    label="推理努力程度 (reasoning_effort)",
                    choices=["low", "medium", "max"],
                    value="max",
                )
                thinking_enabled_input = gr.Checkbox(
                    label="启用 thinking 模式",
                    value=True,
                )

        # 事件绑定
        parse_btn.click(
            fn=_parse_midi,
            inputs=midi_file,
            outputs=[parse_status, note_table_state],
        )

        func_selector.change(
            fn=_update_func_visibility,
            inputs=func_selector,
            outputs=[lyrics_box, lang_row, note_sw, req_box],
        )

        # API Key 显隐切换
        def _toggle_key_visibility(show: bool) -> dict:
            return gr.update(type="text" if show else "password")

        show_key_sw.change(
            fn=_toggle_key_visibility,
            inputs=show_key_sw,
            outputs=api_key_input,
        )

        # 刷新模型列表
        def _refresh_models(api_key: str, base_url: str) -> tuple[dict, str]:
            ids, msg = _fetch_models(api_key, base_url)
            return gr.update(choices=ids), msg

        refresh_model_btn.click(
            fn=_refresh_models,
            inputs=[api_key_input, base_url_input],
            outputs=[model_input, model_status],
        )

        start_btn.click(
            fn=_run_task,
            inputs=[
                func_selector,
                note_table_state,
                bpm_input,
                timesig_input,
                lyrics_box,
                orig_lang_input,
                target_lang_input,
                note_sw,
                req_box,
                api_key_input,
                base_url_input,
                model_input,
                max_tokens_input,
                max_completion_tokens_input,
                reasoning_effort_input,
                thinking_enabled_input,
            ],
            outputs=[result_box, download_file, status_text],
        )

    return app


def main() -> None:
    app = build_ui()
    app.launch(share=False)


if __name__ == "__main__":
    main()
