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
    loadSoundFont: function (path) {
      if (app) { return app.EngineLoadSoundFont(path); }
      return Promise.reject(new Error("engine unavailable"));
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
    }
  };

  window.EngineBridge = EngineBridge;
})(window);
