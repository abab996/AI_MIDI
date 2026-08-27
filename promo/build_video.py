# -*- coding: utf-8 -*-
"""
AI_MIDI-go 宣传视频 · 后期合成器 v5
====================================
结构（31.25 小节 ≈ 87.5s @85 BPM）：
  intro(3) → A 舞台·对话写歌(7) → 过场1(1.25) → B 快速任务(4) → 过场2(1.25) →
  C 编曲窗(5) → 过场3(1.25) → D 卷帘快剪(1+1) → 过场4(1.25) → E 闭环导出(3) → outro(2)
段间 xfade 转场（0.35s，落在节拍线上）；混音：垫乐负责片头/过场/片尾，
应用内音乐按段进出，riser+impact 接编曲窗全奏高潮。
"""
import os
import subprocess
import sys

import numpy as np
import wave
from concurrent.futures import ThreadPoolExecutor, as_completed

FFBIN = os.environ.get(
    "FFBIN",
    os.path.expandvars(r"%LOCALAPPDATA%/Microsoft/WinGet/Packages/Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe/ffmpeg-9.0-full_build/bin"),
)
FFMPEG = os.path.join(FFBIN, "ffmpeg.exe")
SRC = os.environ.get("PROMO_SRC", os.path.join(os.path.dirname(os.path.abspath(__file__)), "out"))
AUDIO_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "audio")
SFX_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "sfx")
OUT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "out")
FONT = "msyhbd.ttc"

SPB = 60.0 / 85.0
BAR = 4 * SPB
XFD = 0.35          # 段间叠化时长

# ---- 剪辑清单：(源, 入点, 段长(拍), 字幕CN, 字幕EN, 转场样式, 字幕框位置(x,y)) ----
# 字幕框 420×96 紧凑尺寸；位置按各段画面逐一定制，确保不遮挡演示内容：
#   A 舞台对话：左上（避开居中对话列 x≥570 与品牌角标 y≤64）
#   B 快速任务：左上（弹窗居中 x≥600，背景已压暗）
#   C 编曲窗：左下（顶部是走带控制条，左下为空白画布区）
#   D 卷帘：右下（音符集中在 17-18 小节左侧，右侧网格为空）
#   E 闭环：左上（薄片在左半 y≥330，聊天列在右侧）
CUTS = [
    ("intro.mp4",      0.0,  3.0,  None, None, "fade", (48, 76)),
    ("stage_chat.mp4", 0.8,  8.0,
     "对话写歌，从一句话开始", "Start with a single prompt", "smoothleft", (48, 76)),
    ("trans1.mp4",     0.0,  1.25, None, None, "fade", (48, 64)),
    ("qt.mp4",         2.4,  6.0,
     "快速任务，结构化下单", "Structured briefs, instant results", "smoothleft", (48, 64)),
    ("trans2.mp4",     0.0,  1.25, None, None, "fade", (48, 64)),
    ("arrange.mp4",    9.8,  5.0,
     "编曲窗，全局尽在掌握", "The arrangement, at a glance", "smoothleft", (48, 930)),
    ("trans3.mp4",     0.0,  1.25, None, None, "fade", (48, 64)),
    ("pr.mp4",         6.0,  1.0,
     "需要时，深入卷帘细修", "Dive deeper when needed", "fade", (1456, 872)),
    ("pr.mp4",         9.9,  1.0,  None, None, "smoothleft", (1456, 872)),
    ("trans4.mp4",     0.0,  1.25, None, None, "fade", (48, 64)),
    ("stage_loop.mp4", 0.5,  3.0,
     "改完发回，成品导出", "Send it back, export done", "fade", (48, 76)),
    ("outro.mp4",      0.0,  2.0,  None, None, None, (48, 76)),
]


def run(cmd):
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.returncode != 0:
        print(r.stderr[-1800:])
        sys.exit(1)


def run_enc(cmd_head, out, preset="medium", crf="14"):
    """视频编码：CPU x264 多线程（高画质）"""
    cmd = cmd_head + ["-c:v", "libx264", "-preset", preset, "-crf", crf,
                      "-threads", "0"] + tail_of(cmd_head, out)
    run(cmd)


def tail_of(cmd_head, out):
    return ["-an", out] if "-an" in cmd_head else ["-c:a", "copy", "-shortest", out]


def cut_segment(idx, src, start, beats, cn, en, pos):
    """切段（多留 XFD 手柄供叠化）+ 烧字幕 + 统一编码（无声视频，60fps）
    字幕框 420×96 紧凑蓝图卡：墨蓝底 + 青框线 + 橙侧条，位置按段定制不遮挡内容"""
    dur = beats * BAR + XFD
    out = os.path.join(OUT_DIR, f"seg{idx}.mp4")
    bx, by = pos
    vf = "fps=60,format=yuv420p"
    if cn:
        vf += (
            f",drawbox=x={bx}:y={by}:w=420:h=96:color=0x1E3A5F@0.90:t=fill"
            f",drawbox=x={bx}:y={by}:w=420:h=96:color=0xE0F7FC@0.45:t=2"
            f",drawbox=x={bx}:y={by}:w=6:h=96:color=0xD35400@1:t=fill"
            f",drawtext=fontfile={FONT}:text='{cn}':fontsize=31:fontcolor=white:x={bx + 26}:y={by + 16}"
            f",drawtext=fontfile={FONT}:text='{en}':fontsize=17:fontcolor=0xE0F7FC@0.85:x={bx + 26}:y={by + 60}"
        )
    head = [FFMPEG, "-y", "-v", "error", "-ss", f"{start:.3f}", "-i", f"{SRC}/{src}",
            "-t", f"{dur:.3f}", "-vf", vf, "-an"]
    run_enc(head, out)
    print("seg", idx, src, f"{start:.2f}s +{dur - XFD:.2f}s", cn or "", pos)
    return beats * BAR


def read_wav(path):
    with wave.open(path, "rb") as w:
        n = w.getnframes()
        ch = w.getnchannels()
        sr = w.getframerate()
        data = np.frombuffer(w.readframes(n), dtype=np.int16).astype(np.float64) / 32767
        if ch == 2:
            return sr, data.reshape(-1, 2)
        return sr, np.stack([data, data], axis=1)


class AudioTimeline:
    def __init__(self, total):
        self.sr = 48000
        self.total = total
        self.buf = np.zeros((int(total * self.sr), 2))

    def place(self, path, at, gain=1.0, fade_in=0.0, fade_out=0.0, src_offset=0.0, dur=0.0):
        sr, data = read_wav(path)
        assert sr == self.sr, f"{path} 采样率 {sr} != 48000"
        if src_offset:
            data = data[int(src_offset * sr):]
        if fade_in:
            k = min(int(fade_in * sr), len(data))
            data[:k] *= np.linspace(0, 1, k)[:, None]
        if fade_out:
            k = min(int(fade_out * sr), len(data))
            data[-k:] *= np.linspace(1, 0, k)[:, None]
        if dur:   # 显式时长上限：音乐严格锁定在本段内，不溢出到下一段
            k = min(int(dur * sr), len(data))
            data = data[:k]
            if fade_out:
                fk = min(int(fade_out * sr), len(data))
                data[-fk:] *= np.linspace(1, 0, fk)[:, None]
        at_i = int(at * self.sr)
        end = min(at_i + len(data), len(self.buf))
        if end > at_i:
            self.buf[at_i:end] += data[: end - at_i] * gain

    def duck(self, at, dur, floor=0.4, fade=0.12):
        sr = self.sr
        a = int(at * sr)
        b = min(int((at + dur) * sr), len(self.buf))
        env = np.ones(b - a)
        fa = min(int(fade * sr), len(env) // 2)
        env[:fa] = np.linspace(1, floor, fa)
        env[-fa:] = np.linspace(floor, 1, fa)
        env[fa:-fa] = floor
        self.buf[a:b] *= env[:, None]

    def save(self, path, peak_gain=0.88):
        peak = np.max(np.abs(self.buf)) or 1.0
        pcm = (self.buf / peak * peak_gain * 32767).astype(np.int16)
        with wave.open(path, "wb") as w:
            w.setnchannels(2)
            w.setsampwidth(2)
            w.setframerate(self.sr)
            w.writeframes(pcm.tobytes())
        print("audio ->", path, f"{self.total:.1f}s")


def main():
    os.makedirs(OUT_DIR, exist_ok=True)

    # 1) 切段（3 路并发编码，吃满多核）
    seg_files = [f"seg{i}.mp4" for i in range(len(CUTS))]
    lens = [0.0] * len(CUTS)
    with ThreadPoolExecutor(max_workers=3) as pool:
        futs = {}
        for i, row in enumerate(CUTS):
            src, start, beats, cn, en = row[0], row[1], row[2], row[3], row[4]
            pos = row[6]
            futs[pool.submit(cut_segment, i, src, start, beats, cn, en, pos)] = i
        for f in as_completed(futs):
            lens[futs[f]] = f.result()
    starts = []
    t = 0.0
    for d in lens:
        starts.append(t)
        t += d
    total = t
    print("total video:", f"{total:.2f}s")

    # 2) 混音（85 BPM：1 拍 = 0.70588s；落点全部吸附节拍）
    tl = AudioTimeline(total)
    BEAT = SPB
    B = lambda n: n * BEAT
    (s_intro, s_A, s_tr1, s_qt, s_tr2, s_arr,
     s_tr3, s_d1, s_d2, s_tr4, s_loop, s_outro) = starts

    # ── 垫乐：全程连续循环、永不断开（从根源消灭段间接缝）。
    #    电平用包络自动化：应用出声时压低（duck），过场/纯视觉段回到全值 ──
    bed = AudioTimeline(total)
    bt = 0.0
    while bt < total - 0.1:
        bed.place(f"{AUDIO_DIR}/bed_loop.wav", bt, gain=1.0, dur=min(B(32), total - bt))
        bt += B(32)
    env_bars = [
        (0, 0.50),      # 片头全值
        (12.25, 0.50),  # B 快速任务：和弦垫先入，垫乐在其下避让（电平不塌陷）
        (13.0, 0.16),
        (18.0, 0.13),
        (18.6, 0.22),   # 过场2：riser 爬升，垫乐略抬
        (19.5, 0.09),   # C 编曲窗：全奏高潮，垫乐让位
        (24.2, 0.09),
        (24.6, 0.30),   # 过场3：无应用音源，垫乐回升避免空洞
        (27.75, 0.30),  # 过场4
        (29, 0.42),     # E 闭环（纯视觉段，垫乐回升）
        (32, 0.42),     # 片尾
        (33.9, 0.0),    # 结尾随 chime 收干净
    ]
    env_t = np.arange(len(bed.buf)) / bed.sr
    env = np.interp(env_t, [b * BAR for b, _ in env_bars], [g for _, g in env_bars])
    bed.buf *= env[:, None]
    tl.buf += bed.buf

    # ── 片头音效 ──
    tl.place(f"{SFX_DIR}/sfx_chaos.wav", B(0.5), gain=0.5)
    tl.place(f"{SFX_DIR}/sfx_ding.wav", B(3), gain=0.55)

    # ── A 段：打字键击 + 发送 whoosh + 结果 pop ──
    for k in range(8):
        tl.place(f"{SFX_DIR}/sfx_key.wav", s_A + B(2) + k * B(0.5), gain=0.46)
    tl.place(f"{SFX_DIR}/sfx_whoosh.wav", s_A + B(8), gain=0.55)
    tl.place(f"{SFX_DIR}/sfx_pop.wav", s_A + B(34), gain=0.5)

    # ── B 快速任务：应用内结果（和弦垫变奏）──
    tl.place(f"{AUDIO_DIR}/music_chords.wav", s_qt, gain=0.38,
             fade_in=0.6, fade_out=0.4, src_offset=B(16), dur=B(24))

    # ── C 编曲窗：riser 接 impact + 全奏高潮（全片最响）──
    tl.place(f"{AUDIO_DIR}/fx_riser.wav", s_tr2, gain=0.5, src_offset=B(3), dur=B(5))
    tl.place(f"{AUDIO_DIR}/fx_impact.wav", s_arr, gain=0.6)
    tl.place(f"{AUDIO_DIR}/music_full.wav", s_arr, gain=0.95,
             fade_in=0.25, fade_out=0.5, src_offset=B(32), dur=B(20))

    # ── D 卷帘快剪：轻和声垫 + 擦除 shimmer ──
    tl.place(f"{AUDIO_DIR}/music_chords.wav", s_d1, gain=0.4,
             fade_in=0.45, fade_out=0.3, src_offset=B(8), dur=B(8))
    tl.place(f"{SFX_DIR}/sfx_shimmer.wav", s_d2 + B(0.5), gain=0.55)
    tl.duck(s_d2 - 0.1, 1.0, floor=0.35)

    # ── E 闭环：导出徽章 pop 点缀（垫乐保持连续）──
    tl.place(f"{SFX_DIR}/sfx_pop.wav", s_loop + B(10), gain=0.5)
    tl.place(f"{SFX_DIR}/sfx_pop.wav", s_loop + B(10.5), gain=0.45)

    # ── 片尾 chime ──
    tl.place(f"{SFX_DIR}/sfx_end.wav", s_outro + B(2), gain=0.6)
    mix_path = os.path.join(OUT_DIR, "mix.wav")
    tl.save(mix_path)

    # 3) xfade 转场链 + 混流（offset 按 ffprobe 实测段长计算，避免舍入漂移越界）
    def probe_dur(p):
        r = subprocess.run([os.path.join(FFBIN, "ffprobe.exe"), "-v", "error",
                            "-show_entries", "format=duration", "-of", "csv=p=0", p],
                           capture_output=True, text=True)
        return float(r.stdout.strip())

    real_lens = [probe_dur(os.path.join(OUT_DIR, s)) for s in seg_files]
    inputs = []
    for s in seg_files:
        inputs += ["-i", os.path.join(OUT_DIR, s)]
    fc = []
    cur_len = real_lens[0]
    prev = "0:v"
    for i in range(1, len(seg_files)):
        style = CUTS[i - 1][5] or "fade"
        off = max(0.0, cur_len - XFD)
        outv = f"v{i}"
        fc.append(f"[{prev}][{i}:v]xfade=transition={style}:duration={XFD}:offset={off:.3f}[{outv}]")
        prev = outv
        cur_len = off + real_lens[i]
    fc.append(f"[{prev}]format=yuv420p[vout]")
    silent = os.path.join(OUT_DIR, "video_all.mp4")
    run_enc([FFMPEG, "-y", "-v", "error"] + inputs +
            ["-filter_complex", ";".join(fc), "-map", "[vout]", "-an"], silent)
    final = os.path.join(OUT_DIR, "AI_MIDI-go_promo.mp4")
    run([FFMPEG, "-y", "-v", "error", "-i", silent, "-i", mix_path,
         "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", "-shortest", final])
    print("FINAL ->", final, f"{total:.2f}s")

    # 4) 4K 衍生版（独立命令单独跑，见 build_video_4k）
    if os.environ.get("SKIP4K"):
        print("SKIP4K=1 → 跳过 4K 衍生")
        return
    final4k = os.path.join(OUT_DIR, "AI_MIDI-go_promo_4K.mp4")
    run_enc([FFMPEG, "-y", "-v", "error", "-i", final,
             "-vf", "scale=3840:2160:flags=bilinear,format=yuv420p"],
            final4k, preset="veryfast", crf="15")
    print("4K ->", final4k)


if __name__ == "__main__":
    main()
