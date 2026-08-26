# -*- coding: utf-8 -*-
"""
生成编曲窗预置编排 preset_arrangement.json
==========================================
四轨（和弦/贝斯/主旋律/鼓组），clips.notes 与 compose_music 乐曲数据同源，
经 showrunner 的准备流程 PUT /api/projects/{id}/arrangement 后，
打开编曲窗即是"多轨编排完成"状态。
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import compose_music as C  # noqa: E402

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                   "..", "frontend", "demo", "assets", "preset_arrangement.json")

TOTAL_BEATS = C.N_BARS * 4


def note_name(m):
    NAMES = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"]
    return NAMES[m % 12] + str(m // 12 - 1)


def make_clip(notes, fname, color_key):
    max_end = max((s + d) for s, d, _, _ in notes)
    length = min(TOTAL_BEATS, int(-(-max_end // 4) * 4))  # 向上取整到小节
    return {
        "id": "clip_promo_" + fname.split(".")[0].lower(),
        "type": "midi",
        "name": fname,
        "fullName": fname,
        "start": 0,
        "length": length,
        "mute": False,
        "fadeIn": 0,
        "fadeOut": 0,
        "notes": [
            {"note": note_name(m), "velocity": int(v),
             "start": round(s, 3), "end": round(s + d, 3)}
            for (s, d, m, v) in notes
        ],
    }


def track(name, color, source, clips):
    return {
        "id": "tr_promo_" + name.lower().replace(" ", "_"),
        "name": name,
        "color": color,
        "mute": False,
        "solo": False,
        "volume": 0.8,
        "source": source,
        "clips": clips,
    }


if __name__ == "__main__":
    state = {
        "version": 1,
        "bpm": C.BPM,
        "snap": 0.25,
        "ppb": 29,
        "loop": {"on": True, "start": 0, "end": TOTAL_BEATS},
        "tracks": [
            track("Lofi Chords", "#00B8CC",
                  {"type": "builtin", "tone": "piano", "label": "内置 · 温暖钢琴"},
                  [make_clip(C.build_chords(), "Lofi_Chords.mid", "chords")]),
            track("Lofi Bass", "#FF8C00",
                  {"type": "synth", "wave": "triangle", "label": "合成器 · 三角波"},
                  [make_clip(C.build_bass(), "Demo_BassLine.mid", "bass")]),
            track("Lead Melody", "#5B8DEF",
                  {"type": "synth", "wave": "square", "label": "合成器 · 方波"},
                  [make_clip(C.build_melody(), "Demo_Melody.mid", "melody")]),
            track("Drums", "#06D6A0",
                  {"type": "synth", "wave": "sawtooth", "label": "合成器 · 锯齿波"},
                  [make_clip(C.build_drums(), "Demo_Drums.mid", "drums")]),
        ],
    }
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(state, f, ensure_ascii=False, indent=1)
    n_clips = sum(len(t["clips"][0]["notes"]) for t in state["tracks"])
    print("wrote", OUT, f"| tracks={len(state['tracks'])} notes={n_clips}")
