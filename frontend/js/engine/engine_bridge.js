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
    }
  };

  window.EngineBridge = EngineBridge;
})(window);
