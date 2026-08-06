/* AI_MIDI 蓝图主题 · 主题切换（默认暖纸） */
(function () {
  "use strict";

  var root = document.documentElement;
  var THEME_KEY = "ai-midi-blueprint-theme";
  var switchTimer = null;

  /* 页面上可能有多个主题按钮（档案库/工作台各一个），统一按 class 查找 */
  function themeButtons() {
    return Array.prototype.slice.call(document.querySelectorAll(".theme-btn"));
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

    themeButtons().forEach(function (btn) {
      btn.textContent = t === "dark" ? "\u2600 \u6696\u7EB8" : "\u263E \u6697\u84DD";
      btn.title = t === "dark" ? "切换到暖纸主题" : "切换到暗蓝主题";
    });
  }

  function initTheme() {
    var saved = null;
    try { saved = localStorage.getItem(THEME_KEY); } catch (e) {}
    /* 默认暖纸；用户选择过暗蓝则记住 */
    root.dataset.theme = saved === "dark" ? "dark" : "parchment";
    applyTheme(root.dataset.theme);

    themeButtons().forEach(function (btn) {
      btn.addEventListener("click", function () {
        applyTheme(root.dataset.theme === "dark" ? "parchment" : "dark");
      });
    });
  }

  document.addEventListener("DOMContentLoaded", initTheme);
})();
