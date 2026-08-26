/* 原生音频引擎桥（M2）：Wails 绑定直达 supervisor，绕过 HTTP。
   available=false 时（浏览器模式/绑定缺失）调用方自动回退 Web Audio。 */
(function (window) {
  "use strict";

  var app = window.go && window.go.app && window.go.app.App;

  var EngineBridge = {
    available: !!app,
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

  // timecode 轮询缓存（40ms，引擎为唯一时钟主时前端插值用）
  (function(){
    var last = { beat: 0, samplePos: 0, bpm: 120, playing: false, t: 0 };
    window.__engineTimecode = last;
    function tick(){
      if (!EngineBridge.available) return;
      // 仅 AUTO 且 ready 时才高频拉取，避免 WEBAUDIO 模式无谓开销
      try {
        if (EngineBridge.getBackend() === "webaudio") return;
        if (window.__engineState && window.__engineState !== "ready") return;
      } catch(e) {}
      EngineBridge.getTimecode().then(function(tc){
        if (tc && typeof tc.beat === "number") {
          last.beat = tc.beat;
          last.samplePos = tc.samplePos;
          last.bpm = tc.bpm;
          last.playing = tc.playing;
          last.t = performance.now() / 1000;
        }
      }).catch(function(){});
    }
    setInterval(tick, 40);
    // 首帧立即拉一次
    setTimeout(tick, 300);
  })();
})(window);
