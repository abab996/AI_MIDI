/* 全局共享 AudioContext 单例。
   浏览器允许同时存在的 AudioContext 数量有限（约 6 个），且每个 context
   独立的音频渲染线程带来额外延迟与内存。此前 synth / soundfont /
   audio_engine 各自 new 一个（最多 3 个并存），时钟基准还不一致——
   跨引擎调度（钢琴窗按 ctx 时钟排音符到 soundfont）需要统一时间轴。
   惰性创建；必须在与 SharedAudio 同源的时间轴上取 currentTime。 */
(function (window) {
  "use strict";
  var ctx = null;

  window.SharedAudio = {
    get: function () {
      if (!ctx) {
        var AC = window.AudioContext || window.webkitAudioContext;
        if (AC) {
          try { ctx = new AC(); } catch (e) { ctx = null; }
        }
      }
      return ctx;
    },
    resume: function () {
      var c = this.get();
      if (c && c.state === "suspended") return c.resume();
      return Promise.resolve();
    },
    now: function () {
      var c = this.get();
      return c ? c.currentTime : performance.now() / 1000;
    }
  };
})(window);
