# -*- coding: utf-8 -*-
"""
AI_MIDI-go 宣传视频 · 配乐离线渲染器 v2
========================================
新特性：
  - ADSR + 释放尾音：音符结束后自然衰减收尾，不再戛然而止
  - FFT 卷积混响：合成立体声 IR，音乐湿声可调，空间感完整
  - 丰富鼓组：有力 kick、带腔体与混响尾的 snare、ghost/ride/fill/crash
  - FX：riser 上扫（接全奏）、impact 冲击
  - 垫乐 bed_loop：与演示配乐同源引擎渲染，供片头/过场/片尾使用

输出 48kHz 立体声 WAV 到 promo/audio/
"""
import os
import sys
import wave

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import compose_music as C  # noqa: E402

SR = 48000
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "audio")
rng = np.random.default_rng(20260825)

SPB = C.SECONDS_PER_BEAT


def t_axis(dur):
    return np.arange(max(1, int(SR * dur))) / SR


# ═══════════════════════════════════════════════════════════
# 包络：保持期自然衰减 + note-off 后指数释放（尾音关键）
# ═══════════════════════════════════════════════════════════
def adsr_env(hold, release, attack=0.006, k_hold=2.0, k_rel=4.0, sustain=1.0):
    """返回长度 hold+release 的包络。hold 内 exp 衰减到 sustain 比例，
    gate 结束后从当前电平按 k_rel 指数释放——无硬切。"""
    n_hold = max(int(SR * hold), 1)
    n_rel = max(int(SR * release), 1)
    th = np.arange(n_hold) / SR
    env_hold = (np.exp(-k_hold * th) * (1 - sustain) + sustain)
    at = max(int(attack * SR), 1)
    env_hold[:at] *= np.linspace(0, 1, at)
    tr = np.arange(n_rel) / SR
    tail_level = env_hold[-1]
    env_rel = tail_level * np.exp(-k_rel * tr)
    return np.concatenate([env_hold, env_rel])


def mono_to_stereo(sig, pan=0.0):
    gl = np.sqrt(1 - max(0.0, pan))
    gr = np.sqrt(1 + min(0.0, pan))
    return np.stack([sig * gl, sig * gr], axis=1)


# ═══════════════════════════════════════════════════════════
# 乐器（全部带释放尾音）
# ═══════════════════════════════════════════════════════════
def rhodes(midi, dur_beats, vel, soft=False):
    f = 440.0 * 2 ** ((midi - 69) / 12)
    hold = dur_beats * SPB
    total = hold + 1.1
    t = t_axis(total)
    vib = 1 + 0.0025 * np.sin(2 * np.pi * 4.7 * t)
    ph = 2 * np.pi * np.cumsum(f * vib) / SR
    if soft:
        # 垫乐专用：起音更缓、高次谐波更收敛，听感更软
        sig = (np.sin(ph) +
               np.sin(ph * 2.003) * 0.30 * np.exp(-t * 3.6) +
               np.sin(ph * 4.01) * 0.08 * np.exp(-t * 6.0))
        amp = (vel / 127.0) ** 1.5
        env = adsr_env(hold, 1.1, attack=0.014, k_hold=1.6, k_rel=3.2, sustain=0.62)
        return sig * env * amp * 0.5
    sig = (np.sin(ph) +
           np.sin(ph * 2.003) * 0.42 * np.exp(-t * 4.5) +
           np.sin(ph * 4.01) * 0.15 * np.exp(-t * 7.5) +
           np.sin(ph * 6.99) * 0.06 * np.exp(-t * 10.0))
    amp = (vel / 127.0) ** 1.35
    env = adsr_env(hold, 1.1, attack=0.005, k_hold=1.9, k_rel=3.6, sustain=0.55)
    return sig * env * amp * 0.5


def bass_note(midi, dur_beats, vel):
    f = 440.0 * 2 ** ((midi - 69) / 12)
    hold = dur_beats * SPB
    total = hold + 0.7
    t = t_axis(total)
    ph = 2 * np.pi * f * t
    sig = np.sin(ph) + 0.35 * np.sin(ph * 2) + 0.12 * np.sin(ph * 3)
    sig = np.tanh(sig * 1.6) * 0.72
    pluck = rng.normal(0, 1, len(t)) * np.exp(-t * 90) * 0.22
    amp = (vel / 127.0) ** 1.3
    env = adsr_env(hold, 0.7, attack=0.006, k_hold=2.4, k_rel=5.0, sustain=0.62)
    return (sig + pluck) * env * amp * 0.6


def lead_note(midi, dur_beats, vel):
    f = 440.0 * 2 ** ((midi - 69) / 12)
    hold = dur_beats * SPB
    total = hold + 1.3
    t = t_axis(total)
    ph = 2 * np.pi * f * t
    sig = (np.sin(ph) + np.sin(ph * 3) / 3 + np.sin(ph * 5) / 5 + np.sin(ph * 7) / 7)
    amp = (vel / 127.0) ** 1.2
    env = adsr_env(hold, 1.3, attack=0.02, k_hold=1.6, k_rel=3.0, sustain=0.6)
    return sig * env * amp * 0.32


# ─── 鼓组 ───
def kick(vel):
    dur = 0.42
    t = t_axis(dur)
    freq = 150 * np.exp(-t * 30) + 44
    ph = 2 * np.pi * np.cumsum(freq) / SR
    body = np.sin(ph) * np.exp(-t * 8.5)
    click = rng.normal(0, 1, len(t)) * np.exp(-t * 500) * 0.65
    knock = np.sin(2 * np.pi * 1100 * t) * np.exp(-t * 220) * 0.25
    return (body + click + knock) * (vel / 127.0) * 1.0


def snare(vel):
    dur = 0.45 if vel >= 70 else 0.28          # ghost 短促一些
    t = t_axis(dur)
    noise = rng.normal(0, 1, len(t))
    noise = noise - np.convolve(noise, np.ones(10) / 10, "same")
    tone = (np.sin(2 * np.pi * 182 * t) * 0.6 +
            np.sin(2 * np.pi * 238 * t) * 0.35 +
            np.sin(2 * np.pi * 330 * t) * 0.15)
    k = 16 if vel >= 70 else 26
    return (noise * 0.72 + tone * np.exp(-t * 26)) * np.exp(-t * k) * (vel / 127.0) * 0.8


def hat(vel, open_=False):
    dur = 0.34 if open_ else 0.08
    t = t_axis(dur)
    x = rng.normal(0, 1, len(t))
    x = x - np.convolve(x, np.ones(6) / 6, "same")
    x = x - np.convolve(x, np.ones(14) / 14, "same")
    return x * np.exp(-t * (11 if open_ else 52)) * (vel / 127.0) * 0.42


def ride(vel):
    dur = 0.9
    t = t_axis(dur)
    x = rng.normal(0, 1, len(t))
    x = x - np.convolve(x, np.ones(20) / 20, "same")
    ping = np.sin(2 * np.pi * 820 * t) * np.exp(-t * 18) * 0.3
    return (x * 0.5 + ping) * np.exp(-t * 6) * (vel / 127.0) * 0.3


def crash(vel):
    dur = 1.8
    t = t_axis(dur)
    x = rng.normal(0, 1, len(t))
    x = x - np.convolve(x, np.ones(24) / 24, "same")
    shimmer = np.sin(2 * np.pi * 3400 * t) * np.exp(-t * 3) * 0.12
    return (x * 0.75 + shimmer) * np.exp(-t * 2.6) * (vel / 127.0) * 0.5


# ═══════════════════════════════════════════════════════════
# FX：riser 上扫 / impact 冲击
# ═══════════════════════════════════════════════════════════
def fx_riser(dur=2.82):
    """两小节上扫：滤波噪声渐亮 + 正弦音高爬升，末端收紧。"""
    t = t_axis(dur)
    prog = t / dur
    noise = rng.normal(0, 1, len(t))
    for i, win in enumerate((240, 120, 48, 16)):
        lp = np.convolve(noise, np.ones(win) / win, "same")
        w = np.clip(prog * 4 - i, 0, 1)
        noise = noise * (1 - w) + lp * w * 2.0
    sweep_f = 180 * 2 ** (prog * 2.6)
    sweep = np.sin(2 * np.pi * np.cumsum(sweep_f) / SR) * 0.4
    env = prog ** 1.7
    sig = (noise * 0.55 + sweep) * env
    return fade_out(sig, 30)


def fx_impact():
    """低频冲击 + 宽噪声爆破，用于全奏正拍。"""
    dur = 1.6
    t = t_axis(dur)
    freq = 130 * np.exp(-t * 14) + 38
    boom = np.sin(2 * np.pi * np.cumsum(freq) / SR) * np.exp(-t * 4.5)
    burst = rng.normal(0, 1, len(t))
    burst = burst - np.convolve(burst, np.ones(30) / 30, "same")
    burst = burst * np.exp(-t * 9) * 0.5
    return (boom + burst) * 0.85


def fade_out(sig, ms=100):
    n = min(int(SR * ms / 1000), len(sig))
    if n > 0:
        sig = sig.copy()
        sig[-n:] *= np.linspace(1, 0, n)
    return sig


# ═══════════════════════════════════════════════════════════
# 混响：FFT 卷积 + 合成立体声 IR
# ═══════════════════════════════════════════════════════════
def make_ir(dur=1.6, decay=3.4, pre_delay=0.02):
    n = int(SR * dur)
    t = np.arange(n) / SR
    ir = rng.normal(0, 1, (n, 2)) * np.exp(-decay * t)[:, None]
    pre = int(pre_delay * SR)
    ir[:pre] *= np.linspace(0, 1, pre)[:, None]
    ir /= np.sqrt((ir ** 2).sum(axis=0, keepdims=True))
    return ir


IR = make_ir()


def reverb(dry_st, wet):
    """dry_st: (n,2)。FFT 快速卷积，逐声道去相关。"""
    n = len(dry_st) + len(IR) - 1
    out = dry_st.copy()
    for ch in range(2):
        S = np.fft.rfft(dry_st[:, ch], n)
        H = np.fft.rfft(IR[:, ch], n)
        wet_sig = np.fft.irfft(S * H, n)[: len(dry_st)]
        out[:, ch] = dry_st[:, ch] * (1 - wet) + wet_sig * wet
    return out


class Bus:
    """干声总线 → 统一加湿"""

    def __init__(self, total_sec):
        self.n = int(SR * total_sec)
        self.buf = np.zeros((self.n, 2))

    def put(self, sig_mono_or_st, at_sec, pan=0.0, gain=1.0):
        at = int(at_sec * SR)
        if at >= self.n:
            return
        st = sig_mono_or_st if sig_mono_or_st.ndim == 2 else mono_to_stereo(sig_mono_or_st, pan)
        seg = st * gain
        end = min(at + len(seg), self.n)
        if end > at:
            self.buf[at:end] += seg[: end - at]

    def render(self, wet):
        return reverb(self.buf, wet)


def save_stereo(name, data, gain=0.88):
    peak = np.max(np.abs(data)) or 1.0
    pcm = (data / peak * gain * 32767).astype(np.int16)
    path = os.path.join(OUT, name)
    with wave.open(path, "wb") as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(SR)
        w.writeframes(pcm.tobytes())
    print("wrote", path, f"{len(data)/SR:.1f}s")


# ═══════════════════════════════════════════════════════════
# 乐曲渲染
# ═══════════════════════════════════════════════════════════
DRUM_TABLE = {36: kick, 38: snare, 42: lambda v: hat(v),
              46: lambda v: hat(v, True), 51: ride, 49: crash}


def render_notes(bus, notes, inst, pan=0.0, gain=1.0):
    for start, dur, midi, vel in notes:
        bus.put(inst(midi, dur, vel), start * SPB, pan, gain)


def render_drums(bus, notes, gain=1.0):
    for start, dur, midi, vel in notes:
        fn = DRUM_TABLE.get(midi)
        if fn:
            bus.put(fn(vel), start * SPB, pan=rng.uniform(-0.05, 0.05), gain=gain)


if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    total_beats = C.N_BARS * 4
    tail = 2.5                       # 给混响尾留空间

    # 1) 和声垫（镜头 03-05 的应用内试听声）
    bus = Bus(total_beats * SPB + tail)
    render_notes(bus, C.build_chords(), rhodes, pan=-0.12, gain=0.92)
    save_stereo("music_chords.wav", bus.render(wet=0.20))

    # 2) 全奏（镜头 06）：和声+贝斯+主旋律+完整鼓组
    bus = Bus(total_beats * SPB + tail)
    render_notes(bus, C.build_chords(), rhodes, pan=-0.12, gain=0.84)
    render_notes(bus, C.build_bass(), bass_note, pan=0.0, gain=0.95)
    render_notes(bus, C.build_melody(), lead_note, pan=0.18, gain=0.9)
    render_drums(bus, C.build_drums(), gain=0.98)
    save_stereo("music_full.wav", bus.render(wet=0.18))

    # 3) 垫乐 loop（片头/过场/片尾）：A 段和声 + A 段鼓组（无旋律），轻快不抢戏
    #    和弦用 soft 音色 + 更低力度，避免"背景和弦太硬"
    bars_bed = len(C.CHORD_BARS)
    bus = Bus(bars_bed * 4 * SPB + tail)
    a_chords = [(s, d, m, v) for (s, d, m, v) in C.build_chords() if s < bars_bed * 4]
    a_drums = [(s, d, m, v) for (s, d, m, v) in C.build_drums() if s < bars_bed * 4]
    light_chords = [(s, d, m, max(28, v - 14)) for (s, d, m, v) in a_chords]
    for (s, d, m, v) in light_chords:
        bus.put(rhodes(m, d, v, soft=True), s * SPB, pan=-0.1, gain=0.66)
    render_notes(bus, [(s, d, m, v) for (s, d, m, v) in C.build_bass() if s < bars_bed * 4],
                 bass_note, pan=0.0, gain=0.62)
    render_drums(bus, a_drums, gain=0.72)
    save_stereo("bed_loop.wav", bus.render(wet=0.24))

    # 4) 打字键盘即兴段
    jam_codes = [("KeyX", 62), ("KeyC", 64), ("KeyV", 65), ("KeyG", 67),
                 ("KeyV", 65), ("KeyC", 64), ("KeyB", 69), ("KeyG", 67),
                 ("KeyV", 65), ("KeyX", 62), ("KeyQ", 72), ("KeyB", 69), ("KeyG", 67)]
    durs = [0.28, 0.21, 0.25, 0.33, 0.21, 0.25, 0.46, 0.25, 0.21, 0.5, 0.33, 0.27, 0.56]
    bus = Bus(7.5)
    t = 0.35
    for (code, midi), d in zip(jam_codes, durs):
        bus.put(rhodes(midi, d + 0.6, 96), t, pan=0.1, gain=0.95)
        t += d + 0.17
    save_stereo("jam_riff.wav", bus.render(wet=0.26))

    # 5) FX：riser（2 小节上扫）与 impact
    save_stereo("fx_riser.wav", mono_to_stereo(fx_riser(2 * 4 * SPB)), gain=0.6)
    save_stereo("fx_impact.wav", mono_to_stereo(fx_impact()), gain=0.66)
