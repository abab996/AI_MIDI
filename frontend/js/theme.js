/* AI_MIDI 蓝图主题与冷暖双色调联动控制 (含至少 1.2 秒开屏启动图控制) */
(function () {
  "use strict";

  var root = document.documentElement;
  var THEME_KEY = "ai-midi-blueprint-theme";
  var switchTimer = null;

  function themeButtons() {
    return Array.prototype.slice.call(document.querySelectorAll(".theme-btn"));
  }

  function updateIconsAndSplash(theme) {
    // 1. 更新网页 Favicon (冷蓝 vs 暖纸)
    var favicon = document.querySelector('link[rel="icon"]');
    if (!favicon) {
      favicon = document.createElement("link");
      favicon.rel = "icon";
      favicon.type = "image/x-icon";
      document.head.appendChild(favicon);
    }
    favicon.href = theme === "dark" ? "/favicon_dark.ico" : "/favicon_warm.ico";

    // 2. 更新页面内所有应用徽标图片
    var appLogos = document.querySelectorAll(".app-logo-img, .splash-logo-img");
    appLogos.forEach(function (img) {
      if (img.classList.contains("splash-logo-img")) {
        img.src = theme === "dark" ? "/splash_dark.png" : "/splash_warm.png";
      } else {
        img.src = theme === "dark" ? "/app_icon_dark.png" : "/app_icon_warm.png";
      }
    });

    // 3. 通知 Go 后端切换 Windows 原生标题栏与任务栏图标
    try {
      fetch("/api/theme/switch?theme=" + encodeURIComponent(theme), {
        method: "POST"
      }).catch(function () {});
    } catch (e) {}
  }

  function applyTheme(t) {
    var target = root.dataset.theme === t ? null : t;
    try { localStorage.setItem(THEME_KEY, t); } catch (e) {}

    if (target) {
      if (document.startViewTransition) {
        try {
          var vt = document.startViewTransition(function () {
            root.dataset.theme = target;
          });
          root.classList.add("theme-vt");
          if (vt && vt.finished) {
            vt.finished.finally(function () {
              root.classList.remove("theme-vt");
            });
          } else {
            setTimeout(function () { root.classList.remove("theme-vt"); }, 400);
          }
        } catch (e) {
          root.dataset.theme = target;
        }
      } else {
        root.classList.add("theme-switching");
        root.dataset.theme = target;
        clearTimeout(switchTimer);
        switchTimer = setTimeout(function () {
          root.classList.remove("theme-switching");
        }, 380);
      }
    }

    updateIconsAndSplash(t);

    themeButtons().forEach(function (btn) {
      btn.textContent = t === "dark" ? "\u2600 \u6696\u7EB8" : "\u263E \u6697\u84DD";
      btn.title = t === "dark" ? "切换到暖纸主题" : "切换到暗蓝主题";
    });
  }

  function initTheme() {
    var saved = null;
    try { saved = localStorage.getItem(THEME_KEY); } catch (e) {}
    root.dataset.theme = saved === "dark" ? "dark" : "parchment";
    applyTheme(root.dataset.theme);

    themeButtons().forEach(function (btn) {
      btn.addEventListener("click", function () {
        applyTheme(root.dataset.theme === "dark" ? "parchment" : "dark");
      });
    });

    // 旧版 HTML 闪屏（#splashScreen 元素已随页面改版移除，原生桌面闪屏
    // 由 Go 侧 ShowNativeTransparentSplash 接管）——initSplash 死代码移除
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initTheme);
  } else {
    initTheme();
  }
})();
