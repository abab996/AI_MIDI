/* 全局共享 AudioContext 单例。
   浏览器允许同时存在的 AudioContext 数量有限（约 6 个），且每个 context
   独立的音频渲染线程带来额外延迟与内存。此前 synth / soundfont /
   audio_engine 各自 new 一个（最多 3 个并存），时钟基准还不一致——
   跨引擎调度（钢琴窗按 ctx 时钟排音符到 soundfont）需要统一时间轴。
   惰性创建；必须在与 SharedAudio 同源的时间轴上取 currentTime。
   引擎模式（ENGINE 严格路由）不创建 AudioContext：发声全部走原生引擎，
   时钟由 now() 以 performance.now 兜底（与 WebAudio 时钟同为毫秒级）。 */
(function (window) {
  "use strict";
  var ctx = null;

  window.SharedAudio = {
    get: function () {
      /* 引擎模式：任何场景都不得创建 WebAudio 上下文（存在即可能被误用）；
         浏览器模式（无 Wails 桥）经 AudioBackend.mode() 判为 webaudio，不受影响 */
      if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) {
        return null;
      }
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
      if (c && c.state === "suspended") {
        return c.resume().catch(function (e) {
          /* P2-3 诊断：resume 被拒（非用户手势触发等）不应静默 */
          console.warn("[SharedAudio] AudioContext resume 被拒绝:", e);
        });
      }
      return Promise.resolve();
    },
    now: function () {
      var c = this.get();
      return c ? c.currentTime : performance.now() / 1000;
    },
    /* 销毁共享上下文（引擎模式切换时调用）：关闭并置空，
       杜绝"切到 ENGINE 还有 Web 音在响"；切回 webaudio 后由 get 惰性重建 */
    dispose: function () {
      if (ctx) {
        try { ctx.close(); } catch (e) {}
        ctx = null;
      }
    }
  };

  /* P2-3：全局一次性用户手势兜底唤醒。MIDI 触发（noteOn 来自回调而非
     手势）与程序化播放都可能撞上 suspended 状态，浏览器要求用户手势后
     才允许 resume——这里在任何首次 pointerdown/keydown 时统一唤醒；
     resume 幂等（已 running 时为空操作），长期绑定开销可忽略 */
  var gestureBound = false;
  function bindGestureResume() {
    if (gestureBound) return;
    gestureBound = true;
    var wake = function () {
      try { window.SharedAudio.resume(); } catch (e) {}
    };
    try {
      window.addEventListener("pointerdown", wake, { passive: true });
      window.addEventListener("keydown", wake, { passive: true });
    } catch (e) {}
  }
  if (document && document.readyState !== "loading") {
    bindGestureResume();
  } else if (document) {
    document.addEventListener("DOMContentLoaded", bindGestureResume);
  }
})(window);
