/* 编排窗口音频引擎：共享 AudioContext、每轨 gain/analyser 链、
   lookahead 调度器（25ms 轮询 / 300ms 提前量，兜底窗口后台节流）、
   素材解码 LRU 缓存与波形峰值预计算 */
(function (window) {
  "use strict";

  var AudioContext = window.AudioContext || window.webkitAudioContext;

  function ArrangeEngine() {
    this.ctx = null;
    this.masterGain = null;
    this.trackNodes = {};       // trackId -> { gain, analyser, synth, soundfont, sourceKey }
    this.bufferCache = new Map(); // path -> { promise, buffer, peaks, lastUse }
    this.maxCachedBuffers = 48;   // LRU 上限
    this.activeSources = [];      // 播放中的 BufferSource（stopAll 用）

    // 走带状态（由 arrange.js 同步写入）
    this.isPlaying = false;
    this.bpm = 120;
    this.metronome = false;
    this.loop = { on: false, start: 0, end: 16 };

    // 调度器内部状态
    this._timer = null;
    this._intervalMs = 25;
    this._lookahead = 0.30;
    this._anchorCtxTime = 0;  // 播放起点对应的 AudioContext 时间
    this._anchorPos = 0;      // 播放起点的"展开位置"（未回绕拍数）
    this._schedPos = 0;       // 已调度到的展开位置
    this._loopStartPos = 0;   // 播放起点在循环段内的偏移

    // 外部回调（arrange.js 注入）
    this.getTracks = null;    // () => tracks
    this.onLoopWrapped = null;// (timelineBeat) => void
  }

  /* ═══════════ 上下文与轨道链路 ═══════════ */

  ArrangeEngine.prototype.init = function () {
    if (this.ctx) return;
    // 共享单例 AudioContext（与钢琴窗同一时钟基准，减少常驻音频线程）
    this.ctx = (window.SharedAudio && window.SharedAudio.get()) || new AudioContext();
    this.masterGain = this.ctx.createGain();
    this.masterGain.gain.setValueAtTime(0.9, this.ctx.currentTime);
    // 主链限幅器：多轨合成（合成器 + 采样叠加）超过 0dB 时硬削波，
    // 压缩器把峰值压回可听区间
    this.limiter = this.ctx.createDynamicsCompressor();
    this.limiter.threshold.value = -6;
    this.limiter.knee.value = 6;
    this.limiter.ratio.value = 12;
    this.limiter.attack.value = 0.003;
    this.limiter.release.value = 0.12;
    this.masterGain.connect(this.limiter);
    this.limiter.connect(this.ctx.destination);
  };

  ArrangeEngine.prototype.resume = function () {
    this.init();
    if (this.ctx && this.ctx.state === "suspended") return this.ctx.resume();
    return Promise.resolve();
  };

  /** 确保轨道发声链存在；sourceKey 变化时重建发声器 */
  ArrangeEngine.prototype.ensureTrack = function (track) {
    this.init();
    var self = this;
    var nodes = this.trackNodes[track.id];
    var created = false;
    if (!nodes) {
      nodes = {
        gain: this.ctx.createGain(),
        analyser: this.ctx.createAnalyser(),
        synth: null,
        soundfont: null,
        sourceKey: null,
        sfLoading: false
      };
      nodes.analyser.fftSize = 512;
      nodes.gain.connect(nodes.analyser);
      nodes.analyser.connect(this.masterGain);
      this.trackNodes[track.id] = nodes;
      created = true;
    }

    var src = track.source || { type: "synth", wave: "sawtooth" };
    var key;
    if (src.type === "sf2") {
      key = "sf2:" + (src.libId || "") + ":" + (src.presetId || "");
    } else if (src.type === "builtin") {
      key = "builtin:" + (src.tone || "piano");
    } else {
      key = "synth:" + (src.wave || "sawtooth");
    }

    if (nodes.sourceKey !== key) {
      nodes.sourceKey = key;
      if (nodes.synth) { nodes.synth.stopAll(); nodes.synth = null; }
      if (nodes.soundfont) { nodes.soundfont.stopAll(); nodes.soundfont = null; }
      nodes.sfLoading = false;

      if (src.type === "sf2") {
        nodes.soundfont = new window.SoundFontPlayer(this.ctx, nodes.gain);
        nodes.soundfont.init();
        this.loadTrackSoundFont(track, nodes, src);
      } else if (src.type === "builtin") {
        // 内置高品质采样音色（钢琴/弦乐），无需外部文件
        nodes.soundfont = new window.SoundFontPlayer(this.ctx, nodes.gain);
        nodes.soundfont.init();
        nodes.soundfont.setPreset(src.tone || "piano");
      } else {
        nodes.synth = new window.SynthEngine(this.ctx, nodes.gain);
        nodes.synth.init();
        nodes.synth.setWaveform(src.wave || "sawtooth");
      }
    }
    // 新建节点后按当前混音（音量/静音/独奏）设置初始增益
    if (created && this.getTracks) {
      this.applyMix(this.getTracks());
    }
    return nodes;
  };

  /** 从 IndexedDB 音源库异步加载轨道 SF2（不阻塞 UI） */
  ArrangeEngine.prototype.loadTrackSoundFont = function (track, nodes, src) {
    var self = this;
    if (!window.SoundLibrary || !src.libId) return;
    nodes.sfLoading = true;
    window.SoundLibrary.getSoundFont(src.libId).then(function (rec) {
      if (!rec || !rec.data) throw new Error("音源数据不存在");
      var parsed = nodes.soundfont.parseSF2(rec.data);
      if (src.presetId) nodes.soundfont.setPreset(src.presetId);
      nodes.sfLoading = false;
      if (self.onSoundFontLoaded) self.onSoundFontLoaded(track.id, parsed);
    }).catch(function (err) {
      nodes.sfLoading = false;
      console.warn("轨道音源加载失败:", err);
      if (self.onSoundFontError) self.onSoundFontError(track.id, err);
    });
  };

  /** 音量 / 静音 / 独奏求值（任一轨 solo 时其余轨静音） */
  ArrangeEngine.prototype.applyMix = function (tracks) {
    if (!this.ctx) return;
    var anySolo = tracks.some(function (t) { return t.solo && !t.mute; });
    for (var i = 0; i < tracks.length; i++) {
      var t = tracks[i];
      var nodes = this.trackNodes[t.id];
      if (!nodes) continue;
      var audible = !t.mute && (!anySolo || t.solo);
      var vol = audible ? Math.max(0, Math.min(1, t.volume !== undefined ? t.volume : 0.8)) : 0;
      nodes.gain.gain.setTargetAtTime(vol, this.ctx.currentTime, 0.015);
    }
  };

  /** 轨道电平峰值（0~1），电平表 ~20fps 轮询取值 */
  ArrangeEngine.prototype.trackLevel = function (trackId) {
    var nodes = this.trackNodes[trackId];
    if (!nodes || !this.ctx) return 0;
    var arr = nodes._lvlBuf;
    if (!arr) { arr = nodes._lvlBuf = new Float32Array(nodes.analyser.fftSize); }
    nodes.analyser.getFloatTimeDomainData(arr);
    var peak = 0;
    for (var i = 0; i < arr.length; i++) {
      var a = Math.abs(arr[i]);
      if (a > peak) peak = a;
    }
    return Math.min(1, peak);
  };

  /* ═══════════ 素材解码缓存（LRU）与波形峰值 ═══════════ */

  ArrangeEngine.prototype.getSampleEntry = function (absPath) {
    var entry = this.bufferCache.get(absPath);
    if (entry) {
      entry.lastUse = Date.now();
      return entry.promise;
    }
    var self = this;
    var promise = fetch("/api/arrangement/audio?p=" + encodeURIComponent(absPath))
      .then(function (res) {
        if (!res.ok) throw new Error("素材读取失败 (" + res.status + ")");
        return res.arrayBuffer();
      })
      .then(function (ab) {
        return self.ctx.decodeAudioData(ab);
      })
      .then(function (buffer) {
        entry.buffer = buffer;
        entry.peaks = self.computePeaks(buffer);
        return entry;
      });
    entry = { promise: promise, buffer: null, peaks: null, lastUse: Date.now() };
    this.bufferCache.set(absPath, entry);

    // LRU 淘汰：仅释放缓存引用，正在播放的节点仍持有 buffer
    if (this.bufferCache.size > this.maxCachedBuffers) {
      var keys = Array.from(this.bufferCache.keys());
      keys.sort(function (a, b) { return self.bufferCache.get(a).lastUse - self.bufferCache.get(b).lastUse; });
      while (this.bufferCache.size > this.maxCachedBuffers) {
        this.bufferCache.delete(keys.shift());
      }
    }
    return promise;
  };

  /** 波形峰值预计算：每 bucket 取最大绝对值（缩略图复用，不重复计算） */
  ArrangeEngine.prototype.computePeaks = function (buffer, buckets) {
    buckets = buckets || 600;
    var data = buffer.getChannelData(0);
    var per = Math.max(1, Math.floor(data.length / buckets));
    var peaks = new Float32Array(buckets);
    for (var b = 0; b < buckets; b++) {
      var start = b * per;
      var end = Math.min(data.length, start + per);
      var max = 0;
      for (var i = start; i < end; i += 2) {
        var a = Math.abs(data[i]);
        if (a > max) max = a;
      }
      peaks[b] = max;
    }
    return peaks;
  };

  /* ═══════════ 走带与调度 ═══════════ */

  ArrangeEngine.prototype.secondsPerBeat = function () {
    return 60 / (this.bpm || 120);
  };

  /** 当前播放头所在的时间线拍 */
  ArrangeEngine.prototype.currentBeat = function () {
    if (!this.isPlaying || !this.ctx) return this._playStartBeat;
    var pos = (this.ctx.currentTime - this._anchorCtxTime) / this.secondsPerBeat();
    return this._posToBeat(pos);
  };

  /** 从指定时间线拍开始播放 */
  ArrangeEngine.prototype.play = function (startBeat) {
    this.resume();
    this.stopSchedule();
    this._playStartBeat = startBeat;
    this._anchorCtxTime = this.ctx.currentTime + 0.06;
    this._anchorPos = 0;
    this._schedPos = -0.0001;
    this.isPlaying = true;
    var self = this;
    this._timer = window.setInterval(function () { self.tick(); }, this._intervalMs);
    this.tick();
  };

  ArrangeEngine.prototype.stopSchedule = function () {
    if (this._timer) { window.clearInterval(this._timer); this._timer = null; }
    this.stopAllVoices();
    this.isPlaying = false;
  };

  ArrangeEngine.prototype.stopAllVoices = function () {
    for (var id in this.trackNodes) {
      var nodes = this.trackNodes[id];
      if (nodes.synth) nodes.synth.stopAll();
      if (nodes.soundfont) nodes.soundfont.stopAll();
    }
    // 音频源先做 30ms 线性淡出再停：瞬时截断在暂停/跳转/变速时是明显爆音
    var now = this.ctx ? this.ctx.currentTime : 0;
    for (var i = 0; i < this.activeSources.length; i++) {
      this._releaseSource(this.activeSources[i], now);
    }
    this.activeSources = [];
  };

  /** 淡出并释放一个音频源（短包络 + 延迟断开，避免爆音与节点泄漏） */
  ArrangeEngine.prototype._releaseSource = function (rec, now) {
    try {
      rec.gain.gain.cancelScheduledValues(now);
      rec.gain.gain.setValueAtTime(rec.gain.gain.value || 0.0001, now);
      rec.gain.gain.linearRampToValueAtTime(0, now + 0.03);
    } catch (e) {}
    try { rec.src.stop(now + 0.04); } catch (e) {}
    setTimeout(function () {
      try { rec.src.disconnect(); } catch (e) {}
      try { rec.gain.disconnect(); } catch (e) {}
    }, 90);
  };

  /** 停止挂在指定轨道上的全部音频源（删除轨道时调用，防幽灵发声） */
  ArrangeEngine.prototype.stopTrackSources = function (trackId) {
    var kept = [];
    var now = this.ctx ? this.ctx.currentTime : 0;
    for (var i = 0; i < this.activeSources.length; i++) {
      var rec = this.activeSources[i];
      if (rec.trackId === trackId) {
        this._releaseSource(rec, now);
      } else {
        kept.push(rec);
      }
    }
    this.activeSources = kept;
  };

  /**
   * 调度心跳：把 [schedPos, now+lookahead] 区间内的所有事件排入 AudioContext
   * 事件源 = 各轨 clip（MIDI 音符 / 音频片段 / 节拍器 click），循环段按周期展开
   */
  ArrangeEngine.prototype.tick = function () {
    if (!this.isPlaying) return;
    var horizonPos = (this.ctx.currentTime + this._lookahead - this._anchorCtxTime) / this.secondsPerBeat();
    if (horizonPos <= this._schedPos) return;

    var tracks = this.getTracks ? this.getTracks() : [];
    var self = this;
    var spb = this.secondsPerBeat();

    // 展开区间 [from, to)（展开位置），逐 clip 排事件
    var from = Math.max(this._schedPos, 0);
    var to = horizonPos;

    for (var ti = 0; ti < tracks.length; ti++) {
      var track = tracks[ti];
      if (track.mute || (this._anySolo(tracks) && !track.solo)) continue;
      var nodes = this.ensureTrack(track);
      if (nodes.sfLoading) continue;

      for (var ci = 0; ci < track.clips.length; ci++) {
        var clip = track.clips[ci];
        if (clip.mute) continue;
        this._scheduleClip(clip, track, nodes, from, to, spb);
      }
    }

    // 节拍器 click（每个整数拍）
    if (this.metronome) {
      var b0 = Math.ceil(from);
      for (var beat = b0; beat < to; beat++) {
        var tBeat = this._posToBeat(beat);
        if (tBeat === null) continue;
        var when = this._anchorCtxTime + beat * spb;
        this.click(when, Math.abs(tBeat % 4) < 0.001);
      }
    }

    this._schedPos = to;
  };

  ArrangeEngine.prototype._anySolo = function (tracks) {
    for (var i = 0; i < tracks.length; i++) if (tracks[i].solo && !tracks[i].mute) return true;
    return false;
  };

  /** 展开位置 → 时间线拍；不在任何有效区间时返回 null */
  ArrangeEngine.prototype._posToBeat = function (pos) {
    var lp = this.loop;
    if (!lp.on || lp.end <= lp.start) return this._playStartBeat + pos;
    var segLen = lp.end - lp.start;
    var rel = pos;
    if (this._playStartBeat < lp.start) {
      // 起点在循环段之前：先直线走到 loop.start，再进入循环
      var lead = lp.start - this._playStartBeat;
      if (rel < lead) return this._playStartBeat + rel;
      rel = rel - lead;
    } else {
      rel = rel + (this._playStartBeat - lp.start);
    }
    return lp.start + (rel % segLen);
  };

  /** 展开位置区间 → 时间线拍区间列表（处理循环回绕分段） */
  ArrangeEngine.prototype._posRangeToBeats = function (from, to) {
    // 返回 [{beatFrom, beatTo, posOffset}]，posOffset 用于换算 ctx 时间
    var lp = this.loop;
    var out = [];
    if (!lp.on || lp.end <= lp.start) {
      out.push({ beatFrom: this._playStartBeat + from, beatTo: this._playStartBeat + to, posFrom: from });
      return out;
    }
    var segLen = lp.end - lp.start;
    var lead = 0;
    if (this._playStartBeat < lp.start) lead = lp.start - this._playStartBeat;

    var pos = from;
    while (pos < to) {
      var relInSeg, beatFrom;
      if (pos < lead) {
        beatFrom = this._playStartBeat + pos;
        var segEnd = Math.min(to, lead);
        out.push({ beatFrom: beatFrom, beatTo: this._playStartBeat + segEnd, posFrom: pos });
        pos = segEnd;
        continue;
      }
      var rel = pos - lead;
      var k = Math.floor(rel / segLen);
      var off = rel - k * segLen;
      var beatStart = lp.start + off;
      var remainInSeg = segLen - off;
      var chunk = Math.min(remainInSeg, to - pos);
      out.push({ beatFrom: beatStart, beatTo: beatStart + chunk, posFrom: pos });
      pos += chunk;
      if (out.length > 64) break; // 安全上限
    }
    return out;
  };

  ArrangeEngine.prototype._scheduleClip = function (clip, track, nodes, from, to, spb) {
    var segs = this._posRangeToBeats(from, to);
    for (var s = 0; s < segs.length; s++) {
      var seg = segs[s];
      var clipStart = clip.start;
      var clipEnd = clip.start + clip.length;
      if (clipEnd <= seg.beatFrom || clipStart >= seg.beatTo) continue;

      var evFrom = Math.max(seg.beatFrom, clipStart);
      var evTo = Math.min(seg.beatTo, clipEnd);

      if (clip.type === "midi" && clip.notes && clip.notes.length) {
        this._scheduleMidiClip(clip, nodes, evFrom, evTo, seg, spb);
      }
      if (clip.type === "audio") {
        this._scheduleAudioClip(clip, track, nodes, evFrom, evTo, seg, spb);
      }
    }
  };

  ArrangeEngine.prototype._scheduleMidiClip = function (clip, nodes, evFrom, evTo, seg, spb) {
    /* 音名→音高记忆化：调度心跳每 25ms 全量扫描剪辑音符，此前每个音符
       每次都走正则解析（数千音符的工程每秒数十万次正则 → 卡顿） */
    var pitchCache = this._pitchCache || (this._pitchCache = {});
    for (var ni = 0; ni < clip.notes.length; ni++) {
      var n = clip.notes[ni];
      var nStart = clip.start + n.start;
      var nEnd = clip.start + n.end;
      if (nEnd <= evFrom || nStart >= evTo) continue;
      var startBeat = Math.max(nStart, evFrom);
      var when = this._anchorCtxTime + (seg.posFrom + (startBeat - seg.beatFrom)) * spb;
      var midiNote = pitchCache[n.note];
      if (midiNote === undefined) {
        midiNote = window.MidiParse ? window.MidiParse.noteNameToNumber(n.note) : 60;
        pitchCache[n.note] = midiNote;
      }
      var dur = Math.max(0.02, (nEnd - startBeat)) * spb;
      var vel = n.velocity || 100;
      if (nodes.soundfont) {
        nodes.soundfont.noteOn(midiNote, vel, when);
        nodes.soundfont.noteOff(midiNote, when + dur);
      } else if (nodes.synth) {
        nodes.synth.noteOn(midiNote, vel, when);
        nodes.synth.noteOff(midiNote, when + dur);
      }
    }
  };

  ArrangeEngine.prototype._scheduleAudioClip = function (clip, track, nodes, evFrom, evTo, seg, spb) {
    var self = this;
    if (!clip.src || !clip.src.p) return;
    var entryPromise = this.getSampleEntry(clip.src.p);
    var playFromBeat = Math.max(evFrom, clip.start);
    var clipRemain = (clip.start + clip.length) - playFromBeat;
    if (clipRemain <= 0) return;
    var when = this._anchorCtxTime + (seg.posFrom + (playFromBeat - seg.beatFrom)) * spb;

    entryPromise.then(function (entry) {
      if (!entry || !entry.buffer || !self.isPlaying) return;
      var offsetInClip = playFromBeat - clip.start; // 拍
      var srcOffset = (clip.offset || 0) + offsetInClip * spb; // 秒（素材内偏移）
      var durSec = Math.min(clipRemain * spb, Math.max(0, entry.buffer.duration - srcOffset));
      if (durSec <= 0.01) return;
      // 解码迟到（LRU 驱逐后重取/恢复工程）：调度时刻已过——从"现在应
      // 播到的素材位置"立即接入，而非按过期时刻起播造成素材位置漂移
      if (when < self.ctx.currentTime) {
        var late = self.ctx.currentTime - when;
        srcOffset += late;
        durSec -= late;
        if (durSec <= 0.01) return;
        when = self.ctx.currentTime;
      }

      var src = self.ctx.createBufferSource();
      src.buffer = entry.buffer;
      var gain = self.ctx.createGain();
      src.connect(gain);
      gain.connect(nodes.gain);

      // 渐入渐出包络
      var fadeIn = Math.max(0, clip.fadeIn || 0);
      var fadeOut = Math.max(0, clip.fadeOut || 0);
      var clipGain = clip.gain !== undefined ? clip.gain : 1;
      var t0 = when;
      var tEnd = when + durSec;
      gain.gain.setValueAtTime(0.0001, t0);
      if (fadeIn > 0.005) {
        gain.gain.linearRampToValueAtTime(clipGain, t0 + Math.min(fadeIn, durSec));
        if (durSec > fadeIn) gain.gain.setValueAtTime(clipGain, t0 + fadeIn);
      } else {
        gain.gain.setValueAtTime(clipGain, t0);
      }
      if (fadeOut > 0.005 && durSec > fadeOut) {
        gain.gain.setValueAtTime(clipGain, tEnd - fadeOut);
        gain.gain.linearRampToValueAtTime(0.0001, tEnd);
      } else if (fadeOut > 0.005) {
        gain.gain.linearRampToValueAtTime(0.0001, tEnd);
      }

      src.start(t0, srcOffset, durSec + 0.02);
      src.stop(tEnd + 0.02);
      var rec = { src: src, gain: gain, trackId: track.id };
      self.activeSources.push(rec);
      src.onended = function () {
        var idx = self.activeSources.indexOf(rec);
        if (idx !== -1) self.activeSources.splice(idx, 1);
        try { src.disconnect(); gain.disconnect(); } catch (e) {}
      };
    }).catch(function () {});
  };

  /** 节拍器 click */
  ArrangeEngine.prototype.click = function (when, isDownbeat) {
    var osc = this.ctx.createOscillator();
    var gain = this.ctx.createGain();
    osc.type = "sine";
    osc.frequency.setValueAtTime(isDownbeat ? 1600 : 900, when);
    osc.frequency.exponentialRampToValueAtTime(100, when + 0.04);
    gain.gain.setValueAtTime(isDownbeat ? 0.32 : 0.2, when);
    gain.gain.exponentialRampToValueAtTime(0.0001, when + 0.045);
    osc.connect(gain);
    gain.connect(this.masterGain);
    osc.start(when);
    osc.stop(when + 0.06);
  };

  /** 单次试听素材（素材库双击） */
  ArrangeEngine.prototype.previewSample = function (absPath) {
    var self = this;
    this.resume();
    return this.getSampleEntry(absPath).then(function (entry) {
      if (!entry || !entry.buffer) return;
      // 切换试听：先停上一段（其 onended 会自行断开节点，不再泄漏 gain）
      if (self._previewSrc) {
        try { self._previewSrc.stop(); } catch (e) {}
      }
      var src = self.ctx.createBufferSource();
      src.buffer = entry.buffer;
      var gain = self.ctx.createGain();
      gain.gain.setValueAtTime(0.8, self.ctx.currentTime);
      src.connect(gain);
      gain.connect(self.masterGain);
      src.onended = function () {
        try { src.disconnect(); } catch (e) {}
        try { gain.disconnect(); } catch (e) {}
        if (self._previewSrc === src) self._previewSrc = null;
      };
      src.start();
      self._previewSrc = src;
    });
  };

  window.ArrangeEngine = ArrangeEngine;
})(window);
