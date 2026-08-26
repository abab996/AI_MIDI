/* ═══════════════════════════════════════════════════════════════
   编排窗口音频引擎 v2（完全重写）

   v1 的问题（实证）：
   - 调度心跳（setInterval 25ms）在 UI 渲染繁忙时被推迟甚至饿死，
     音符来不及排入 → 丢音/断续；
   - 心跳时（提前 0.3s）就把音符包络（AudioParam 事件）排到远期，
     WebView2 内核对远期 AudioParam 事件的处理不可靠：
     cancelAndHoldAtTime 提前切断音符、gain.value 快照取到错误瞬时值，
     表现为"粒子效果器式"断续与爆响。

   v2 架构：
   1) Web Worker 心跳（25ms）——独立于主线程，UI 渲染阻塞不影响节拍；
   2) 两级调度：
      - 入队（提前 LOOKAHEAD_S=0.45s）：只做纯数据展开（循环段→音符
        事件列表），不触碰任何音频节点；音频素材同时预热解码；
      - 触发（临近 TRIGGER_S=0.15s）：才创建 osc/source 与 gain 并排
        包络。节点 start/stop 的远期调度是采样级精确且可靠的（三个
        实证 bug 全部出在包络，从不在 start/stop）；包络事件全部落在
        未来 ≤150ms 的近端可靠区间；
   3) 主线程偶发卡顿 <150ms 时音符仍准点触发（start(when) 为绝对时钟）；
      卡顿更久时积压的 Worker 消息批量补触发（轻微延迟、不丢音）。

   对外 API 与 v1 完全兼容（arrange.js 无需改动）。
   ═══════════════════════════════════════════════════════════════ */
(function (window) {
  "use strict";

  var AudioContext = window.AudioContext || window.webkitAudioContext;

  var HEARTBEAT_MS = 25;  // 心跳周期（Worker 驱动）
  var LOOKAHEAD_S = 0.45; // 入队提前量
  var TRIGGER_S = 0.15;   // 触发提前量（包络事件的最远端点）

  function isNativePreferred() {
    try {
      // 全局后端为准（Go权威），引擎未就绪时自动回退 Web
      var backend = (window.__engineBackend || (window.AudioBackend && window.AudioBackend.getMode ? window.AudioBackend.getMode() : "auto"));
      if (backend === "webaudio") return false;
      if (window.__engineState && window.__engineState !== "ready") return false;
      if (window.AudioBackend && window.AudioBackend.isNativePreferred) {
        return window.AudioBackend.isNativePreferred();
      }
      if (window.AudioBackend && window.AudioBackend.getMode) {
        return window.AudioBackend.getMode() === "auto" && window.EngineBridge && window.EngineBridge.available;
      }
    } catch (e) {}
    return false;
  }
  function trackIndexOf(trackId, tracks) {
    if (!tracks) return -1;
    for (var i = 0; i < tracks.length; i++) if (tracks[i].id === trackId) return i;
    return -1;
  }

  function ArrangeEngine() {
    this.ctx = null;
    this.masterGain = null;
    this.trackNodes = {};       // trackId -> { gain, analyser, synth, soundfont, sourceKey }
    this.bufferCache = new Map(); // path -> { promise, buffer, peaks, lastUse }
    this.maxCachedBuffers = 48;
    this.activeSources = [];      // 播放中的 BufferSource（停止时快速静音）

    // 走带状态（arrange.js 同步写入）
    this.isPlaying = false;
    this.bpm = 120;
    this.metronome = false;
    this.loop = { on: false, start: 0, end: 16 };

    // 调度状态
    this._anchorCtxTime = 0;  // 播放起点对应的 AudioContext 时间
    this._playStartBeat = 0;  // 播放起点的时间线拍
    this._schedPos = 0;       // 已入队的展开位置（未回绕拍数）

    // 待触发队列（按 t 升序；音符触发后才生成 off 事件）
    this._pendingOn = [];     // {t, midi, vel, dur, nodes}
    this._pendingOff = [];    // {t, midi, nodes}
    this._pendingClicks = []; // {t, downbeat}
    this._pendingClips = [];  // {t, tEnd, clip, track, nodes}

    this._heartbeat = null;   // Worker 心跳句柄
    this._hbFallback = null;  // setInterval 降级句柄

    // 外部回调（arrange.js 注入）
    this.getTracks = null;
    this.onSoundFontLoaded = null;
    this.onSoundFontError = null;
  }

  /* ═══════════ 心跳（Worker 优先，setInterval 降级） ═══════════ */

  function createHeartbeat() {
    try {
      var src = "setInterval(function(){postMessage(0)}," + HEARTBEAT_MS + ");";
      var blob = new Blob([src], { type: "application/javascript" });
      var w = new Worker(URL.createObjectURL(blob));
      return {
        worker: w,
        start: function (cb) { w.onmessage = cb; },
        stop: function () { w.onmessage = null; },
        dispose: function () { w.terminate(); }
      };
    } catch (e) {
      return null;
    }
  }

  ArrangeEngine.prototype._startHeartbeat = function () {
    var self = this;
    this._stopHeartbeat();
    if (this._heartbeat) {
      this._heartbeat.start(function () { self._onHeartbeat(); });
    } else {
      this._hbFallback = setInterval(function () { self._onHeartbeat(); }, HEARTBEAT_MS);
    }
  };

  ArrangeEngine.prototype._stopHeartbeat = function () {
    if (this._heartbeat) this._heartbeat.stop();
    if (this._hbFallback) { clearInterval(this._hbFallback); this._hbFallback = null; }
  };

  /* ═══════════ 上下文与轨道链路（沿用 v1，已稳定） ═══════════ */

  ArrangeEngine.prototype.init = function () {
    if (this.ctx) return;
    this.ctx = (window.SharedAudio && window.SharedAudio.get()) || new AudioContext();
    this.masterGain = this.ctx.createGain();
    this.masterGain.gain.setValueAtTime(0.9, this.ctx.currentTime);
    // 主链限幅器：多轨合成超 0dB 时压回可听区间
    this.limiter = this.ctx.createDynamicsCompressor();
    this.limiter.threshold.value = -6;
    this.limiter.knee.value = 6;
    this.limiter.ratio.value = 12;
    this.limiter.attack.value = 0.003;
    this.limiter.release.value = 0.12;
    this.masterGain.connect(this.limiter);
    this.limiter.connect(this.ctx.destination);
    this._heartbeat = this._heartbeat || createHeartbeat();
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
        nodes.soundfont = new window.SoundFontPlayer(this.ctx, nodes.gain);
        nodes.soundfont.init();
        nodes.soundfont.setPreset(src.tone || "piano");
      } else {
        nodes.synth = new window.SynthEngine(this.ctx, nodes.gain);
        nodes.synth.init();
        nodes.synth.setWaveform(src.wave || "sawtooth");
        // 默认走 JUCE 时，编曲亦走引擎；仅 WEBAUDIO 强制留 WebAudio
        var useNativeSynth = isNativePreferred();
        nodes.synth._forceWebAudio = !useNativeSynth;
        // 标记是否原生直通（供 _triggerDue 分流，SF2与synth统一用 _useNative）
        nodes._useNative = useNativeSynth;
        nodes._isNativeSynth = useNativeSynth;
      }
    }
    if (created && this.getTracks) {
      this.applyMix(this.getTracks());
    }
    return nodes;
  };

  /** 从 IndexedDB 音源库异步加载轨道 SF2（原生优先时直通 JUCE，每轨独立） */
  ArrangeEngine.prototype.loadTrackSoundFont = function (track, nodes, src) {
    var self = this;
    if (!window.SoundLibrary || !src.libId) return;
    nodes.sfLoading = true;
    window.SoundLibrary.getSoundFont(src.libId).then(function (rec) {
      if (!rec || !rec.data) throw new Error("音源数据不存在");
      // 原生优先：尝试经 JUCE 加载（每轨独立SF2，为VST铺垫）
      if (isNativePreferred() && window.EngineBridge && window.EngineBridge.loadSoundFont) {
        var tracks = self.getTracks ? self.getTracks() : [];
        var idx = trackIndexOf(track.id, tracks);
        if (idx < 0) idx = 0;
        // 仿 handler_soundfont.go 的 safe 规则
        var rawName = rec.name || src.name || "soundfont";
        var safe = rawName.replace(/[^\p{L}\p{N}_\-\.]/gu, "_");
        safe = safe.replace(/\.[^/.]+$/, "");
        if (!safe) safe = "soundfont";
        var diskPath = "Library/soundfonts/" + safe + ".sf2";
        // 优先用磁盘路径（已镜像），失败则回退 Web 解析
        return window.EngineBridge.loadSoundFont(diskPath, idx).then(function(){
          nodes.sfLoading = false;
          nodes._useNative = true;
          nodes._nativeTrackIdx = idx;
          if (self.onSoundFontLoaded) self.onSoundFontLoaded(track.id, { name: rec.name, presets: rec.presets, native: true });
        }).catch(function(e){
          // 回退 WebAudio
          try {
            var parsed = nodes.soundfont.parseSF2(rec.data);
            if (src.presetId) nodes.soundfont.setPreset(src.presetId);
            nodes._useNative = false;
            nodes.sfLoading = false;
            if (self.onSoundFontLoaded) self.onSoundFontLoaded(track.id, parsed);
          } catch(err2){
            nodes.sfLoading = false;
            throw err2;
          }
        });
      } else {
        var parsed = nodes.soundfont.parseSF2(rec.data);
        if (src.presetId) nodes.soundfont.setPreset(src.presetId);
        nodes.sfLoading = false;
        if (self.onSoundFontLoaded) self.onSoundFontLoaded(track.id, parsed);
      }
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

  /** 轨道电平峰值（电平表 ~20fps 轮询） */
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

  /* ═══════════ 素材解码缓存（LRU）与波形峰值（沿用 v1） ═══════════ */

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

    if (this.bufferCache.size > this.maxCachedBuffers) {
      var keys = Array.from(this.bufferCache.keys());
      keys.sort(function (a, b) { return self.bufferCache.get(a).lastUse - self.bufferCache.get(b).lastUse; });
      while (this.bufferCache.size > this.maxCachedBuffers) {
        this.bufferCache.delete(keys.shift());
      }
    }
    return promise;
  };

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

  /* ═══════════ 位置 ↔ 拍 换算（含循环回绕；数学沿用已验证实现） ═══════════ */

  ArrangeEngine.prototype.secondsPerBeat = function () {
    return 60 / (this.bpm || 120);
  };

  /** 当前播放头所在的时间线拍 */
  ArrangeEngine.prototype.currentBeat = function () {
    if (!this.isPlaying || !this.ctx) return this._playStartBeat;
    var pos = (this.ctx.currentTime - this._anchorCtxTime) / this.secondsPerBeat();
    return this._posToBeat(pos);
  };

  /** 展开位置 → 时间线拍 */
  ArrangeEngine.prototype._posToBeat = function (pos) {
    var lp = this.loop;
    if (!lp.on || lp.end <= lp.start) return this._playStartBeat + pos;
    var segLen = lp.end - lp.start;
    var rel = pos;
    if (this._playStartBeat < lp.start) {
      var lead = lp.start - this._playStartBeat;
      if (rel < lead) return this._playStartBeat + rel;
      rel = rel - lead;
    } else {
      rel = rel + (this._playStartBeat - lp.start);
    }
    return lp.start + (rel % segLen);
  };

  /** 展开位置区间 → 时间线拍区间列表（循环回绕分段；与 _posToBeat 完全一致） */
  ArrangeEngine.prototype._posRangeToBeats = function (from, to) {
    var lp = this.loop;
    var out = [];
    var push = function (beatFrom, chunk, posFrom) {
      out.push({ beatFrom: beatFrom, beatTo: beatFrom + chunk, posFrom: posFrom });
    };
    if (!lp.on || lp.end <= lp.start) {
      push(this._playStartBeat + from, to - from, from);
      return out;
    }
    var segLen = lp.end - lp.start;
    var pos = from;
    while (pos < to && out.length <= 64) {
      if (this._playStartBeat < lp.start) {
        var lead = lp.start - this._playStartBeat;
        if (pos < lead) {
          var chunkLead = Math.min(to, lead) - pos;
          push(this._playStartBeat + pos, chunkLead, pos);
          pos += chunkLead;
          continue;
        }
        var relPre = pos - lead;
        var offPre = relPre % segLen;
        var chunkPre = Math.min(segLen - offPre, to - pos);
        push(lp.start + offPre, chunkPre, pos);
        pos += chunkPre;
        continue;
      }
      var rel = pos + (this._playStartBeat - lp.start);
      var off = rel % segLen;
      var chunk = Math.min(segLen - off, to - pos);
      push(lp.start + off, chunk, pos);
      pos += chunk;
    }
    return out;
  };

  /* ═══════════ 走带 ═══════════ */

  /** 从指定时间线拍开始播放 */
  ArrangeEngine.prototype.play = function (startBeat) {
    var self = this;
    this.resume().then(function () {
      self._beginPlay(startBeat);
    });
  };

  ArrangeEngine.prototype._beginPlay = function (startBeat) {
    this.stopSchedule();
    this._playStartBeat = startBeat;
    this._anchorCtxTime = this.ctx.currentTime + 0.08;
    this._schedPos = -0.0001;
    this._pendingOn = [];
    this._pendingOff = [];
    this._pendingClicks = [];
    this._pendingClips = [];
    this.isPlaying = true;
    this._queueSeekClips(startBeat);
    this._startHeartbeat();
    this._onHeartbeat();
  };

  /** 定位播放接入：起播点落在音频剪辑中段的（起点在起播点之前，
      永远不会经窗口入队），直接生成从起播点切入的实例 */
  ArrangeEngine.prototype._queueSeekClips = function (startBeat) {
    var tracks = this.getTracks ? this.getTracks() : [];
    for (var ti = 0; ti < tracks.length; ti++) {
      var track = tracks[ti];
      if (track.mute || (this._anySolo(tracks) && !track.solo)) continue;
      var nodes = this.ensureTrack(track);
      if (!nodes || nodes.sfLoading) continue;
      for (var ci = 0; ci < track.clips.length; ci++) {
        var clip = track.clips[ci];
        if (clip.mute || clip.type !== "audio" || !clip.src || !clip.src.p) continue;
        if (clip.start >= startBeat) continue; // 起点在起播点之后，走正常窗口入队
        var clipEnd = clip.start + clip.length;
        if (startBeat >= clipEnd) continue;     // 剪辑已整体在起播点之前
        this.getSampleEntry(clip.src.p).catch(function () {});
        this._pendingClips.push({
          t: this._anchorCtxTime,
          tEnd: this._anchorCtxTime + (clipEnd - startBeat) * this.secondsPerBeat(),
          playFromBeat: startBeat,
          clipRemain: clipEnd - startBeat,
          clip: clip,
          track: track,
          nodes: nodes
        });
      }
    }
  };

  /** 停止调度并立即静音（原生轨经 EngineBridge 逐个 noteOff，避免挂音） */
  ArrangeEngine.prototype.stopSchedule = function () {
    this._stopHeartbeat();
    if (isNativePreferred() && window.EngineBridge) {
      try {
        var tracks = this.getTracks ? this.getTracks() : [];
        for (var k = 0; k < this._pendingOff.length; k++) {
          var ev = this._pendingOff[k];
          if (!ev.trackId || !ev.nodes || !ev.nodes._useNative) continue;
          var idx = trackIndexOf(ev.trackId, tracks);
          if (idx >= 0) { try { window.EngineBridge.noteOffTrack(idx, ev.midi); } catch(e) {} }
        }
      } catch(e) {}
    }
    this._pendingOn = [];
    this._pendingOff = [];
    this._pendingClicks = [];
    this._pendingClips = [];
    this.isPlaying = false;
    this.stopAllVoices();
  };

  /** v1 兼容别名（arrange.js / 自测页可能引用） */
  ArrangeEngine.prototype.tick = function () {
    this._onHeartbeat();
  };

  ArrangeEngine.prototype.stopAllVoices = function () {
    for (var id in this.trackNodes) {
      var nodes = this.trackNodes[id];
      if (nodes.synth) nodes.synth.stopAll();
      if (nodes.soundfont) nodes.soundfont.stopAll();
    }
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

  /** 停止挂在指定轨道上的全部音频源（删除轨道时调用） */
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

  /* ═══════════ v2 核心：心跳 → 入队 → 临近触发 ═══════════ */

  ArrangeEngine.prototype._onHeartbeat = function () {
    if (!this.isPlaying || !this.ctx) return;
    var now = this.ctx.currentTime;
    var spb = this.secondsPerBeat();

    // 1) 入队：把 [schedPos, now+LOOKAHEAD) 窗口内的事件展开为纯数据
    var horizonPos = (now + LOOKAHEAD_S - this._anchorCtxTime) / spb;
    if (horizonPos > this._schedPos) {
      var from = Math.max(this._schedPos, 0);
      this._scanWindow(from, horizonPos, spb);
      this._schedPos = horizonPos;
    }

    // 2) 触发：临近（≤TRIGGER_S）的事件创建音频节点并排近端包络
    this._triggerDue(now);
  };

  /** 展开调度窗口：每轨每剪辑 → 待触发队列（不触碰音频节点） */
  ArrangeEngine.prototype._scanWindow = function (from, to, spb) {
    var tracks = this.getTracks ? this.getTracks() : [];
    var segs = this._posRangeToBeats(from, to);

    for (var s = 0; s < segs.length; s++) {
      var seg = segs[s];

      // 节拍器：整数拍事件
      if (this.metronome) {
        for (var mb = Math.ceil(seg.beatFrom - 1e-9); mb < seg.beatTo; mb++) {
          if (mb < 0) continue;
          this._pendingClicks.push({
            t: this._anchorCtxTime + (seg.posFrom + (mb - seg.beatFrom)) * spb,
            downbeat: Math.abs(mb % 4) < 0.001
          });
        }
      }

      for (var ti = 0; ti < tracks.length; ti++) {
        var track = tracks[ti];
        if (track.mute || (this._anySolo(tracks) && !track.solo)) continue;
        var nodes = this.ensureTrack(track);
        if (!nodes) continue;
        if (nodes.sfLoading) continue;

        for (var ci = 0; ci < track.clips.length; ci++) {
          var clip = track.clips[ci];
          if (clip.mute) continue;
          var clipStart = clip.start;
          var clipEnd = clip.start + clip.length;
          if (clipEnd <= seg.beatFrom || clipStart >= seg.beatTo) continue;

          var evFrom = Math.max(seg.beatFrom, clipStart);
          var evTo = Math.min(seg.beatTo, clipEnd);

          if (clip.type === "midi" && clip.notes && clip.notes.length) {
            this._queueMidiClip(clip, track, nodes, evFrom, evTo, seg, spb);
          } else if (clip.type === "audio" && clip.src && clip.src.p) {
            this._queueAudioClip(clip, track, nodes, evFrom, evTo, seg, spb);
          }
        }
      }
    }
  };

  ArrangeEngine.prototype._anySolo = function (tracks) {
    for (var i = 0; i < tracks.length; i++) if (tracks[i].solo && !tracks[i].mute) return true;
    return false;
  };

  /** MIDI 剪辑 → 待触发音符事件（noteOn 仅当音符物理起点落在窗口内） */
  ArrangeEngine.prototype._queueMidiClip = function (clip, track, nodes, evFrom, evTo, seg, spb) {
    var pitchCache = this._pitchCache || (this._pitchCache = {});
    for (var ni = 0; ni < clip.notes.length; ni++) {
      var n = clip.notes[ni];
      var nStart = clip.start + n.start;
      var nEnd = clip.start + n.end;
      if (nStart < evFrom || nStart >= evTo) continue;
      // 原生优先时：synth 轨也允许（无 Web 节点也能经 EngineBridge 发声）
      var native = isNativePreferred() && track && track.source && track.source.type === "synth";
      if (!native && !nodes.synth && !nodes.soundfont) continue;
      var midiNote = pitchCache[n.note];
      if (midiNote === undefined) {
        midiNote = window.MidiParse ? window.MidiParse.noteNameToNumber(n.note) : 60;
        pitchCache[n.note] = midiNote;
      }
      this._pendingOn.push({
        t: this._anchorCtxTime + (seg.posFrom + (nStart - seg.beatFrom)) * spb,
        midi: midiNote,
        vel: n.velocity || 100,
        dur: Math.max(0.02, (nEnd - nStart)) * spb,
        nodes: nodes,
        trackId: track ? track.id : null
      });
    }
  };

  /** 音频素材剪辑 → 待触发事件；入队即预热解码（触发时大概率已就绪）。
      关键：仅当剪辑物理起点落在窗口内才入队（与 MIDI 音符同判定）——
      v1 对每个相交窗口都排一个实例，同一素材叠加播放 N 次
      （颗粒/合唱感的重要来源之一）。起点错过窗口的（起播点在剪辑
      中段的定位播放）由 _startAudioClip 的迟到接入逻辑兜底。 */
  ArrangeEngine.prototype._queueAudioClip = function (clip, track, nodes, evFrom, evTo, seg, spb) {
    // 原生优先时样本走 JUCE SamplePool（批量调度），Web队列跳过以免双重播放
    if (isNativePreferred()) return;
    if (clip.start < evFrom || clip.start >= evTo) return;
    var clipRemain = clip.length;
    if (clipRemain <= 0) return;
    // 预热解码缓存（异步、失败仅丢该剪辑实例）
    this.getSampleEntry(clip.src.p).catch(function () {});
    this._pendingClips.push({
      t: this._anchorCtxTime + (seg.posFrom + (clip.start - seg.beatFrom)) * spb,
      tEnd: this._anchorCtxTime + (seg.posFrom + (Math.min(evTo, clip.start + clip.length) - seg.beatFrom)) * spb,
      playFromBeat: clip.start,
      clipRemain: clipRemain,
      clip: clip,
      track: track,
      nodes: nodes
    });
  };

  /** 触发到期事件：创建音频节点 + 近端（≤TRIGGER_S）包络 */
  ArrangeEngine.prototype._triggerDue = function (now) {
    var i, ev;

    // 音符 on：过期 >0.25s 丢弃；否则临近触发（原生优先时走 JUCE，失败回退 WebAudio）
    var stillOn = [];
    var nativeWanted = isNativePreferred();
    for (i = 0; i < this._pendingOn.length; i++) {
      ev = this._pendingOn[i];
      if (ev.t <= now + TRIGGER_S) {
        if (ev.t >= now - 0.25 && this.isPlaying) {
          var handledNative = false;
          if (nativeWanted && ev.trackId && window.EngineBridge && window.EngineBridge.noteOnTrack && ev.nodes && ev.nodes._useNative) {
            var tracksOn = this.getTracks ? this.getTracks() : [];
            var idxOn = trackIndexOf(ev.trackId, tracksOn);
            if (idxOn >= 0) {
              try { window.EngineBridge.noteOnTrack(idxOn, ev.midi, ev.vel); handledNative = true; } catch(e) { handledNative = false; }
            }
          }
          if (!handledNative) {
            var whenOn = Math.max(ev.t, now);
            if (ev.nodes.soundfont) {
              ev.nodes.soundfont.noteOn(ev.midi, ev.vel, whenOn);
            } else if (ev.nodes.synth) {
              ev.nodes.synth.noteOn(ev.midi, ev.vel, whenOn);
            }
          }
          this._pendingOff.push({
            t: ev.t + ev.dur,   // 原时刻（非 whenOn），保持乐句时值
            midi: ev.midi,
            nodes: ev.nodes,
            trackId: ev.trackId
          });
        }
      } else {
        stillOn.push(ev);
      }
    }
    this._pendingOn = stillOn;

    // 音符 off
    var stillOff = [];
    for (i = 0; i < this._pendingOff.length; i++) {
      ev = this._pendingOff[i];
      if (ev.t <= now + TRIGGER_S + 0.05) {
        if (this.isPlaying) {
          var handledOff = false;
          if (nativeWanted && ev.trackId && window.EngineBridge && window.EngineBridge.noteOffTrack && ev.nodes && ev.nodes._useNative) {
            var tracksOff = this.getTracks ? this.getTracks() : [];
            var idxOff = trackIndexOf(ev.trackId, tracksOff);
            if (idxOff >= 0) {
              try { window.EngineBridge.noteOffTrack(idxOff, ev.midi); handledOff = true; } catch(e) { handledOff = false; }
            }
          }
          if (!handledOff) {
            var whenOff = ev.t < now ? now : ev.t;
            if (ev.nodes.soundfont) {
              ev.nodes.soundfont.noteOff(ev.midi, whenOff);
            } else if (ev.nodes.synth) {
              ev.nodes.synth.noteOff(ev.midi, whenOff);
            }
          }
        }
      } else {
        stillOff.push(ev);
      }
    }
    this._pendingOff = stillOff;

    // 节拍器
    var stillClick = [];
    for (i = 0; i < this._pendingClicks.length; i++) {
      ev = this._pendingClicks[i];
      if (ev.t <= now + TRIGGER_S) {
        if (ev.t >= now - 0.05 && this.isPlaying) {
          this.click(Math.max(ev.t, now), ev.downbeat);
        }
      } else {
        stillClick.push(ev);
      }
    }
    this._pendingClicks = stillClick;

    // 音频素材剪辑
    var stillClip = [];
    for (i = 0; i < this._pendingClips.length; i++) {
      ev = this._pendingClips[i];
      if (ev.t <= now + TRIGGER_S) {
        if (ev.tEnd >= now - 0.2 && this.isPlaying) {
          this._startAudioClip(ev, Math.max(ev.t, now));
        }
      } else {
        stillClip.push(ev);
      }
    }
    this._pendingClips = stillClip;
  };

  /** 触发一个音频素材剪辑实例（解码已预热；迟到则从当前时间线位置接入） */
  ArrangeEngine.prototype._startAudioClip = function (ev, when) {
    var self = this;
    var clip = ev.clip;
    if (this.activeSources.length > 96) return; // 安全阀：洪峰时拒绝新增

    this.getSampleEntry(clip.src.p).then(function (entry) {
      if (!entry || !entry.buffer || !self.isPlaying) return;
      var now = self.ctx.currentTime;
      var spb = self.secondsPerBeat();
      // 触发延迟/解码迟到：以当前实际时间线拍为锚接入素材（循环内亦正确）
      var when2 = when;
      if (when2 < now) {
        var nowBeat = self.currentBeat();
        if (nowBeat >= clip.start + clip.length) return;
        when2 = now;
        ev.playFromBeat = Math.max(ev.playFromBeat, nowBeat);
      }

      var offsetInClip = ev.playFromBeat - clip.start; // 拍
      var srcOffset = (clip.offset || 0) + offsetInClip * spb; // 秒（素材内偏移）
      var durSec = Math.min(ev.clipRemain * spb, Math.max(0, entry.buffer.duration - srcOffset));
      if (durSec <= 0.01) return;

      var src = self.ctx.createBufferSource();
      src.buffer = entry.buffer;
      var gain = self.ctx.createGain();
      src.connect(gain);
      gain.connect(ev.nodes.gain);

      // 包络：仅排近端事件（触发时刻在即）。fadeIn/fadeOut 长于触发窗的
      // 用远端 ramp——渐变失真的代价远小于 v1 的整段包络远端排程。
      var fadeIn = Math.max(0, clip.fadeIn || 0);
      var fadeOut = Math.max(0, clip.fadeOut || 0);
      var clipGain = clip.gain !== undefined ? clip.gain : 1;
      var t0 = when2;
      var tEnd = when2 + durSec;
      gain.gain.setValueAtTime(0.0001, t0);
      if (fadeIn > 0.005) {
        gain.gain.linearRampToValueAtTime(clipGain, t0 + Math.min(fadeIn, durSec));
        if (durSec > fadeIn) gain.gain.setValueAtTime(clipGain, t0 + fadeIn);
      } else {
        gain.gain.linearRampToValueAtTime(clipGain, t0 + 0.01);
      }
      if (fadeOut > 0.005 && durSec > fadeOut) {
        gain.gain.setValueAtTime(clipGain, tEnd - fadeOut);
        gain.gain.linearRampToValueAtTime(0.0001, tEnd);
      }

      src.start(t0, srcOffset, durSec + 0.02);
      src.stop(tEnd + 0.02);
      var rec = { src: src, gain: gain, trackId: ev.track.id };
      self.activeSources.push(rec);
      src.onended = function () {
        var idx = self.activeSources.indexOf(rec);
        if (idx !== -1) self.activeSources.splice(idx, 1);
        try { src.disconnect(); gain.disconnect(); } catch (e) {}
      };
    }).catch(function () {});
  };

  /** 节拍器 click（触发时调用，when 临近 → 包络近端） */
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

  /** 更新音轨混音属性并同步原生引擎（M3 混音图） */
  ArrangeEngine.prototype.updateTrackMix = function (trackIndex, gainVal, panVal, isMute, isSolo, isActive) {
    if (window.EngineBridge && window.EngineBridge.setTrackMix) {
      window.EngineBridge.setTrackMix(trackIndex, gainVal, panVal, isMute, isSolo, isActive);
    }
  };

  /** 单次试听素材（素材库双击）——即时触发，包络天然近端 */
  ArrangeEngine.prototype.previewSample = function (absPath) {
    var self = this;
    this.resume();
    return this.getSampleEntry(absPath).then(function (entry) {
      if (!entry || !entry.buffer) return;
      if (self._previewSrc) {
        try { self._previewSrc.stop(); } catch (e) {}
        self._previewSrc = null;
      }
      var src = self.ctx.createBufferSource();
      src.buffer = entry.buffer;
      var gain = self.ctx.createGain();
      gain.gain.setValueAtTime(0.9, self.ctx.currentTime);
      src.connect(gain);
      gain.connect(self.masterGain);
      src.start();
      self._previewSrc = src;
      src.onended = function () {
        try { src.disconnect(); gain.disconnect(); } catch (e) {}
        if (self._previewSrc === src) self._previewSrc = null;
      };
    });
  };

  window.ArrangeEngine = ArrangeEngine;
})(window);
