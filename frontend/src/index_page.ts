/* 旧快捷操作页（index.html）专用脚本。
 *
 * ① 落点重定向：冷启动不再落在快捷操作页，直达档案库（对话工作台为主
 *    入口）。仅当"带导航标记进入"（从应用内页面显式点过来）或本会话已
 *    展示过本页（刷新）时正常展示。-browser 模式默认本就直达 chat.html，
 *    这里只兜住手动访问 "/" 的情况。
 * ② 一次性集成提示：本页功能已集成进多轮对话页的「快速任务」，每个
 *    会话提示一次（避免反复打扰）。
 *
 * 原为 index.html 内联脚本——CSP 收紧后搬出。加载顺序：须在 boot.ts
 * 之前（重定向先读 nav-dir 标记，boot.ts 的导航块会把标记消费掉）。
 */
(function (window: any) {
  "use strict";
  try {
    var dir = sessionStorage.getItem("melodymate-nav-dir");
    var explicit = dir === "forward" || dir === "back";
    var shown = sessionStorage.getItem("melodymate-index-shown") === "1";
    if (explicit || shown) {
      sessionStorage.setItem("melodymate-index-shown", "1");
    } else {
      location.replace("/chat.html");
    }
  } catch (e) {}
  try {
    if (sessionStorage.getItem("melodymate_legacy_page_tip_v1") === "1") return;
    sessionStorage.setItem("melodymate_legacy_page_tip_v1", "1");
  } catch (e) {}
  document.addEventListener("DOMContentLoaded", function () {
    setTimeout(function () {
      if (window.UI && window.UI.toast) {
        window.UI.toast("ℹ 本页面已集成进「快速任务」：前往多轮对话页，点击顶部 ⚡ 快速任务 即可使用", "warn");
      }
    }, 600);
  });
})(window);
