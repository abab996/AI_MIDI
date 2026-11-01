/* 页面启动引导（index/chat/settings 共用，head 内同步加载、不带 defer）。
 *
 * ① 主题防闪烁：CSS 应用前同步设置已保存主题（theme.js 在 DOMContentLoaded
 *    才生效，暗蓝用户每次导航会先闪暖纸）；
 * ② 导航方向：读 nav-dir 标记并消费（各页转场动效读 data-nav）。
 *
 * 原为各页 <head> 内联脚本——CSP 收紧（script-src 去 'unsafe-inline'）后搬出。
 */
(function (window: any) {
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
      document.documentElement.dataset.nav = dir;
      setTimeout(function () { document.documentElement.removeAttribute("data-nav"); }, 800);
    }
  } catch (e) {}
})(window);
