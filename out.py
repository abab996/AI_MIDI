"""MIDI 写入模块(输出侧)。

把 note_table 文本(来自 AI 回复或文件)转回 MIDI 文件。
支持宽容解析:用一个较宽松的正则从每行提取音符字段,
以应对 LLM 输出偶尔不规整的情况。
"""
import logging
import re
import os
import io

import mido
from mido import MidiFile, MidiTrack, Message, MetaMessage

import config

logger = logging.getLogger("ai_midi")


class EmptyNoteTableError(ValueError):
    """当 AI 回复中未解析出任何音符数据时抛出。"""

    def __init__(self, message: str = "未从 AI 回复中解析出任何音符") -> None:
        super().__init__(message)

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

def _extract_fields(line: str) -> tuple[str, str, str, str] | None:
    note_match = re.search(r'note:\s*[^A-G]*([A-G][#b]*-?\d+)', line, re.IGNORECASE)
    vel_match = re.search(r'velocity:\s*[^\d.-]*([-\d.]+)', line, re.IGNORECASE)
    start_match = re.search(r'start:\s*[^\d.-]*([-\d.]+)', line, re.IGNORECASE)
    end_match = re.search(r'end:\s*[^\d.-]*([-\d.]+)', line, re.IGNORECASE)
    if note_match and vel_match and start_match and end_match:
        return note_match.group(1), vel_match.group(1), start_match.group(1), end_match.group(1)
    return None


def _clamp_velocity(vel) -> int:
    """将力度值限制在 MIDI 合法范围 0-127。"""
    v = int(float(vel))
    if not (config.MIN_VELOCITY <= v <= config.MAX_VELOCITY):
        logger.warning("velocity %s 超出范围,已钳制到 0-127", vel)
    return max(config.MIN_VELOCITY, min(config.MAX_VELOCITY, v))


def _clamp_bpm(bpm) -> int:
    """将 BPM 限制在合理范围 1-600。"""
    b = int(float(bpm))
    if not (config.BPM_MIN <= b <= config.BPM_MAX):
        logger.warning("BPM %s 超出合理范围,已钳制到 1-600", bpm)
    return max(config.BPM_MIN, min(config.BPM_MAX, b))


def _clamp_midi_number(note_num) -> int:
    """将 MIDI 音高编号限制在合法范围 0-127。"""
    n = int(note_num)
    if not (config.NOTE_NUMBER_MIN <= n <= config.NOTE_NUMBER_MAX):
        logger.warning("MIDI 音高 %s 超出范围,已钳制到 0-127", note_num)
    return max(config.NOTE_NUMBER_MIN, min(config.NOTE_NUMBER_MAX, n))


def note_name_to_midi_number(note_name: str) -> int:
    """将音符名称(如 C4、Eb3、C##4)转换为 MIDI 编号(0-127)。无法识别时抛出 ValueError。"""
    match = re.match(r'^([A-G][#b]*)', note_name, re.IGNORECASE)
    if not match:
        raise ValueError(f"无法识别的音符名称: {note_name}")

    raw_pitch = match.group(1)
    # 将首字母大写,后面的符号(#或b)小写,保证与字典键一致。
    pitch = raw_pitch[0].upper() + raw_pitch[1:].lower()

    octave_str = note_name[len(raw_pitch):]
    try:
        octave = int(octave_str)
    except ValueError:
        raise ValueError(
            f"无法识别的音符名称: {note_name}（八度部分 '{octave_str}' 无效）"
        )

    # ---- 计算升降号偏移量（支持多升降号如 C##、Bbb） ----
    accidental_offset = raw_pitch[1:].lower().count('#') - raw_pitch[1:].lower().count('b')

    # 基础音名（去掉升降号）
    base_pitch = pitch[0].upper()
    base_midi = NOTES_MAP[base_pitch]

    # 加上升降号偏移
    midi_num = (octave + 1) * 12 + base_midi + accidental_offset

    return _clamp_midi_number(midi_num)


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

        result = _extract_fields(clean_line)
        if result:
            note_str, vel_str, start_str, end_str = result
            try:
                vel_val = float(vel_str)
                start_val = float(start_str)
                end_val = float(end_str)
            except ValueError:
                logger.warning("第 %d 行数值解析失败，跳过: %s...", line_count, clean_line[:80])
                continue
            if end_val < start_val:
                logger.warning(
                    "第 %d 行 end(%s) < start(%s)，跳过音符 %s",
                    line_count, end_str, start_str, note_str,
                )
                continue
            notes_info.append({
                'note': note_str,
                'velocity': int(vel_val),
                'start': start_val,
                'end': end_val
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

    source_is_pathlike = isinstance(source, os.PathLike)
    source_text = os.fspath(source) if source_is_pathlike else source
    if isinstance(source_text, str):
        looks_like_note_table = _extract_fields(source_text) is not None or "\n" in source_text
        looks_like_path = bool(re.search(r"([A-Za-z]:[\\/]|[\\/]|\.[A-Za-z0-9]+$)", source_text))
        if os.path.isfile(source_text):
            source_is_file = True
        elif source_is_pathlike or (looks_like_path and not looks_like_note_table):
            raise FileNotFoundError(f"找不到文件: {source_text}")
        else:
            source_is_file = False
    else:
        source_is_file = False

    if source_is_file:
        logger.info("正在读取文件: %s", source_text)
        try:
            with open(source_text, 'r', encoding='utf-8') as f:
                notes_info, line_count = _parse_lines(f)
        except UnicodeDecodeError:
            logger.warning("UTF-8 解码失败，尝试使用 GBK 编码重新读取: %s", source_text)
            try:
                with open(source_text, 'r', encoding='gbk') as f:
                    notes_info, line_count = _parse_lines(f)
            except (OSError, UnicodeError):
                logger.exception("GBK 回退读取文件失败: %s", source_text)
                raise
        except FileNotFoundError:
            logger.error("找不到文件: %s", source_text)
            raise
    else:
        logger.info("检测到输入为音符内容字符串，直接解析")
        notes_info, line_count = _parse_lines(io.StringIO(str(source_text)))

    if not notes_info:
        logger.warning("读取了 %d 行，但未找到任何有效的音符数据", line_count)
        raise EmptyNoteTableError("未从 AI 回复中解析出任何音符")

    logger.info("成功解析出 %d 个音符，开始构建 MIDI", len(notes_info))

    # 创建 MIDI 文件结构。
    mid = MidiFile(ticks_per_beat=config.TICKS_PER_BEAT)
    track = MidiTrack()
    mid.tracks.append(track)

    track.append(MetaMessage('set_tempo', tempo=mido.bpm2tempo(_clamp_bpm(bpm)), time=0))

    # 将音符转换为 MIDI 事件(note_on / note_off 成对),记录绝对 tick。
    events = []
    tpb = mid.ticks_per_beat

    for info in notes_info:
        try:
            note_num = note_name_to_midi_number(info['note'])
        except ValueError as e:
            logger.warning("跳过无效音符: %s", e)
            continue
        vel = info['velocity']

        start_tick = max(0, round(info['start'] * tpb))
        end_tick = max(start_tick + 1, round(info['end'] * tpb))

        events.append({'type': 'note_on', 'note': note_num, 'velocity': _clamp_velocity(vel), 'abs_tick': start_tick})
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
    output_parent = os.path.dirname(str(output_midi_path))
    if output_parent:
        os.makedirs(output_parent, exist_ok=True)
    mid.save(str(output_midi_path))
    logger.info("成功生成 MIDI 文件: %s", output_midi_path)


def out_note(note_table, bpm, output_path=None):
    """把 AI 返回的 note_table 文本写成 MIDI。供 main.py 调用。"""
    txt_to_midi(note_table, output_path, bpm)
