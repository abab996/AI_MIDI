/* 多轮对话页（chat.html）页面级脚本。
 *
 * ① 引擎常驻徽章：桌面版固定走原生引擎（不降级），浏览器模式走 WebAudio
 *    兼容层。以 defer 挂在脚本链末尾（engine_bridge.js 之后）。
 * ② 工具弹窗滑杆的数值回显：原为 4 处 oninput 行内事件处理器，CSP 收紧
 *    （script-src 去 'unsafe-inline'）后改这里 addEventListener。
 */
(function (window     ) {
  "use strict";

  /* ── 引擎徽章 ── */
  const badge = document.getElementById("engineStamp");
  if (badge) {
    var lastState      = null;
    var updateBadge = function () {
      var statusOk = true;
      fetch("/api/audio/status").then(function (r) { return r.json(); }).catch(function () { statusOk = false; return {}; })
        .then(function (st     ) {
          st = st || {};
          /* 取状态失败时保留上一次已知状态：写入 unknown 会让播放中的失联
             判定（isEngineDown）把一次 HTTP 抖动当成引擎掉线，掐断正在播放的
             走带。首次加载即失败时保持未取到（undefined），判定侧同样忽略 */
          var state = statusOk ? (st.state || "unknown") : (window.__engineState || "unknown");
          if (statusOk && st.replay_errors && st.replay_errors.length) state = "failed";
          /* 浏览器模式（无 Wails 桥）：没有引擎通道，实际发声恒为 WebAudio
             兼容层——徽章必须如实反映，不得显示引擎就绪 */
          var noBridge = !(window.EngineBridge && window.EngineBridge.available);
          var text = "";
          var cls = "stamp";
          if (noBridge) {
            text = "WEB ○";
            cls = "stamp warn";
            badge.title = "浏览器模式（无引擎桥）· 全部走 Web Audio 兼容层（桌面应用内为原生引擎）";
          } else {
            if (state === "ready") {
              text = "ENGINE ●"; cls = "stamp ok";
              var sfNote = (st.soundfont_loaded === false) ? " · 未加载音色（SF2 轨静音）" : "";
              /* P-H3：单插件告警不改变引擎就绪状态（播放仍可用），只在提示里列出 */
              var pluginNote = "";
              if (st.plugin_warnings && st.plugin_warnings.length) {
                pluginNote = " · " + st.plugin_warnings.length + " 个插件告警（其余轨道正常）";
                cls = "stamp warn";
              }
              badge.title = (st.device_summary || "引擎就绪") + sfNote + pluginNote +
                (st.plugin_warnings && st.plugin_warnings.length ? "\n" + st.plugin_warnings.join("\n") : "") +
                " · 全链路原生引擎";
            }
            else if (state === "starting" || state === "restarting") { text = "ENGINE …"; cls = "stamp warn"; badge.title = "引擎启动中（播放暂不可用）"; }
            else if (state === "failed" || state === "stopped" || state === "disabled") {
              text = "ENGINE ✗"; cls = "stamp warn";
              badge.title = "音频引擎不可用（不降级，播放无声）：" + (st.last_error || state) + "；请检查声卡设置或重启应用";
            }
            else { text = "ENGINE ?"; cls = "stamp"; badge.title = "引擎状态：" + state; }
          }
          badge.textContent = "ENGINE: " + text;
          badge.className = cls;
          var prevState = window.__engineState;
          window.__engineState = state;
          // 卡音兜底：引擎从就绪转为不可用时，之前经原生路径发出的音符没有
          // 对应的前端 voice 可停——清引擎侧残留
          if (prevState === "ready" && state !== "ready" && window.EngineBridge && window.EngineBridge.panic) {
            try { window.EngineBridge.panic().catch(function () {}); } catch (e) {}
          }
          // 失败转态 toast（仅一次）：严格路由不降级，明确告知。
          // 浏览器模式（无桥）不弹此提示（WebAudio 兼容层从未失效）
          if (state === "failed" && lastState !== "failed" && !noBridge) {
            if (window.UI && window.UI.toast) window.UI.toast("⚠ 音频引擎不可用（不降级，播放无声）。请检查声卡设置或重启应用", "warn");
          }
          // 引擎恢复（starting/restarting → ready）转态提示一次（此前恢复全程静默）
          if (state === "ready" && (lastState === "starting" || lastState === "restarting" || lastState === "failed") && !noBridge) {
            if (window.UI && window.UI.toast) window.UI.toast("✓ 音频引擎已就绪（播放可用）", "ok");
            /* S-L1：新引擎进程没有旧的调度表/音源，前端只在播放/编辑时重发，
               恢复后若不补发，音频 clip 与 SF2 轨会静音到下次播放。 */
            var ctrl      = (window       ).Arrange;
            if (ctrl) {
              try { if (ctrl.engine && ctrl.engine.invalidateNativeSources) ctrl.engine.invalidateNativeSources(); } catch (e) {}
              try {
                ctrl._lastSampleSig = "";
                if (ctrl.sendSampleSchedule) ctrl.sendSampleSchedule();
              } catch (e) {}
            }
          }
          lastState = state;
        });
    };
    updateBadge();
    /* 轮询仅用于徽章显示：5s 常驻请求过密（与 tasks.ts 的自适应降频不一致），
       放宽到 15s；页面隐藏（后台标签）时暂停，回到前台立即刷新一次 */
    var badgeTimer      = setInterval(updateBadge, 15000);
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) {
        clearInterval(badgeTimer);
        badgeTimer = null;
      } else {
        updateBadge();
        if (!badgeTimer) badgeTimer = setInterval(updateBadge, 15000);
      }
    });
    // 后端设置项已移除（固定原生引擎），storage 跨页同步监听一并去掉
    // 暴露给设置页调用（音频设置变更后立即刷新徽章）
    window.__refreshEngineBadge = updateBadge;
  }

  /* ── 工具弹窗滑杆数值回显 ── */
  var bindSlider = function (id        , fmt                       ) {
    var input = document.getElementById(id)       ;
    if (!input) return;
    var update = function () {
      var next = input.nextElementSibling;
      if (next) next.textContent = fmt(input.value);
    };
    input.addEventListener("input", update);
    update();
  };
  bindSlider("lsMultiply", function (v) { return v + "x"; });
  bindSlider("lsOffset", function (v) { return (Number(v) > 0 ? "+" : "") + v; });
  bindSlider("strumTime", function (v) { return v + " ticks"; });
  bindSlider("strumVelRamp", function (v) { return v; });
})(window);
