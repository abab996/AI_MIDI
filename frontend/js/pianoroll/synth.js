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

  /* ===== 原生音频引擎双后端（M2）=====
     mode："auto"（默认：EngineBridge 可用即走原生 SF2 合成器）|"webaudio"（强制浏览器合成）
     经 localStorage 持久化；原生调用失败自动回退 Web Audio 路径。 */
  var BACKEND_KEY = "ai_midi_audio_backend";
  var _backendMode = (function () {
    try { return localStorage.getItem(BACKEND_KEY) || "auto"; } catch (e) { return "auto"; }
  })();

  window.AudioBackend = {
    getMode: function () { return _backendMode; },
    setMode: function (mode) {
      if (mode !== "auto" && mode !== "webaudio") return;
      _backendMode = mode;
      try { localStorage.setItem(BACKEND_KEY, mode); } catch (e) {}
    },
    isNativeAvailable: function () {
      return !!(window.EngineBridge && window.EngineBridge.available);
    }
  };

  SynthEngine.prototype._useNative = function () {
    return _backendMode === "auto" && window.AudioBackend.isNativeAvailable();
  };


  SynthEngine.prototype.setWaveform = function (type) {
    if (["sine", "triangle", "square", "sawtooth"].indexOf(type) !== -1) {
      this.waveform = type;
    }
  };

  SynthEngine.prototype.setVolume = function (vol) {
    this.volume = Math.max(0, Math.min(1, vol));
    if (this.masterGain && this.ctx) {
      this.masterGain.gain.setTargetAtTime(this.isMuted ? 0 : this.volume, this.ctx.currentTime, 0.01);
    }
  };

  SynthEngine.prototype.setFilter = function (cutoff, res) {
    this.cutoff = Math.max(100, Math.min(20000, cutoff));
    if (res !== undefined) this.resonance = Math.max(0.1, Math.min(20, res));
    if (this.filterNode && this.ctx) {
      this.filterNode.frequency.setTargetAtTime(this.cutoff, this.ctx.currentTime, 0.01);
      this.filterNode.Q.setTargetAtTime(this.resonance, this.ctx.currentTime, 0.01);
    }
  };

  SynthEngine.prototype.noteOn = function (midiNote, velocity, when) {
    // 原生路径：SF2 合成器在引擎进程渲染；when 调度暂不支持（即时发声）
    if (this._useNative()) {
      try {
        window.EngineBridge.noteOn(0, midiNote, Math.round(velocity !== undefined ? velocity : 100));
        this._nativeVoices[midiNote] = (this._nativeVoices[midiNote] || 0) + 1;
        return;
      } catch (e) {
        console.warn("[SynthEngine] 原生 noteOn 失败，回退 Web Audio:", e);
      }
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
    // 原生路径
    if (this._useNative() && this._nativeVoices[midiNote]) {
      var left = --this._nativeVoices[midiNote];
      if (left <= 0) delete this._nativeVoices[midiNote];
      try { window.EngineBridge.noteOff(0, midiNote); } catch (e) {}
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
      voice.gain.gain.cancelScheduledValues(stopTime);
      var currentVal = Math.max(0.0001, voice.gain.gain.value);
      voice.gain.gain.setValueAtTime(currentVal, stopTime);
      voice.gain.gain.exponentialRampToValueAtTime(0.00001, stopTime + releaseTime);
      voice.osc.stop(stopTime + releaseTime + 0.05);

      setTimeout(function () {
        try {
          voice.osc.disconnect();
          voice.gain.disconnect();
        } catch (e) {}
      }, (releaseTime + 0.1) * 1000);
    } catch (e) {
      try { voice.osc.stop(stopTime); } catch (err) {}
    }
  };

  SynthEngine.prototype.stopAll = function () {
    var self = this;

    // 原生路径：对仍在发声的音符逐个 noteOff
    if (this._nativeVoices) {
      Object.keys(this._nativeVoices).forEach(function (note) {
        try { window.EngineBridge.noteOff(0, parseInt(note, 10)); } catch (e) {}
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
