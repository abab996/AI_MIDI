"""MIDI 解析模块(输入侧)。

读取 MIDI 文件,把其中的音符事件转换成自定义的 note_table 文本格式:
    [note: "C4", velocity: "80", start: "1", end: "2"]
"""
import os
from pathlib import Path

import mido
from mido import MidiFile

import config

# note_table 每个音符的输出格式模板。
_NOTE_FORMAT = '[note: "{note}", velocity: "{velocity}", start: "{start}", end: "{end}"]'


def midi_number_to_note_name(midi_note: int) -> str:
    """将 MIDI 音高编号(0-127)转换为音符名称,如 C4。无效输入返回 'Invalid'。"""
    if midi_note < 0 or midi_note > 127:
        return "Invalid"

    notes = ['C', 'C#', 'D', 'D#', 'E', 'F', 'F#', 'G', 'G#', 'A', 'A#', 'B']
    octave = (midi_note // 12) - 1
    note = notes[midi_note % 12]
    return f"{note}{octave}"


def parse_midi_to_custom_format(file_path) -> list[str]:
    """解析 MIDI 文件,返回 note_table 格式字符串列表。

    合并所有轨道,通过 note_on/note_off 事件追踪每个音符的起止时间,
    把 tick 换算成拍(ticks_per_beat),按起始时间排序后输出。
    """
    try:
        mid = MidiFile(str(file_path))
    except Exception as e:
        print(f"无法读取MIDI文件: {e}")
        return []

    tpb = mid.ticks_per_beat
    merged_track = mido.merge_tracks(mid.tracks)

    active_notes: dict[int, dict] = {}
    parsed_notes: list[dict] = []

    current_tick = 0

    for msg in merged_track:
        current_tick += msg.time

        if msg.type == 'note_on' and msg.velocity > 0:
            active_notes[msg.note] = {
                'start_tick': current_tick,
                'velocity': msg.velocity
            }

        elif msg.type == 'note_off' or (msg.type == 'note_on' and msg.velocity == 0):
            if msg.note in active_notes:
                info = active_notes.pop(msg.note)

                start_beat = round(info['start_tick'] / tpb, 2)
                end_beat = round(current_tick / tpb, 2)

                parsed_notes.append({
                    'note': midi_number_to_note_name(msg.note),
                    'velocity': info['velocity'],
                    'start': start_beat,
                    'end': end_beat
                })

    parsed_notes.sort(key=lambda x: x['start'])

    return [
        _NOTE_FORMAT.format(**n) for n in parsed_notes
    ]


def get_note(file_path=None, save_to_file: bool = True) -> list[str]:
    """解析输入 MIDI,返回 note_table 字符串列表。

    - file_path: MIDI 文件路径,默认 config.INPUT_MIDI(./input/in.mid)。
    - save_to_file: 是否把结果写入 config.DOING_OUTPUT_TXT。
    """
    if file_path is None:
        file_path = config.INPUT_MIDI

    print(f"正在解析MIDI文件: {file_path} ...")
    output_list = parse_midi_to_custom_format(file_path)

    print("\n--- 解析结果 ---")
    for item in output_list:
        print(item)

    print(f"\n总共解析出 {len(output_list)} 个音符。")

    if save_to_file and output_list:
        os.makedirs(config.DOING_DIR, exist_ok=True)
        with open(config.DOING_OUTPUT_TXT, "w", encoding="utf-8") as f:
            for item in output_list:
                f.write(item + "\n")
        print(f"\n结果已保存到 {config.DOING_OUTPUT_TXT}。")

    return output_list


if __name__ == "__main__":
    # 独立测试入口:解析默认输入 MIDI 并打印/保存结果。
    get_note()
