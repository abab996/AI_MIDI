"""AI_MIDI Web UI 入口。

基于 Gradio 实现,作为 gui.py 的跨平台替代,可在浏览器中操作。

用法:
    python webui.py
"""
import os
import shutil
import tempfile
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
) -> tuple[str, str | None, str]:
    """调用 AI API 并返回 (结果文本, 可下载文件路径, 状态文本)。"""
    if not note_table:
        return "", None, "⚠ 请先解析 MIDI 文件。"

    bpm = bpm.strip() or str(config.DEFAULT_BPM)
    time_signature = time_signature.strip() or config.DEFAULT_TIME_SIGNATURE
    note_text = _note_to_text(note_table)

    try:
        if func == FUNC_ADD_CHORD:
            if not requirements.strip():
                return "", None, "⚠ 请填写配和弦的具体要求。"
            result = ai_api.add_chord(note_text, bpm, time_signature, requirements)

        elif func == FUNC_TRANSLATE:
            if not original_language.strip() or not target_language.strip():
                return "", None, "⚠ 请填写原语言和目标语言。"
            result = ai_api.translate_lyrics(
                note_text, lyrics, bpm, time_signature,
                original_language, target_language,
            )

        elif func == FUNC_MELISMA:
            result = ai_api.design_melisma(
                note_text, lyrics, bpm, time_signature, requirements,
            )

        else:  # FUNC_OTHER
            result = ai_api.other_requirements(
                note_text, lyrics, bpm, time_signature,
                requirements, note_output,
            )
    except Exception as e:  # noqa: BLE001
        return "", None, f"✗ 调用失败: {e}"

    if not result:
        return "", None, "✗ AI 未返回内容或调用失败。"

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
        return result, None, f"✓ AI 返回结果,但保存失败: {e}"

    return result, download_path, status_msg


def build_ui() -> gr.Blocks:
    """构建并返回 Gradio 应用。"""
    with gr.Blocks(title="AI_MIDI · AI 编曲助手") as app:
        gr.Markdown("# AI_MIDI · AI 编曲助手")
        gr.Markdown("上传 MIDI → 解析 → 选择任务 → AI 处理 → 下载结果")

        # 运行时状态
        note_table_state = gr.State(value=[])

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
            ],
            outputs=[result_box, download_file, status_text],
        )

    return app


def main() -> None:
    app = build_ui()
    app.launch(share=False)


if __name__ == "__main__":
    main()
