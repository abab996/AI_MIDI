/* 设置页（settings.html）音频面板脚本。
 *
 * 驱动/设备选择、引擎状态轮询、延迟提示、测试音、音频应用锁、设备热插拔
 * 轮询。原为 settings.html 末尾的内联 <script> 块——CSP 收紧（script-src
 * 去 'unsafe-inline'）后搬出；以非 defer 挂在 settings.js / engine_bridge.js
 * 之后，与原内联位置同序。
 */
(function (window     ) {
  "use strict";
  var $ = function (id        )      { return document.getElementById(id); };
  var toneOn = false;

  function updateLatency() {
    var rate = parseInt($("rateSel").value, 10) || 0;
    var buf = parseInt($("bufSel").value, 10) || 0;
    // 若未选则尝试从 device_summary 解析： “@ 48000Hz, buffer=128”
    if (!rate || !buf) {
      var m = ($("engineDetail").textContent || "").match(/(\d+)\s*Hz.*?buffer=(\d+)/i);
      if (m) { rate = parseInt(m[1], 10); buf = parseInt(m[2], 10); }
    }
    var hint = $("latencyHint");
    if (hint) {
      if (rate && buf) {
        var ms = (buf / rate * 1000).toFixed(2);
        hint.textContent = "延迟≈" + ms + "ms";
        hint.title = "理论往返延迟≈ buffer/sampleRate";
      } else {
        hint.textContent = "";
      }
    }
  }

  function refreshStatus() {
    fetch("/api/audio/status").then(function (r) { return r.json(); }).then(function (st     ) {
      var map      = { ready: "✅ 就绪", starting: "⏳ 启动中", restarting: "🔄 重启中",
        failed: "❌ 失败", stopped: "已停止", disabled: "已禁用" };
      $("engineState").textContent = map[st.state] || st.state;
      $("engineDetail").textContent =
        (st.pid ? "pid=" + st.pid + " 重启=" + st.restarts + " " : "") + (st.device_summary || "");
      // 音色库状态（ready 只代表 IPC 通畅；SF2 轨出声还需引擎已加载音色。
      // 合成波音色走内置波形声部，不依赖音色）
      if (st.state === "ready") {
        $("sf2Hint").textContent = st.soundfont_loaded
          ? "🎵 音色库：已加载（SF2 轨走引擎）"
          : "🎵 音色库：空——SF2 轨将逐轨回退浏览器渲染；可将 .sf2 放入 应用目录\\Library\\soundfonts";
      } else {
        $("sf2Hint").textContent = "";
      }
      updateLatency();
    }).catch(function () { $("engineState").textContent = "不可用"; });
  }

  function loadDevices() {
    fetch("/api/audio/devices").then(function (r) { return r.json(); }).then(function (dl     ) {
      var drvSel = $("drvSel"); drvSel.innerHTML = "";
      (dl.drivers || []).forEach(function (d     ) {
        window.__aiDevList[d.driver] = d.devices;
        var o = document.createElement("option");
        o.value = d.driver; o.textContent = d.driver + "（" + d.devices.length + " 设备）";
        drvSel.appendChild(o);
      });
      return fetch("/api/audio/settings").then(function (r) { return r.json(); });
    }).then(function (a     ) {
      if (!a) return;
      if (a.driver) $("drvSel").value = a.driver;
      fillDevs();
      if (a.device) $("devSel").value = a.device;
      $("rateSel").value = a.sample_rate ? String(a.sample_rate) : "";
      $("bufSel").value = a.buffer_size ? String(a.buffer_size) : "";
    }).catch(function () {});
  }

  window.__aiDevList = {};
  function fillDevs() {
    var drv = $("drvSel").value;
    var devSel = $("devSel"); devSel.innerHTML = "";
    (window.__aiDevList[drv] || []).forEach(function (n        ) {
      var o = document.createElement("option");
      o.value = n; o.textContent = n;
      devSel.appendChild(o);
    });
  }
  $("drvSel").addEventListener("change", function () {
    fillDevs();
  });

  var audioApplyLockTimer      = null;
  function setAudioApplyLock(on         ) {
    var el = $("audioApplyLock");
    var wait = $("audioApplyLockWait");
    if (!el) return;
    if (audioApplyLockTimer) {
      clearInterval(audioApplyLockTimer);
      audioApplyLockTimer = null;
    }
    if (on) {
      var started = Date.now();
      if (wait) wait.textContent = "已等待 0 秒";
      el.hidden = false;
      el.classList.remove("modal-out");
      el.classList.add("modal-in");
      audioApplyLockTimer = setInterval(function () {
        if (wait) wait.textContent = "已等待 " + Math.floor((Date.now() - started) / 1000) + " 秒";
      }, 500);
      return;
    }
    el.classList.remove("modal-in");
    el.classList.add("modal-out");
    setTimeout(function () {
      if (!el.classList.contains("modal-out")) return;
      el.hidden = true;
      el.classList.remove("modal-out");
    }, 200);
  }

  $("applyAudioBtn").addEventListener("click", function () {
    var btn = $("applyAudioBtn");
    if (btn.disabled) return;
    var body = {
      driver: $("drvSel").value,
      device: $("devSel").value,
      sample_rate: parseInt($("rateSel").value, 10) || 0,
      buffer_size: parseInt($("bufSel").value, 10) || 0
    };
    btn.disabled = true;
    setAudioApplyLock(true);
    fetch("/api/audio/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }).then(function (r) {
      return r.json().then(function (j     ) { return { ok: r.ok, j: j }; });
    }).then(function (res     ) {
      if (res.j.saved) {
        $("applyHint").textContent = "✅ 已应用";
      } else {
        var detail      = res.j.detail || "失败";
        var low = detail.toLowerCase();
        if (low.indexOf("no server running") >= 0 || low.indexOf("server not running") >= 0 || detail.indexOf("未响应") >= 0)
          detail += "。ASIO Link Pro 需要一台已在运行的服务器；若还没有，重新应用一次，引擎会改由本程序作为服务器打开";
        $("applyHint").textContent = "❌ " + detail;
        if (window.UI && window.UI.toast) window.UI.toast("✗ " + detail, "err");
      }
      setTimeout(function () { $("applyHint").textContent = ""; refreshStatus(); }, 2500);
    }).catch(function () {
      $("applyHint").textContent = "❌ 网络错误";
      if (window.UI && window.UI.toast) window.UI.toast("✗ 应用音频设置失败：网络错误", "err");
    }).then(function () {
      btn.disabled = false;
      setAudioApplyLock(false);
    });
  });

  $("openPanelBtn").addEventListener("click", function () {
    var btn = $("openPanelBtn");
    btn.disabled = true;
    fetch("/api/audio/control-panel", { method: "POST" })
      .then(function (r) { return r.json(); })
      .then(function (j     ) {
        btn.disabled = false;
        if (!j.opened) {
          window.UI.toast("⚠ 当前音频设备不支持弹出硬件控制面板（非 ASIO 设备或驱动未提供控制界面）", "warn");
        }
      })
      .catch(function (err     ) {
        btn.disabled = false;
        window.UI.toast("✗ 打开控制面板失败: " + (err.message || err), "err");
      });
  });

  /* 演奏合成后端选择器已移除：桌面版固定走原生引擎（JUCE），浏览器模式
     自动落 Web Audio 兼容层，不再是用户可选项 */

  $("testToneBtn").addEventListener("click", function () {
    toneOn = !toneOn;
    var btn = $("testToneBtn");
    btn.disabled = true;
    fetch("/api/audio/test-tone", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ on: toneOn })
    })
      .then(function () { btn.disabled = false; })
      .catch(function (err     ) {
        /* 请求失败：回滚开关状态，避免 UI 显示与实际音源状态失同步 */
        toneOn = !toneOn;
        btn.disabled = false;
        window.UI.toast("✗ 测试音请求失败: " + (err.message || err), "err");
      });
  });

  refreshStatus();
  loadDevices();
  setInterval(refreshStatus, 5000);
  // 延迟实时刷新
  $("rateSel").addEventListener("change", updateLatency);
  $("bufSel").addEventListener("change", updateLatency);
  // 热插拔：5s 轮询设备列表，变化时 toast 并刷新下拉
  (function () {
    var lastHash = "";
    function hashDevices(dl     ) {
      try { return JSON.stringify(dl); } catch (e) { return ""; }
    }
    setInterval(function () {
      fetch("/api/audio/devices").then(function (r) { return r.json(); }).then(function (dl     ) {
        var h = hashDevices(dl);
        if (lastHash && h && h !== lastHash) {
          if (window.UI && window.UI.toast) window.UI.toast("🔌 音频设备列表已变化，已自动刷新", "");
          loadDevices();
        }
        lastHash = h;
      }).catch(function () {});
    }, 5000);
    // 首帧初始化 hash
    fetch("/api/audio/devices").then(function (r) { return r.json(); }).then(function (dl     ) { lastHash = hashDevices(dl); }).catch(function () {});
  })();
})(window);
