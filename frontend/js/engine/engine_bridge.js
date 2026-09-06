/* 原生音频引擎桥（M2）：Wails 绑定直达 supervisor，绕过 HTTP。
   available=false 时（浏览器模式/绑定缺失）调用方自动回退 Web Audio。 */
(function (window) {
  "use strict";

  var app = window.go && window.go.app && window.go.app.App;

  /* 钢琴窗/实时键盘专用引擎轨：编曲轨用 0..N（SF2/波形声部各自切换），
     演奏路径独占 31，避免与编曲轨 0 的 SF2 互相覆盖（引擎共 32 轨） */
  var PERF_TRACK = 31;

  var EngineBridge = {
    available: !!app,
    PERF_TRACK: PERF_TRACK,
    /* 切换轨道到内置波形声部（合成波音色的原生渲染路径，不依赖 SF2）。
       voice: {wave, attack, decay, sustain, release, cutoff, resonance, gain}。
       引擎崩溃重启后由 supervisor 重放，无需前端感知 */
    setTrackVoice: function (track, voice) {
      if (app && app.EngineSetTrackVoice) { return app.EngineSetTrackVoice(track, voice || {}); }
      return Promise.reject(new Error("engine unavailable"));
    },
    noteOn: function (channel, key, velocity) {
      if (app) { return app.EngineNoteOn(channel, key, velocity); }
    },
    noteOff: function (channel, key) {
      if (app) { return app.EngineNoteOff(channel, key); }
    },
    loadSoundFont: function (path, track) {
      if (app) {
        if (track !== undefined && app.EngineLoadSoundFontTrack) {
          return app.EngineLoadSoundFontTrack(track, path);
        }
        return app.EngineLoadSoundFont(path);
      }
      return Promise.reject(new Error("engine unavailable"));
    },
    noteOnTrack: function (track, key, velocity) {
      if (app && app.EngineNoteOnTrack) { return app.EngineNoteOnTrack(track, key, velocity); }
      if (app) { return app.EngineNoteOn(0, key, velocity); }
    },
    noteOffTrack: function (track, key) {
      if (app && app.EngineNoteOffTrack) { return app.EngineNoteOffTrack(track, key); }
      if (app) { return app.EngineNoteOff(0, key); }
    },
    setTrackMix: function (track, gain, pan, mute, solo, active) {
      if (app) {
        return app.EngineSetTrackMix(
          track,
          gain !== undefined ? gain : 1.0,
          pan !== undefined ? pan : 0.0,
          !!mute,
          !!solo,
          active !== undefined ? !!active : true
        );
      }
      return Promise.resolve();
    },
    /* ===== 走带控制（M3 阶段一）===== */
    play: function () {
      if (app) { return app.EnginePlay(); }
      return Promise.reject(new Error("engine unavailable"));
    },
    stop: function () {
      if (app) { return app.EngineStop(); }
      return Promise.reject(new Error("engine unavailable"));
    },
    locate: function (beat) {
      if (app) { return app.EngineLocate(beat); }
      return Promise.reject(new Error("engine unavailable"));
    },
    setTempo: function (bpm) {
      if (app) { return app.EngineSetTempo(bpm); }
      return Promise.reject(new Error("engine unavailable"));
    },
    getTimecode: function () {
      if (app) { return app.EngineGetTimecode(); }
      return Promise.reject(new Error("engine unavailable"));
    },
    scheduleSamples: function (clips, bpm) {
      if (app && app.EngineScheduleSamples) { return app.EngineScheduleSamples(clips, bpm); }
      return Promise.reject(new Error("engine unavailable"));
    },
    clearSamples: function () {
      if (app && app.EngineClearSamples) { return app.EngineClearSamples(); }
      return Promise.reject(new Error("engine unavailable"));
    },
    scheduleNotes: function (notes, bpm) {
      if (app && app.EngineScheduleNotes) { return app.EngineScheduleNotes(notes, bpm); }
      return Promise.reject(new Error("engine unavailable"));
    },
    bounce: function (params) {
      if (app && app.EngineBounce) { return app.EngineBounce(params); }
      return Promise.reject(new Error("engine unavailable"));
    },
    setLoop: function (on, start, end) {
      if (app && app.EngineSetLoop) { return app.EngineSetLoop(on, start, end); }
      return Promise.reject(new Error("engine unavailable"));
    },
    getLevels: function () {
      if (app && app.EngineGetLevels) { return app.EngineGetLevels(); }
      return Promise.reject(new Error("engine unavailable"));
    },
    /* 全音符停止（卡音逃生口）。Wails 绑定缺失（浏览器模式）时回退 HTTP。 */
    panic: function () {
      if (app && app.EnginePanic) { return app.EnginePanic(); }
      return fetch("/api/audio/panic", { method: "POST" }).then(function (resp) {
        if (!resp.ok) throw new Error("panic failed: " + resp.status);
      });
    }
  };

  // 后端模式辅助：统一以 Go settings.audio.backend 为准，localStorage 仅作离线缓存
  // 引擎未就绪时自动回退 Web（避免有绑定无声的假阳性）
  EngineBridge.getBackend = function () {
    try {
      if (window.__engineBackend) return window.__engineBackend;
      var v = localStorage.getItem("ai_midi_audio_backend");
      if (v === "webaudio" || v === "auto") return v;
    } catch (e) {}
    return "auto";
  };
  EngineBridge.isNativePreferred = function () {
    var mode = EngineBridge.getBackend();
    if (mode === "webaudio") return false;
    if (window.__engineState && window.__engineState !== "ready") return false;
    return !!EngineBridge.available;
  };

  window.EngineBridge = EngineBridge;

  // timecode 轮询缓存（40ms，引擎为唯一时钟主时前端插值用）- 带 pending 守卫避免堆积
  (function(){
    var last = { beat: 0, samplePos: 0, bpm: 120, playing: false, t: 0 };
    window.__engineTimecode = last;
    var pending = false;
    function tick(){
      if (pending) return;
      if (!EngineBridge.available) return;
      try {
        if (EngineBridge.getBackend() === "webaudio") return;
        if (window.__engineState && window.__engineState !== "ready") return;
      } catch(e) {}
      pending = true;
      EngineBridge.getTimecode().then(function(tc){
        if (tc && typeof tc.beat === "number") {
          last.beat = tc.beat;
          last.samplePos = tc.samplePos;
          last.bpm = tc.bpm;
          last.playing = tc.playing;
          last.t = performance.now() / 1000;
        }
      }).catch(function(){}).then(function(){ pending = false; });
    }
    setInterval(tick, 40);
    setTimeout(tick, 300);
  })();

  // 电平轮询（50ms，替代前端 AnalyserNode）- 带 pending 守卫
  (function(){
    window.__engineLevels = [];
    var pending = false;
    function tick(){
      if (pending) return;
      if (!EngineBridge.available || !EngineBridge.getLevels) return;
      try {
        if (EngineBridge.getBackend() === "webaudio") return;
        if (window.__engineState && window.__engineState !== "ready") return;
      } catch(e) {}
      pending = true;
      EngineBridge.getLevels().then(function(arr){
        if (Array.isArray(arr)) window.__engineLevels = arr;
      }).catch(function(){}).then(function(){ pending = false; });
    }
    setInterval(tick, 50);
    setTimeout(tick, 500);
  })();
})(window);
