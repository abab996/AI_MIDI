# -*- coding: utf-8 -*-
"""
AI_MIDI-go 宣传视频配乐生成器
=============================
生成两首 85 BPM、D 多利亚（D Dorian）的 Lo-Fi MIDI：
  1. Lofi_Chords.mid —— 仅电钢和弦垫（镜头 02-05 的主角文件）
  2. Lofi_Full.mid   —— 和弦 + 贝斯 + 主旋律 + 鼓组（镜头 06 全奏高潮）

输出：frontend/demo/assets/*.mid（由前端静态服务直接供给演示导播上传）
"""
import os
import struct

TICKS_PER_BEAT = 480
BPM = 85
SECONDS_PER_BEAT = 60.0 / BPM
US_PER_BEAT = int(60000000 / BPM)

# ---------------------------------------------------------------------------
# 音名工具
# ---------------------------------------------------------------------------
NOTE_MAP = {"C": 0, "C#": 1, "Db": 1, "D": 2, "D#": 3, "Eb": 3, "E": 4, "F": 5,
            "F#": 6, "Gb": 6, "G": 7, "G#": 8, "Ab": 8, "A": 9, "A#": 10,
            "Bb": 10, "B": 11}


def n(name):
    """'D4' -> MIDI 编号"""
    pitch = NOTE_MAP[name[:-1]]
    octave = int(name[-1])
    return (octave + 1) * 12 + pitch


# ---------------------------------------------------------------------------
# 和声设计：D 多利亚 Lo-Fi 循环（每小节一个和弦，8 小节和声循环 × 2 段）
# ---------------------------------------------------------------------------
CHORD_BARS = [
    # (和弦名, 电钢声位 MIDI 号列表)
    ("Dm9",    [n("D4"), n("F4"), n("A4"), n("C5")]),
    ("G13",    [n("F4"), n("A4"), n("B4"), n("E5")]),
    ("Dm9",    [n("D4"), n("F4"), n("A4"), n("E5")]),
    ("G13",    [n("F4"), n("A4"), n("B4"), n("E5")]),
    ("Dm9",    [n("D4"), n("F4"), n("A4"), n("C5")]),
    ("G13",    [n("F4"), n("A4"), n("B4"), n("E5")]),
    ("Fmaj7",  [n("A3"), n("C4"), n("E4"), n("F4")]),
    ("A7sus",  [n("A3"), n("D4"), n("E4"), n("G4")]),
]
N_BARS = len(CHORD_BARS) * 2          # 共 16 小节
ROOTS = {"Dm9": "D2", "G13": "G1", "Fmaj7": "F2", "A7sus": "A1"}

SWING = 0.06                           # 反拍八分音符延后量（拍）


def swing_pos(beat):
    """落在半拍上的音符按 Lo-Fi 摇摆延后。"""
    frac = beat - int(beat)
    if abs(frac - 0.5) < 1e-6:
        return beat + SWING
    return beat


# ---------------------------------------------------------------------------
# 各轨音符数据：[(start_beat, dur_beat, midi_no, velocity)]
# ---------------------------------------------------------------------------
def build_chords():
    """电钢和弦：慵懒切分 comping，力度随小节呼吸。"""
    out = []
    for bar in range(N_BARS):
        name, voicing = CHORD_BARS[bar % len(CHORD_BARS)]
        base = bar * 4
        breath = 66 if bar % 4 == 0 else 58        # 每个和声循环起点略强调
        hits = [
            (0.0, 2.4, breath),
            (2.5, 0.85, breath - 12),
            (3.5, 0.45, breath - 18),
        ]
        if bar % 2 == 1:                            # 偶数小节补一处抢拍
            hits.append((1.5, 0.4, breath - 20))
        for off, dur, vel in hits:
            t = swing_pos(base + off)
            for i, m in enumerate(voicing):
                v = max(30, vel - i * 3)            # 上方音略轻
                out.append((round(t, 3), dur, m, v))
    return out


def build_bass():
    """贝斯：A 段长音铺底；B 段加入八度弹跳与走步线。"""
    out = []
    for bar in range(N_BARS):
        name, _ = CHORD_BARS[bar % len(CHORD_BARS)]
        root = n(ROOTS[name])
        base = bar * 4
        second_round = bar >= len(CHORD_BARS)
        if not second_round:
            out.append((base + 0.0, 2.6, root, 78))
            out.append((base + 3.0, 0.9, root + 12, 64))   # 八度回应
        else:
            pat = [(0.0, 0.9, 84), (1.0, 0.4, 62), (1.5, 0.4, 66),
                   (2.0, 0.9, 80), (3.0, 0.4, 68), (3.5, 0.4, 70)]
            for off, dur, vel in pat:
                m = root + 12 if off in (3.5,) else root
                out.append((swing_pos(base + off), dur, m, vel))
    return out


def build_melody():
    """主旋律：仅 B 段（第 9-16 小节）进入——对应『手绘旋律』叙事。"""
    phrase = [
        # (bar_offset_in_section, start, dur, note, vel)
        (0, 0.5, 0.5, "A4", 82), (0, 1.0, 0.5, "C5", 86), (0, 1.5, 1.4, "D5", 92),
        (1, 0.0, 0.95, "F5", 90), (1, 1.0, 0.45, "E5", 78), (1, 1.5, 0.45, "D5", 74),
        (1, 2.0, 0.9, "C5", 80),
        (2, 0.0, 1.4, "A4", 84), (2, 1.5, 0.45, "G4", 72), (2, 2.0, 0.95, "A4", 78),
        (3, 1.0, 0.45, "G4", 70), (3, 1.5, 0.45, "A4", 74), (3, 2.0, 1.4, "C5", 84),
        (4, 0.0, 0.95, "D5", 88), (4, 1.0, 0.45, "C5", 76), (4, 1.5, 0.45, "A4", 72),
        (4, 2.0, 1.4, "F4", 80),
        (5, 0.0, 0.9, "G4", 78), (5, 1.0, 0.9, "A4", 82), (5, 2.0, 0.45, "C5", 84),
        (5, 2.5, 0.45, "A4", 72), (5, 3.0, 0.9, "G4", 70),
        (6, 0.0, 1.4, "F4", 82), (6, 1.5, 0.45, "E4", 70), (6, 2.0, 1.4, "D4", 78),
        (7, 0.0, 3.4, "D4", 76),
    ]
    sec_base = len(CHORD_BARS) * 4      # B 段起始拍（第 9 小节）
    out = []
    for boff, start, dur, name, vel in phrase:
        t = sec_base + boff * 4 + start
        out.append((round(t, 3), dur, n(name), vel))
    return out


def build_drums():
    """鼓组（GM 打击乐，channel 10）：boom-bap 骨架 + 摇摆 hat
    + B 段 ride/ghost、第 8 小节 fill、第 9 小节 crash、结尾 fill。"""
    KICK, SNARE, HAT, OHAT, RIDE, CRASH = 36, 38, 42, 46, 51, 49
    out = []
    for bar in range(N_BARS):
        base = bar * 4
        second_round = bar >= len(CHORD_BARS)
        is_fill_bar = bar == len(CHORD_BARS) - 1          # 第 8 小节：过渡 fill
        # 底鼓：1 拍 + 3.5 拍抢拍（B 段加密集幽灵踩）
        kicks = [(0.0, 96), (3.5, 74)] + ([(2.5, 60)] if second_round else [])
        for off, vel in kicks:
            out.append((base + off, 0.3, KICK, vel))
        # 军鼓：2、4 拍；B 段加 ghost 弱拍
        for off in (1.0, 3.0):
            out.append((base + off, 0.3, SNARE, 88 if off == 3.0 else 82))
        if second_round and not is_fill_bar:
            out.append((swing_pos(base + 1.75), 0.2, SNARE, 46))
            out.append((swing_pos(base + 3.75), 0.2, SNARE, 50))
        # 闭镲：反拍摇摆八分；B 段叠加 ride 四分
        for e in range(8):
            off = e * 0.5
            vel = 56 if off % 1 == 0 else 40
            out.append((swing_pos(base + off), 0.2, HAT, vel))
        if second_round:
            for beat in range(4):
                out.append((base + beat, 0.3, RIDE, 52 if beat % 2 else 62))
        # 每段末小节加开镲
        if bar % 8 == 7:
            out.append((base + 3.5, 0.4, OHAT, 70))
        # 过渡 fill：第 8 小节后半军鼓十六连
        if is_fill_bar:
            for i in range(4):
                out.append((base + 2.0 + i * 0.25, 0.15, SNARE, 58 + i * 8))
            out.append((base + 3.0, 0.15, SNARE, 78))
            out.append((base + 3.5, 0.15, SNARE, 86))
        # 副歌进入 crash：第 9 小节正拍
        if bar == len(CHORD_BARS):
            out.append((base, 1.0, CRASH, 92))
        # 结尾 fill：最后一小节
        if bar == N_BARS - 1:
            for i in range(4):
                out.append((base + 2.0 + i * 0.25, 0.15, SNARE, 56 + i * 10))
            out.append((base + 3.0, 0.2, KICK, 100))
    return out


# ---------------------------------------------------------------------------
# SMF Type 1 写出
# ---------------------------------------------------------------------------
def vlq(value):
    b = [value & 0x7F]
    value >>= 7
    while value:
        b.append((value & 0x7F) | 0x80)
        value >>= 7
    return bytes(reversed(b))


def track_chunk(events):
    """events: [(tick, priority, bytes)] -> MTrk chunk"""
    events.sort(key=lambda e: (e[0], e[1]))
    data = bytearray()
    last = 0
    for tick, _, payload in events:
        data += vlq(tick - last)
        data += payload
        last = tick
    data += vlq(0) + b"\xff\x2f\x00"
    return b"MTrk" + struct.pack(">I", len(data)) + bytes(data)


def meta(tempo_us=None, marker=None):
    """每个 meta 事件必须自带 delta（此处均为 0），否则解析器失步"""
    ev = []
    if tempo_us is not None:
        ev.append(b"\x00\xff\x51\x03" + tempo_us.to_bytes(3, "big"))
    if marker:
        raw = marker.encode("utf-8")
        ev.append(b"\x00\xff\x06" + vlq(len(raw)) + raw)
    return b"".join(ev)


def note_events(notes, channel, program=None):
    """notes: [(start_beat, dur_beat, midi, vel)] -> 排序事件列表"""
    ev = []
    if program is not None:
        ev.append((0, 0, bytes([0xC0 | channel, program])))
    for start, dur, m, vel in notes:
        t0 = int(round(start * TICKS_PER_BEAT))
        t1 = int(round((start + dur) * TICKS_PER_BEAT))
        ev.append((t0, 1, bytes([0x90 | channel, m, max(1, min(127, vel))])))
        ev.append((t1, 0, bytes([0x80 | channel, m, 64])))
    return ev


def write_smf(path, tracks):
    """tracks: [events...] 第一轨自动带速度 meta"""
    header = (b"MThd" + struct.pack(">IHHH", 6, 1, len(tracks), TICKS_PER_BEAT))
    with open(path, "wb") as f:
        f.write(header)
        for tr in tracks:
            f.write(tr)


def make_file(path, with_melody=False, with_drums=False):
    conv = track_chunk([(0, 0, meta(US_PER_BEAT, "AI_MIDI-go Demo"))])
    chords = track_chunk(note_events(build_chords(), 0, program=4))     # 4 = Electric Piano
    tracks = [conv, chords]
    if with_melody:
        tracks.append(track_chunk(note_events(build_melody(), 2, program=81)))   # 81 = Lead Square（合成主音）
    if with_drums:
        tracks.append(track_chunk(note_events(build_drums(), 9)))
    if with_melody:
        tracks.append(track_chunk(note_events(build_bass(), 3, program=33)))     # 33 = Finger Bass
    write_smf(path, tracks)
    print("wrote", path)


def build_ghost_bass():
    """幽灵参考轨专用：8 小节贝斯线（供镜头 05 跨轨对位展示）。"""
    out = []
    for bar in range(8):
        name, _ = CHORD_BARS[bar % len(CHORD_BARS)]
        root = n(ROOTS[name])
        base = bar * 4
        pat = [(0.0, 1.4, 80), (1.5, 0.4, 60), (2.0, 0.9, 76),
               (3.0, 0.45, 64), (3.5, 0.45, 68)]
        for off, dur, vel in pat:
            out.append((swing_pos(base + off), dur, root, vel))
    return out


if __name__ == "__main__":
    out_dir = os.path.join(os.path.dirname(__file__), "..", "frontend", "demo", "assets")
    os.makedirs(out_dir, exist_ok=True)
    make_file(os.path.join(out_dir, "Lofi_Chords.mid"))
    make_file(os.path.join(out_dir, "Lofi_Full.mid"), with_melody=True, with_drums=True)
    ghost = track_chunk([(0, 0, meta(US_PER_BEAT, "Ghost Bass"))])
    ghost_notes = track_chunk(note_events(build_ghost_bass(), 3, program=33))
    write_smf(os.path.join(out_dir, "Demo_Bass.mid"), [ghost, ghost_notes])
    print("wrote", os.path.join(out_dir, "Demo_Bass.mid"))
    # 编排窗演示用分轨
    conv = track_chunk([(0, 0, meta(US_PER_BEAT, "Stems"))])
    stems = {
        "Demo_Melody.mid": note_events(build_melody(), 2, program=81),
        "Demo_Drums.mid": note_events(build_drums(), 9),
        "Demo_BassLine.mid": note_events(build_bass(), 3, program=33),
    }
    for name, ev in stems.items():
        write_smf(os.path.join(out_dir, name), [conv, track_chunk(ev)])
        print("wrote", os.path.join(out_dir, name))
