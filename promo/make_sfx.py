# -*- coding: utf-8 -*-
"""
AI_MIDI-go 宣传视频 SFX / BGM 垱乐合成器
========================================
全部用 numpy 程序化合成，零版权风险。输出 48kHz 16bit WAV 到 promo/sfx/：
  sfx_ding.wav      清脆"叮"（镜头01 转场）
  sfx_chaos.wav     混乱调音杂音（镜头01 钩子）
  sfx_key.wav       单次机械键击（打字段落叠加）
  sfx_whoosh.wav    抽屉滑动风声
  sfx_snip.wav      切片"Snip"
  sfx_shimmer.wav   粒子消散 Shimmer
  sfx_strum.wav     吉他扫弦 "Trrr-ing"
  sfx_pop.wav       UI 泡泡弹出音
  sfx_end.wav       收尾双音 Chime
  （垫乐已迁移至 render_audio.py 的 bed_loop.wav）
"""
import os
import wave

import numpy as np

SR = 48000
OUT_DIR = os.path.join(os.path.dirname(__file__), "sfx")


def save(name, data, gain=0.9):
    data = np.asarray(data, dtype=np.float64)
    peak = np.max(np.abs(data)) or 1.0
    data = data / peak * gain
    pcm = (data * 32767).astype(np.int16)
    path = os.path.join(OUT_DIR, name)
    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(SR)
        w.writeframes(pcm.tobytes())
    print("wrote", path)


def t_axis(dur):
    return np.arange(int(SR * dur)) / SR


def env_exp(dur, k):
    return np.exp(-k * t_axis(dur))


def fade_out(sig, ms=30):
    n = int(SR * ms / 1000)
    if n < len(sig):
        sig[-n:] *= np.linspace(1, 0, n)
    return sig


def sine(freq, dur, k=6.0):
    return np.sin(2 * np.pi * freq * t_axis(dur)) * env_exp(dur, k)


# ---------------------------------------------------------------------------
rng = np.random.default_rng(20260825)


def sfx_ding():
    """清脆双泛音叮：1320Hz + 1980Hz 泛音列。"""
    dur = 1.2
    body = sine(1318.5, dur, k=7) * 0.8
    hi = sine(1975.5, 0.9, k=9) * 0.35
    body[: len(hi)] += hi
    sparkle = sine(2637, 0.4, k=14) * 0.18
    sig = body.copy()
    sig[: len(sparkle)] += sparkle
    save("sfx_ding.wav", fade_out(sig, 200), gain=0.72)


def sfx_chaos():
    """混乱调音杂音：失谐正弦簇 + 噪声扫频，1.6s。"""
    dur = 1.6
    t = t_axis(dur)
    sig = np.zeros_like(t)
    for f in (233.1, 246.9, 466.2, 493.9, 932.3):
        wob = f * (1 + 0.02 * np.sin(2 * np.pi * rng.uniform(3, 9) * t))
        sig += np.sin(2 * np.pi * np.cumsum(wob) / SR) * rng.uniform(0.12, 0.22)
    noise = rng.normal(0, 0.05, len(t))
    sweep = np.sin(2 * np.pi * (600 + 900 * t) * t) * 0.08
    sig = (sig + noise + sweep) * np.linspace(0.25, 1.0, len(t))
    save("sfx_chaos.wav", fade_out(sig, 120), gain=0.55)


def sfx_key():
    """单次机械键击：低频 thock + 高频 click。"""
    dur = 0.07
    thock = sine(rng.uniform(140, 190), dur, k=60) * 0.9
    click = rng.normal(0, 1, int(SR * 0.004)) * 0.5
    sig = thock.copy()
    sig[: len(click)] += click
    save("sfx_key.wav", fade_out(sig, 6), gain=0.62)


def sfx_whoosh():
    """抽屉滑动：带通噪声中心频率上扫，0.55s。"""
    dur = 0.55
    t = t_axis(dur)
    noise = rng.normal(0, 1, len(t))
    # 简易时变带通：两段噪声的交叉淡化模拟上扫
    lp1 = np.convolve(noise, np.ones(220) / 220, "same")
    lp2 = np.convolve(noise, np.ones(28) / 28, "same")
    mix = np.linspace(0, 1, len(t)) ** 1.6
    sig = lp1 * (1 - mix) * 2.2 + lp2 * mix * 1.4
    sig *= np.hanning(len(t))
    save("sfx_whoosh.wav", sig, gain=0.6)


def sfx_snip():
    """切片 Snip：两段高频短促咔嚓。"""
    seg1 = rng.normal(0, 1, int(SR * 0.03)) * np.linspace(1, 0, int(SR * 0.03))
    gap = np.zeros(int(SR * 0.045))
    seg2 = rng.normal(0, 1, int(SR * 0.045)) * np.linspace(1, 0, int(SR * 0.045))
    hp = np.concatenate([seg1, gap, seg2])
    hp = hp - np.convolve(hp, np.ones(12) / 12, "same")   # 高通
    save("sfx_snip.wav", hp * 1.6, gain=0.58)


def sfx_shimmer():
    """粒子消散 Shimmer：上行铃音簇 + 亮噪闪。"""
    parts = []
    total = int(SR * 1.1)
    buf = np.zeros(total)
    for i, f in enumerate((1567.98, 2093, 2637, 3135.96, 4186)):
        at = int(SR * (0.05 + i * 0.09))
        tone = sine(f, 0.7 - i * 0.06, k=8) * (0.5 - i * 0.06)
        end = min(at + len(tone), total)
        buf[at:end] += tone[: end - at]
    spark = rng.normal(0, 1, total) * env_exp(1.1, 6) * 0.05
    spark = spark - np.convolve(spark, np.ones(6) / 6, "same")
    buf += spark
    save("sfx_shimmer.wav", fade_out(buf, 250), gain=0.5)


def sfx_strum():
    """扫弦 Trrr-ing：6 根琴弦按 12ms 间隔依次拨响（六弦分解）。"""
    strings = [(82.41, 0.5), (110, 0.52), (146.83, 0.55),
               (220, 0.6), (329.63, 0.62), (440, 0.65)]
    total = int(SR * 1.5)
    buf = np.zeros(total)
    for i, (f, amp) in enumerate(strings):
        at = int(SR * i * 0.012)
        dur = 1.3 - i * 0.08
        pluck = (sine(f, dur, k=5) + 0.4 * sine(f * 2, dur, k=8) +
                 0.2 * sine(f * 3.01, dur, k=10)) * amp
        end = min(at + len(pluck), total)
        buf[at:end] += pluck[: end - at]
    save("sfx_strum.wav", fade_out(buf, 300), gain=0.68)


def sfx_pop():
    """UI 泡泡弹出：短促上滑正弦。"""
    dur = 0.16
    t = t_axis(dur)
    freq = 420 + 900 * (t / dur) ** 2
    sig = np.sin(2 * np.pi * np.cumsum(freq) / SR) * env_exp(dur, 26)
    save("sfx_pop.wav", fade_out(sig, 20), gain=0.5)


def sfx_end():
    """收尾 Chime：G5 -> C6 双音（柔和正弦 + 五度泛音）。"""
    total = int(SR * 2.0)
    buf = np.zeros(total)

    def add(freq, at_s, amp):
        a = sine(freq, 1.6, k=4.5)
        b = sine(freq * 2, 1.3, k=6) * 0.35
        if len(b) < len(a):
            b = np.pad(b, (0, len(a) - len(b)))
        tone = (a + b) * amp
        at = int(SR * at_s)
        end = min(at + len(tone), total)
        buf[at:end] += tone[: end - at]

    add(783.99, 0.0, 0.8)     # G5
    add(1046.5, 0.18, 0.95)   # C6
    save("sfx_end.wav", fade_out(buf, 500), gain=0.66)




if __name__ == "__main__":
    os.makedirs(OUT_DIR, exist_ok=True)
    sfx_chaos()
    sfx_ding()
    sfx_key()
    sfx_whoosh()
    sfx_snip()
    sfx_shimmer()
    sfx_strum()
    sfx_pop()
    sfx_end()
