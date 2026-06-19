"""MIDI 写入模块(输出侧)。

把 note_table 文本(来自 AI 回复或文件)转回 MIDI 文件。
支持宽容解析:用一个较宽松的正则从每行提取音符字段,
以应对 LLM 输出偶尔不规整的情况。
"""
import re
import os
import io

import mido
from mido import MidiFile, MidiTrack, Message, MetaMessage

import config

# 音名 → 半音数(支持 # 与 b 升降记号)
NOTES_MAP = {
    'C': 0, 'C#': 1, 'Db': 1,
    'D': 2, 'D#': 3, 'Eb': 3,
    'E': 4, 'Fb': 4,
    'F': 5, 'E#': 5,
    'F#': 6, 'Gb': 6,
    'G': 7, 'G#': 8, 'Ab': 8,
    'A': 9, 'A#': 10, 'Bb': 10,
    'B': 11, 'Cb': 11
}

# 极度宽容的正则:精准匹配数据本身,允许字段间出现各种噪声字符。
_PATTERN = re.compile(
    r'note:\s*[^A-G]*([A-G][#b]?\d+)[^,]*,'
    r'\s*velocity:\s*[^\d]*([\d.]+)[^,]*,'
    r'\s*start:\s*[^\d]*([\d.]+)[^,]*,'
    r'\s*end:\s*[^\d]*([\d.]+)',
    re.IGNORECASE
)


def note_name_to_midi_number(note_name: str) -> int:
    """将音符名称(如 C4、Eb3)转换为 MIDI 编号(0-127)。无法识别时抛出 ValueError。"""
    match = re.match(r'^([A-G][#b]?)', note_name, re.IGNORECASE)
    if not match:
        raise ValueError(f"无法识别的音符名称: {note_name}")

    raw_pitch = match.group(1)
    # 将首字母大写,后面的符号(#或b)小写,保证与字典键一致。
    pitch = raw_pitch[0].upper() + raw_pitch[1:].lower()

    octave = int(note_name[len(pitch):])

    # 规范化等价音名,避免 B#、Cb 等特殊写法导致 KeyError。
    if pitch == 'B#':
        pitch = 'C'
        octave += 1
    elif pitch == 'Cb':
        pitch = 'B'
        octave -= 1
    elif pitch == 'E#':
        pitch = 'F'
    elif pitch == 'Fb':
        pitch = 'E'

    return (octave + 1) * 12 + NOTES_MAP[pitch]


def _parse_lines(lines) -> tuple[list[dict], int]:
    """从可迭代的文本行中解析出音符信息。

    返回 (音符信息列表, 处理过的非空行数)。
    """
    notes_info: list[dict] = []
    line_count = 0

    for line in lines:
        line_count += 1
        clean_line = line.strip()
        if not clean_line:
            continue

        match = _PATTERN.search(clean_line)
        if match:
            note_str, vel_str, start_str, end_str = match.groups()
            notes_info.append({
                'note': note_str,
                'velocity': int(vel_str),
                'start': float(start_str),
                'end': float(end_str)
            })

    return notes_info, line_count


def txt_to_midi(source, output_midi_path=None, bpm=config.DEFAULT_BPM):
    """把 note_table 文本转换为 MIDI 文件。

    - source: 文件路径(字符串/Path)或 note_table 文本内容本身。
    - output_midi_path: 输出 MIDI 路径,默认 config.OUTPUT_MIDI。
    - bpm: 速度,默认 config.DEFAULT_BPM。
    """
    if output_midi_path is None:
        output_midi_path = config.OUTPUT_MIDI

    # 判断输入是文件路径还是内容字符串。
    is_file = os.path.isfile(source)

    if is_file:
        print(f"正在读取文件: {source} ...")
        try:
            with open(source, 'r', encoding='utf-8') as f:
                notes_info, line_count = _parse_lines(f)
        except UnicodeDecodeError:
            print("UTF-8 解码失败，尝试使用 GBK 编码重新读取...")
            try:
                with open(source, 'r', encoding='gbk') as f:
                    notes_info, line_count = _parse_lines(f)
            except Exception as e:
                print(f"读取文件失败: {e}")
                return
        except FileNotFoundError:
            print(f"错误：找不到文件 {source}")
            return
    else:
        print("检测到输入为音符内容字符串，直接解析...")
        notes_info, line_count = _parse_lines(io.StringIO(source))

    if not notes_info:
        print(f"警告：读取了 {line_count} 行，但未找到任何有效的音符数据，未生成MIDI。")
        return

    print(f"成功解析出 {len(notes_info)} 个音符，开始构建MIDI...")

    # 创建 MIDI 文件结构。
    mid = MidiFile(ticks_per_beat=config.TICKS_PER_BEAT)
    track = MidiTrack()
    mid.tracks.append(track)

    track.append(MetaMessage('set_tempo', tempo=mido.bpm2tempo(float(bpm)), time=0))

    # 将音符转换为 MIDI 事件(note_on / note_off 成对),记录绝对 tick。
    events = []
    tpb = mid.ticks_per_beat

    for info in notes_info:
        note_num = note_name_to_midi_number(info['note'])
        vel = info['velocity']

        start_tick = int(info['start'] * tpb)
        end_tick = int(info['end'] * tpb)

        events.append({'type': 'note_on', 'note': note_num, 'velocity': vel, 'abs_tick': start_tick})
        events.append({'type': 'note_off', 'note': note_num, 'velocity': 0, 'abs_tick': end_tick})

    # 同一时刻 note_off 排在 note_on 之前(避免重叠粘连)。
    events.sort(key=lambda x: (x['abs_tick'], x['type'] == 'note_on'))

    # 计算相对时间(delta)并写入轨道。
    last_tick = 0
    for event in events:
        delta_time = event['abs_tick'] - last_tick
        track.append(Message(event['type'], note=event['note'],
                             velocity=event['velocity'], time=delta_time))
        last_tick = event['abs_tick']

    # 保存文件。
    mid.save(str(output_midi_path))
    print(f"✅ 成功生成 MIDI 文件：{output_midi_path}")


def out_note(note_table, bpm, output_path=None):
    """把 AI 返回的 note_table 文本写成 MIDI。供 main.py 调用。"""
    txt_to_midi(note_table, output_path, bpm)


if __name__ == "__main__":
    # 独立测试入口:从 1.txt 解析并生成 output.mid。
    txt_to_midi("1.txt", "output.mid", bpm=90)
