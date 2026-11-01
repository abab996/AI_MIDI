/* 页面启动引导（index/chat/settings 共用，head 内同步加载、不带 defer）。
 *
 * ① 主题防闪烁：CSS 应用前同步设置已保存主题（theme.js 在 DOMContentLoaded
 *    才生效，暗蓝用户每次导航会先闪暖纸）；
 * ② 导航方向：读 nav-dir 标记并消费（各页转场动效读 data-nav）。
 *
 * 原为各页 <head> 内联脚本——CSP 收紧（script-src 去 'unsafe-inline'）后搬出。
 */
(function (window     ) {
  "use strict";
  try {
    var themeKey = "melodymate-blueprint-theme";
    if (localStorage.getItem(themeKey) == null) {
      var oldTheme = localStorage.getItem("ai-midi-blueprint-theme");
      if (oldTheme != null) localStorage.setItem(themeKey, oldTheme);
    }
    if (localStorage.getItem(themeKey) === "dark") {
      document.documentElement.dataset.theme = "dark";
    }
  } catch (e) {}
  try {
    var dir = sessionStorage.getItem("melodymate-nav-dir");
    sessionStorage.removeItem("melodymate-nav-dir");
    if (dir === "forward" || dir === "back") {
      var html = document.documentElement;
      html.dataset.nav = dir;
      /* data-nav 必须保留到转场动画真正结束：固定 800ms 计时从 head 脚本执行
         算起，重页面（chat.html）首帧较晚，计时会在动画中途触发——选择器失配
         后浏览器改用默认交叉淡化并重启旧快照动画，表现为"闪回原页面" */
      var clearNav = function () { html.removeAttribute("data-nav"); };
      if ("onpagereveal" in window) {
        window.addEventListener("pagereveal", function (e     ) {
          if (e.viewTransition) e.viewTransition.finished.finally(clearNav);
          else clearNav();
        }, { once: true });
      } else {
        setTimeout(clearNav, 800);
      }
    }
  } catch (e) {}
})(window);
