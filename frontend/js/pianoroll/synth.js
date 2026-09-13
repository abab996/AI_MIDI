/* Web Audio API 多复音合成器引擎 */
(function (window) {
  "use strict";

  var AudioContext = window.AudioContext || window.webkitAudioContext;

  function SynthEngine(sharedCtx, outNode) {
    this.ctx = null;
    this._sharedCtx = sharedCtx || null; // 多引擎共享同一 AudioContext（编排窗口多轨场景）
    this._outNode = outNode || null;     // 输出目标节点，缺省接 ctx.destination
    this.masterGain = null;
    this.filterNode = null;
    this.activeVoices = {}; // midiNote -> voice object
    this.waveform = "sawtooth"; // "sine", "triangle", "square", "sawtooth"
    this.attack = 0.01;
    this.decay = 0.15;
    this.sustain = 0.6;
    this.release = 0.25;
    this.cutoff = 8000;
    this.resonance = 1.0;
    this.volume = 0.7;
    this.isMuted = false;
    this._nativeVoices = {}; // 原生后端下正在发声的 midiNote -> 声部计数
  }

  SynthEngine.prototype.init = function () {
    if (this.ctx) return;
    /* 引擎模式不创建 WebAudio 节点（杜绝"存在即可能被用"）；
       切到 WEBAUDIO 后首次使用时再惰性创建 */
    if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) return;
    try {
      this.ctx = this._sharedCtx || new AudioContext();
      this.masterGain = this.ctx.createGain();
      this.masterGain.gain.setValueAtTime(this.volume, this.ctx.currentTime);

      this.filterNode = this.ctx.createBiquadFilter();
      this.filterNode.type = "lowpass";
      this.filterNode.frequency.setValueAtTime(this.cutoff, this.ctx.currentTime);
      this.filterNode.Q.setValueAtTime(this.resonance, this.ctx.currentTime);

      this.filterNode.connect(this.masterGain);
      this.masterGain.connect(this._outNode || this.ctx.destination);
    } catch (e) {
      console.warn("AudioContext init error:", e);
    }
  };

  SynthEngine.prototype.resume = function () {
    if (!this.ctx) this.init();
    if (this.ctx && this.ctx.state === "suspended") {
      return this.ctx.resume();
    }
    return Promise.resolve();
  };

  SynthEngine.prototype.midiToFreq = function (midiNote) {
    return 440 * Math.pow(2, (midiNote - 69) / 12);
  };

  /* ===== 原生音频引擎双后端（M2→M3统一）=====
     mode："auto"（默认：走 JUCE，失败回退 WebAudio）|"webaudio"（强制 WebAudio）
     以 Go settings.audio.backend 为权威，localStorage 仅作离线缓存与启动瞬时的同步源。 */
  var BACKEND_KEY = "ai_midi_audio_backend";
  var _backendMode = (function () {
    try { return localStorage.getItem(BACKEND_KEY) || "auto"; } catch (e) { return "auto"; }
  })();

  // 启动时异步与 Go 同步（Go 为准，失败保持本地值）
  (function syncBackendFromServer() {
    try {
      fetch("/api/audio/settings").then(function (r) { return r.json(); }).then(function (j) {
        var b = j && j.backend;
        if (b === "auto" || b === "webaudio") {
          _backendMode = b;
          try { localStorage.setItem(BACKEND_KEY, b); } catch (e) {}
        }
      }).catch(function () {});
    } catch (e) {}
  })();

  window.AudioBackend = {
    getMode: function () { return _backendMode; },
    setMode: function (mode) {
      if (mode !== "auto" && mode !== "webaudio") return Promise.resolve();
      _backendMode = mode;
      try { localStorage.setItem(BACKEND_KEY, mode); } catch (e) {}
      try { window.__engineBackend = mode; } catch(e) {}
      // 同步到 Go（权威），失败不影响本地已生效；返回 {ok} 供调用方提示
      try {
        return fetch("/api/audio/settings", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ backend: mode })
        }).then(function (r) {
          if (!r.ok) return { ok: false };
          return r.json().then(function () { return { ok: true }; }).catch(function () { return { ok: true }; });
        }).catch(function () { return { ok: false }; });
      } catch (e) { return Promise.resolve({ ok: false }); }
    },
    isNativeAvailable: function () {
      return !!(window.EngineBridge && window.EngineBridge.available);
    },
    /* ══ 全局唯一音频路由判定（严格化） ══
       mode()："engine" | "webaudio"
       - 浏览器模式（无 Wails 桥，EngineBridge.available=false）→ 恒 webaudio
       - backend="webaudio" → webaudio
       - backend="auto"（设置页显示为 ENGINE · 仅音频引擎）→ engine，不降级：
         引擎未就绪/失败/声部设置失败一律不发声 + UI 提示，绝不回退 WebAudio */
    mode: function () {
      if (!(window.EngineBridge && window.EngineBridge.available)) return "webaudio";
      if (_backendMode === "webaudio") return "webaudio";
      return "engine";
    },
    isEngine: function () { return AudioBackend.mode() === "engine"; },
    isWebAudio: function () { return AudioBackend.mode() === "webaudio"; },
    isEngineReady: function () {
      return AudioBackend.isEngine() && window.__engineState === "ready";
    },
    isNativePreferred: function () {
      /* 兼容旧调用方语义：仅当引擎模式且就绪才判原生可用——
         不再代表"可降级"，引擎模式下永不回退 WebAudio */
      return AudioBackend.isEngineReady();
    }
  };

  SynthEngine.prototype._useNative = function () {
    // 以全局 AudioBackend 为准（引擎模式 + state ready）；引擎模式下
    // 永不回退 WebAudio（严格路由），此方法仅供旧调用方兼容
    return window.AudioBackend && window.AudioBackend.isNativePreferred
      && window.AudioBackend.isNativePreferred();
  };

  /* 确保专用演奏轨（EngineBridge.PERF_TRACK）的内置波形声部与本实例参数
     一致（合成波音色的原生渲染路径，不依赖 SF2）。参数签名变化才重发，
     避免每个按键一次 IPC。失败按 3s 冷却重试（引擎冷启动未就绪属瞬态），
     引擎崩溃重启后的声部恢复由 supervisor 重放兜底。
     引擎模式下本方法仅作"预热"（见 noteOn）：未确认期间也直发，首音
     瞬态可接受——绝不回退 WebAudio */
  SynthEngine.prototype._ensureNativeVoice = function () {
    if (this._voiceRetryAt && Date.now() < this._voiceRetryAt) return false;
    if (!window.EngineBridge || !window.EngineBridge.setTrackVoice || !window.EngineBridge.noteOnTrack) return false;
    var sig = [this.waveform, this.attack, this.decay, this.sustain, this.release,
      this.cutoff, this.resonance, this.volume].join("|");
    if (this._lastVoiceSig === sig) return true;
    var self = this;
    try {
      window.EngineBridge.setTrackVoice(window.EngineBridge.PERF_TRACK, {
        wave: this.waveform,
        attack: this.attack,
        decay: this.decay,
        sustain: this.sustain,
        release: this.release,
        cutoff: this.cutoff,
        resonance: this.resonance,
        gain: this.volume
      }).then(function () {
        self._lastVoiceSig = sig;
        self._voiceRetryAt = 0;
      }).catch(function (e) {
        console.warn("[SynthEngine] setTrackVoice 失败，3s 后重试:", e);
        self._lastVoiceSig = null;
        self._voiceRetryAt = Date.now() + 3000;
      });
    } catch (e) {
      return false;
    }
    return false;
  };


  SynthEngine.prototype.setWaveform = function (type) {
    if (["sine", "triangle", "square", "sawtooth"].indexOf(type) !== -1) {
      this.waveform = type;
    }
  };

  /* 引擎模式声部参数防抖同步：音量/滤波改动后 50ms 合并重发 setTrackVoice
     ——即时生效（首个音符即用新参数）；参数一致时引擎侧 no-op，不打断
     正响音符 */
  SynthEngine.prototype._debouncedVoiceSync = function () {
    var self = this;
    if (this._voiceSyncTimer) clearTimeout(this._voiceSyncTimer);
    this._voiceSyncTimer = setTimeout(function () {
      self._voiceSyncTimer = null;
      self._lastVoiceSig = null;   // 签名失效：强制重发
      self._ensureNativeVoice();
    }, 50);
  };

  SynthEngine.prototype.setVolume = function (vol) {
    this.volume = Math.max(0, Math.min(1, vol));
    /* 引擎模式：重发 setTrackVoice（声部签名含 gain） */
    if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) {
      this._debouncedVoiceSync();
      return;
    }
    if (this.masterGain && this.ctx) {
      this.masterGain.gain.setTargetAtTime(this.isMuted ? 0 : this.volume, this.ctx.currentTime, 0.01);
    }
  };

  SynthEngine.prototype.setFilter = function (cutoff, res) {
    this.cutoff = Math.max(100, Math.min(20000, cutoff));
    if (res !== undefined) this.resonance = Math.max(0.1, Math.min(20, res));
    /* 引擎模式：同 setVolume */
    if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) {
      this._debouncedVoiceSync();
      return;
    }
    if (this.filterNode && this.ctx) {
      this.filterNode.frequency.setTargetAtTime(this.cutoff, this.ctx.currentTime, 0.01);
      this.filterNode.Q.setTargetAtTime(this.resonance, this.ctx.currentTime, 0.01);
    }
  };

  SynthEngine.prototype.noteOn = function (midiNote, velocity, when) {
    // 引擎模式：仅原生路径（严格，不降级）。原生路径仅限实时演奏
    // （when===undefined）：带 when 的预调度（编曲引擎）不经此路——
    // 原生桥不支持 when 且每音符 IPC 往返会阻塞主线程。
    // 未就绪/声部未确认时也直发（首音瞬态可接受）；引擎未 ready 由
    // 播放门控拦截，这里兜底静默，绝不落 WebAudio
    if (when === undefined && window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) {
      if (!window.AudioBackend.isEngineReady()) return;
      try {
        this._ensureNativeVoice();   // 预热声部（签名不变时为 no-op）
        window.EngineBridge.noteOnTrack(window.EngineBridge.PERF_TRACK, midiNote, Math.round(velocity !== undefined ? velocity : 100));
        this._nativeVoices[midiNote] = (this._nativeVoices[midiNote] || 0) + 1;
      } catch (e) {
        console.warn("[SynthEngine] 原生 noteOn 失败:", e);
      }
      return;
    }

    this.resume();
    if (!this.ctx || this.isMuted) return;

    var vel = (velocity !== undefined ? velocity : 100) / 127;
    vel = Math.max(0.01, Math.min(1, vel));

    var startTime = when !== undefined ? when : this.ctx.currentTime;

    var osc = this.ctx.createOscillator();
    var gain = this.ctx.createGain();

    osc.type = this.waveform;
    osc.frequency.setValueAtTime(this.midiToFreq(midiNote), startTime);

    // ADSR 包络
    var peakGain = vel * 0.45;
    var sustainGain = peakGain * this.sustain;

    gain.gain.setValueAtTime(0.0001, startTime);
    gain.gain.exponentialRampToValueAtTime(Math.max(0.0001, peakGain), startTime + Math.max(0.005, this.attack));
    gain.gain.exponentialRampToValueAtTime(Math.max(0.0001, sustainGain), startTime + Math.max(0.005, this.attack) + Math.max(0.01, this.decay));

    osc.connect(gain);
    gain.connect(this.filterNode || this.masterGain);

    osc.start(startTime);

    /* 同音声部入队（此前单槽覆盖：同音重叠互相顶掉，和弦/琶音丢声部） */
    var voices = this.activeVoices[midiNote];
    if (!voices) {
      voices = this.activeVoices[midiNote] = [];
    }
    voices.push({
      osc: osc,
      gain: gain,
      startTime: startTime,
      peakGain: peakGain
    });
  };

  SynthEngine.prototype.noteOff = function (midiNote, when) {
    // 引擎模式：与 noteOn 同轨（严格，不降级）
    if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) {
      if (!this._nativeVoices[midiNote]) return;
      var left = --this._nativeVoices[midiNote];
      if (left <= 0) delete this._nativeVoices[midiNote];
      try { window.EngineBridge.noteOffTrack(window.EngineBridge.PERF_TRACK, midiNote); } catch (e) {}
      return;
    }

    var voices = this.activeVoices[midiNote];
    if (!voices || !voices.length || !this.ctx) return;

    // FIFO：最早开始的同音声部先结束
    var voice = voices.shift();
    if (!voices.length) delete this.activeVoices[midiNote];

    var stopTime = when !== undefined ? when : this.ctx.currentTime;
    var releaseTime = Math.max(0.01, this.release);

    try {
      /* 远期停音（编曲/走带预排：noteOn 与 noteOff 同拍调用，stopTime 在未来）
         不能用 gain.value 快照——那是「现在」的瞬时值（新节点 ≈ 0），
         也不能用 cancelAndHoldAtTime——WebView2 内核对远期时刻的 hold
         行为不正确（实测把音符提前切断：每音只响 attack+decay 即静默，
         听感即"粒子效果器式"断续；代码注释中"1.2s 前瞻实验全静默"同源）。
         正解：cancelScheduledValues(0) 清掉 noteOn 排的完整包络后，
         用 voice 记录的包络参数（startTime/peakGain）确定性重放
         attack/decay/sustain 到 stopTime，再接 release 衰减。 */
      var g = voice.gain.gain;
      var now = this.ctx.currentTime;
      g.cancelScheduledValues(0);
      if (stopTime <= now + 0.005) {
        // 即时/已过期停音：当前瞬时值即正确起点
        stopTime = Math.max(stopTime, now);
        g.setValueAtTime(Math.max(0.0001, g.value), stopTime);
      } else {
        var X = voice.startTime;
        var a = Math.max(0.005, this.attack);
        var d = Math.max(0.01, this.decay);
        var peak = Math.max(0.0001, voice.peakGain);
        var sus = Math.max(0.0001, peak * this.sustain);
        g.setValueAtTime(0.0001, X);
        if (stopTime >= X + a + d) {
          // 完整 attack+decay 后维持 sustain 至 stopTime
          g.exponentialRampToValueAtTime(peak, X + a);
          g.exponentialRampToValueAtTime(sus, X + a + d);
          g.setValueAtTime(sus, stopTime);
        } else if (stopTime >= X + a) {
          // stopTime 落在 decay 段：按指数曲线取该时刻的包络值
          var fDecay = (stopTime - X - a) / d;
          g.exponentialRampToValueAtTime(peak, X + a);
          g.exponentialRampToValueAtTime(Math.max(0.0001, peak * Math.pow(sus / peak, fDecay)), stopTime);
        } else {
          // stopTime 落在 attack 段：按指数曲线取该时刻的包络值
          var fAtt = (stopTime - X) / a;
          g.exponentialRampToValueAtTime(Math.max(0.0001, 0.0001 * Math.pow(peak / 0.0001, fAtt)), stopTime);
        }
      }
      g.exponentialRampToValueAtTime(0.00001, stopTime + releaseTime);
      voice.osc.stop(stopTime + releaseTime + 0.05);

      var nowRef = now;
      setTimeout(function () {
        try {
          voice.osc.disconnect();
          voice.gain.disconnect();
        } catch (e) {}
      }, Math.max(0, (stopTime + releaseTime - nowRef)) * 1000 + 150);
    } catch (e) {
      try { voice.osc.stop(stopTime); } catch (err) {}
    }
  };

  SynthEngine.prototype.stopAll = function () {
    var self = this;

    // 撤销未触发的引擎 click 定时器（停止后不得再有幽灵 click）
    if (this._clickTimers) {
      for (var ci = 0; ci < this._clickTimers.length; ci++) clearTimeout(this._clickTimers[ci]);
      this._clickTimers = [];
    }

    // 原生路径：对仍在发声的音符逐个 noteOff（与 noteOn 同轨）
    if (this._nativeVoices) {
      Object.keys(this._nativeVoices).forEach(function (note) {
        try { window.EngineBridge.noteOffTrack(window.EngineBridge.PERF_TRACK, parseInt(note, 10)); } catch (e) {}
      });
      this._nativeVoices = {};
    }

    Object.keys(this.activeVoices).forEach(function (note) {
      var voices = self.activeVoices[note];
      for (var i = 0; i < voices.length; i++) {
        (function (voice) {
          try {
            voice.gain.gain.cancelScheduledValues(self.ctx.currentTime);
            voice.gain.gain.setValueAtTime(Math.max(0.0001, voice.gain.gain.value), self.ctx.currentTime);
            voice.gain.gain.exponentialRampToValueAtTime(0.00001, self.ctx.currentTime + 0.04);
            voice.osc.stop(self.ctx.currentTime + 0.08);
            setTimeout(function () {
              try { voice.osc.disconnect(); voice.gain.disconnect(); } catch (e) {}
            }, 120);
          } catch (e) {
            try { voice.osc.stop(self.ctx.currentTime); } catch (err) {}
          }
        })(voices[i]);
      }
      delete self.activeVoices[note];
    });
    this.activeVoices = {};
  };

  // 节拍器木鱼/电子 Click 音效
  SynthEngine.prototype.playClick = function (isHigh, when) {
    /* 引擎模式：引擎侧合成 click（极短包络木鱼音）。IPC 无排程参数，
       带 when（钢琴窗前瞻窗内调用）时按计划时刻延迟触发——此前立即
       触发使每拍提前 0~150ms 且不均匀（抢拍）。定时器登记到
       _clickTimers，stopAll 时统一撤销（防停止后幽灵 click） */
    if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) {
      if (!window.AudioBackend.isEngineReady()) return;
      var self = this;
      var fire = function () {
        try { window.EngineBridge.click(window.EngineBridge.PERF_TRACK, !!isHigh); } catch (e) {}
      };
      if (when === undefined) { fire(); return; }
      var nowMs = (window.SharedAudio ? window.SharedAudio.now() : (performance.now() / 1000)) * 1000;
      var delayMs = Math.max(0, when * 1000 - nowMs);
      var timer = setTimeout(fire, delayMs);
      (this._clickTimers = this._clickTimers || []).push(timer);
      return;
    }
    this.resume();
    if (!this.ctx || this.isMuted) return;

    var t = when !== undefined ? when : this.ctx.currentTime;
    var osc = this.ctx.createOscillator();
    var gain = this.ctx.createGain();

    osc.type = "sine";
    osc.frequency.setValueAtTime(isHigh ? 1600 : 900, t);
    osc.frequency.exponentialRampToValueAtTime(100, t + 0.04);

    gain.gain.setValueAtTime(isHigh ? 0.4 : 0.25, t);
    gain.gain.exponentialRampToValueAtTime(0.0001, t + 0.04);

    osc.connect(gain);
    gain.connect(this.masterGain);

    osc.start(t);
    osc.stop(t + 0.05);
  };

  window.SynthEngine = SynthEngine;
})(window);
