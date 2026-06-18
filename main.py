"""AI_MIDI 入口。

交互式菜单:解析输入 MIDI → 选择功能 → 调用 DeepSeek →
把结果写为 MIDI(note_table)或纯文本。
"""
import os

import ai_api
import get
import out
import config


def _prompt_bpm() -> str:
    return input(f"请输入 BPM (默认 {config.DEFAULT_BPM}): ").strip() or str(config.DEFAULT_BPM)


def _prompt_time_signature() -> str:
    return (input(f"请输入拍号 (默认 {config.DEFAULT_TIME_SIGNATURE}): ").strip()
            or config.DEFAULT_TIME_SIGNATURE)


def _save_text(filename: str, content: str) -> None:
    """把文本结果写入 output/ 目录(目录不存在则自动创建)。"""
    os.makedirs(config.OUTPUT_DIR, exist_ok=True)
    path = config.OUTPUT_DIR / filename
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)
    print(f"结果已保存到 {path}")


def main() -> None:
    # 1. 从 MIDI 文件解析音符数据。
    print("正在解析 MIDI 文件...")
    note_table = get.get_note()

    # 2. 选择功能。
    choice = input("请选择功能：1-添加和弦 2-翻译歌词 3-设计转音 4-其他要求 (输入数字): ").strip()

    if choice == "1":
        requirements = input("请输入添加和弦的具体要求: ")
        bpm = _prompt_bpm()
        time_signature = _prompt_time_signature()
        result = ai_api.add_chord(note_table, bpm, time_signature, requirements)
        if result:
            out.out_note(result, bpm)

    elif choice == "2":
        lyrics = input("请输入歌词数据: ")
        bpm = _prompt_bpm()
        time_signature = _prompt_time_signature()
        original_language = input("请输入原语言: ")
        target_language = input("请输入目标语言: ")
        result = ai_api.translate_lyrics(note_table, lyrics, bpm, time_signature,
                                         original_language, target_language)
        if result:
            _save_text("translated_lyrics.txt", result)

    elif choice == "3":
        lyrics = input("请输入歌词数据: ")
        bpm = _prompt_bpm()
        time_signature = _prompt_time_signature()
        requirements = input("请输入设计转音的具体要求: ")
        result = ai_api.design_melisma(note_table, lyrics, bpm, time_signature, requirements)
        if result:
            out.out_note(result, bpm)

    elif choice == "4":
        note_output = input("是否需要输出音符数据？(y/n): ").strip().lower() == "y"
        lyrics = input("请输入歌词数据: ")
        bpm = _prompt_bpm()
        time_signature = _prompt_time_signature()
        requirements = input("请输入其他要求的具体内容: ")
        result = ai_api.other_requirements(note_table, lyrics, bpm, time_signature,
                                           requirements, note_output)
        if not result:
            return
        if not note_output:
            _save_text("other_requirements_result.txt", result)
        else:
            out.out_note(result, bpm)

    else:
        print("无效的选择，请重新运行程序并输入正确的数字。")


if __name__ == "__main__":
    main()
