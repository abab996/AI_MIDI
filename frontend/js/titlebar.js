/* 自绘标题栏（仅 Wails 桌面模式）
   系统边框已由 main.go 的 Frameless 移除，本脚本在 body 顶部注入一条
   40px 标题栏：主题化 Logo + 软件名 + 最小化/最大化/关闭按钮，与应用的
   蓝图风格统一。浏览器模式（-browser 或真浏览器访问）window.runtime 不
   存在 → 整体不渲染，页面外观与带系统边框时完全一致。

   拖拽移动与边缘缩放走 Wails 内置机制：runtime.js 在 mousedown 时读取
   计算样式 --wails-draggable（drag/no-drag），无需自写窗口消息。
   v2.15 无 window:maximise 事件，最大化图标在点击/resize/focus 后查
   WindowIsMaximised() 同步。 */
(function () {
  "use strict";

  var GLYPH_MIN = "─";      // U+2500
  var GLYPH_MAX = "□";      // U+25A1
  var GLYPH_RESTORE = "❐";  // U+2750
  var GLYPH_CLOSE = "✕";    // U+2715

  /* 桌面能力探测：Wails runtime 存在即认为是桌面模式（frameless 窗口
     需要自绘标题栏）。而不是"四个方法全有才渲染"的全有全无判定——
     若未来 runtime 升级/重新 generate 缺了某个方法，标题栏整体不渲染
     会让无边框窗口没有任何关闭/最小化控件（只能任务管理器杀进程）。
     缺失的方法按按钮粒度隐藏：按钮回调在 isDesktop 阶段已确认存在，
     不会出现"点了没反应"的悬空按钮。 */
  var rtWin = null;

  function isDesktop() {
    var rt = window.runtime;
    if (!rt) return false;
    rtWin = rt;
    return true;
  }

  function hasRuntime(method) {
    return !!(rtWin && typeof rtWin[method] === "function");
  }

  var maxBtn = null;
  var syncTimer = null;

  function syncMaxIcon() {
    if (!maxBtn || !hasRuntime("WindowIsMaximised")) return;
    Promise.resolve(rtWin.WindowIsMaximised()).then(function (max) {
      var on = !!max;
      maxBtn.textContent = on ? GLYPH_RESTORE : GLYPH_MAX;
      maxBtn.title = on ? "向下还原" : "最大化";
    }).catch(function () {});
  }

  /* resize 后窗口状态才稳定，防抖再查；focus 兜底（Win+方向键等系统操作） */
  function scheduleSync() {
    clearTimeout(syncTimer);
    syncTimer = setTimeout(syncMaxIcon, 150);
  }

  function makeBtn(cls, glyph, title, fn) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "title-bar-btn " + cls;
    b.textContent = glyph;
    b.title = title;
    b.tabIndex = -1;   /* 标题栏按钮不参与 Tab 序列，避免打断表单操作 */
    b.addEventListener("click", fn);
    return b;
  }

  /* 窗口控制调用统一包一层 try/catch：runtime 在关闭竞态/假死时可能
     同步抛异常，落入 window.onerror 产生无意义的 toast 噪音 */
  function safeRuntime(fn) {
    try {
      fn();
    } catch (e) { /* 静默：窗口控制失败无业务可恢复 */ }
  }

  function buildBar() {
    var bar = document.createElement("div");
    bar.className = "title-bar";
    bar.id = "titleBar";

    var logo = document.createElement("img");
    logo.className = "app-logo-img title-bar-logo";
    logo.alt = "";
    /* theme.js 的 applyTheme 统一刷新 .app-logo-img；先按当前主题设初值 */
    logo.src = document.documentElement.dataset.theme === "dark"
      ? "/app_icon_dark.png" : "/app_icon_warm.png";
    bar.appendChild(logo);

    var name = document.createElement("span");
    name.className = "title-bar-name";
    name.id = "titleBarName";
    name.innerHTML = "<b>AI_MIDI</b>";
    bar.appendChild(name);

    var spacer = document.createElement("span");
    spacer.className = "title-bar-spacer";
    bar.appendChild(spacer);

    var actions = document.createElement("div");
    actions.className = "title-bar-actions";
    /* 按能力渲染按钮：缺失的 runtime 方法（升级/重新 generate 后
       可能不存在）对应按钮直接不渲染，而不是整条标题栏消失 */
    if (hasRuntime("WindowMinimise")) {
      actions.appendChild(makeBtn("tb-min", GLYPH_MIN, "最小化", function () {
        safeRuntime(function () { rtWin.WindowMinimise(); });
      }));
    }
    if (hasRuntime("WindowToggleMaximise")) {
      maxBtn = makeBtn("tb-max", GLYPH_MAX, "最大化", function () {
        safeRuntime(function () { rtWin.WindowToggleMaximise(); });
        setTimeout(syncMaxIcon, 60);
      });
      actions.appendChild(maxBtn);
    }
    if (hasRuntime("Quit")) {
      actions.appendChild(makeBtn("tb-close", GLYPH_CLOSE, "关闭", function () {
        safeRuntime(function () { rtWin.Quit(); });
      }));
    }
    bar.appendChild(actions);

    /* 双击标题栏（按钮区外）= 最大化/还原，与 Windows 惯例一致 */
    bar.addEventListener("dblclick", function (e) {
      if (e.target.closest(".title-bar-actions") || !hasRuntime("WindowToggleMaximise")) return;
      safeRuntime(function () { rtWin.WindowToggleMaximise(); });
      setTimeout(syncMaxIcon, 60);
    });

    return bar;
  }

  function loadName() {
    fetch("/api/version").then(function (r) {
      return r.ok ? r.json() : null;
    }).then(function (v) {
      var el = document.getElementById("titleBarName");
      if (!el || !v || !v.name) return;
      // 用 DOM API 而非 innerHTML 拼接：name 虽来自服务端常量，但
      // 养成"外部文本必 textContent"的习惯，未来若 name 可配置不会
      // 引入注入点
      var parts = String(v.name).split(" · ");
      el.textContent = "";
      var b = document.createElement("b");
      b.textContent = parts[0] || "";
      el.appendChild(b);
      if (parts[1]) {
        var sub = document.createElement("span");
        sub.className = "title-bar-sub";
        sub.textContent = " · " + parts[1];
        el.appendChild(sub);
      }
    }).catch(function () {});
  }

  function init() {
    if (!isDesktop()) return;
    document.documentElement.classList.add("has-titlebar");
    document.body.insertBefore(buildBar(), document.body.firstChild);
    syncMaxIcon();
    loadName();
    window.addEventListener("resize", scheduleSync);
    window.addEventListener("focus", syncMaxIcon);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
