/* 自动更新检查与提示（update.js）
   启动时 GET /api/update/check →
     - 非必要更新：右下角卡片（立即更新 / 更新日志 / 关闭）
     - 必要更新：全屏遮罩（立即更新 / 更新日志，不可关闭）
   「立即更新」→ 应用内下载安装包（进度条）→ 自动运行安装程序；
   下载失败 → 内联对话框（重试 / 浏览器下载）。
   全部 UI 动态创建：页面只需挂载本脚本。 */
(function () {
  "use strict";
  var UI = window.UI;
  if (!UI) return;

  var info = null;       /* /api/update/check 响应 */
  var pollTimer = null;  /* 进度轮询句柄 */
  var els = {};          /* 动态元素引用 */

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function fmtMB(n) {
    return (Number(n || 0) / 1048576).toFixed(1);
  }

  /* ───────────────── DOM 构建（幂等） ───────────────── */

  var NOTICE_HTML =
    '<div class="update-title">🎉 新版本 v<span data-f="ver"></span> 可用</div>' +
    '<div class="update-date" data-f="date"></div>' +
    '<div class="update-body" data-f="body"></div>' +
    '<div class="update-actions" data-f="actions">' +
    '  <button class="btn btn-primary act-apply">立即更新</button>' +
    '  <button class="btn act-log">更新日志</button>' +
    '  <button class="btn act-close">关闭</button>' +
    "</div>";

  var FORCE_HTML =
    '<div class="force-update-card">' +
    '  <div class="force-title">⛔ 发现必要更新</div>' +
    '  <div class="force-text">必须更新到 <b>v<span data-f="ver"></span></b> 才能继续使用' +
    "（当前版本 v<span data-f=\"cur\"></span>）。" +
    "点击「立即更新」开始升级，完成后请重新打开软件。</div>" +
    '  <div class="update-body" data-f="body"></div>' +
    '  <div class="update-actions" data-f="actions">' +
    '    <button class="btn btn-primary act-apply">立即更新</button>' +
    '    <button class="btn act-log">更新日志</button>' +
    "  </div>" +
    "</div>";

  var LOG_HTML =
    '<div class="modal update-log-card">' +
    '  <div class="modal-title">更新日志 · v<span data-f="ver"></span></div>' +
    '  <div class="update-log-body" data-f="logbody"></div>' +
    '  <div class="modal-actions"><button class="btn btn-primary act-log-close">知道了</button></div>' +
    "</div>";

  function buildOnce() {
    if (els.built) return;

    els.notice = document.createElement("div");
    els.notice.id = "updateNotice";
    els.notice.className = "update-notice";
    els.notice.hidden = true;
    els.notice.innerHTML = NOTICE_HTML;
    document.body.appendChild(els.notice);

    els.force = document.createElement("div");
    els.force.id = "forceUpdateOverlay";
    els.force.className = "force-update-overlay";
    els.force.hidden = true;
    els.force.innerHTML = FORCE_HTML;
    document.body.appendChild(els.force);

    els.log = document.createElement("div");
    els.log.className = "update-log-overlay";
    els.log.hidden = true;
    els.log.innerHTML = LOG_HTML;
    document.body.appendChild(els.log);

    /* 填充版本信息 */
    fillAll(els.notice);
    fillAll(els.force);
    fillAll(els.log);
    els.log.querySelector("[data-f=logbody]").innerHTML = UI.md(info.notes || "");

    /* 事件绑定 */
    var apply = qs(els.notice, ".act-apply");
    apply.onclick = applyUpdate;
    qs(els.force, ".act-apply").onclick = applyUpdate;
    qs(els.notice, ".act-log").onclick = showLog;
    qs(els.force, ".act-log").onclick = showLog;
    qs(els.notice, ".act-close").onclick = function () { els.notice.hidden = true; };
    qs(els.log, ".act-log-close").onclick = hideLog;
    els.log.addEventListener("click", function (e) { if (e.target === els.log) hideLog(); });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !els.log.hidden) hideLog();
    });

    els.built = true;
  }

  function fillAll(root) {
    var verEls = root.querySelectorAll("[data-f=ver]");
    for (var i = 0; i < verEls.length; i++) verEls[i].textContent = info.latest;
    var curEls = root.querySelectorAll("[data-f=cur]");
    for (var j = 0; j < curEls.length; j++) curEls[j].textContent = info.current;
    var dateEls = root.querySelectorAll("[data-f=date]");
    for (var k = 0; k < dateEls.length; k++) dateEls[k].textContent = info.date || "";
  }

  function qs(root, sel) { return root.querySelector(sel); }

  /* ───────────────── 显示逻辑 ───────────────── */

  function showNotice() { els.notice.hidden = false; }
  function showForce() { els.force.hidden = false; }
  function showLog() { els.log.hidden = false; }
  function hideLog() { els.log.hidden = true; }

  /* 下载进度展示区（notice 与 force 共用同一结构，各自独立渲染） */
  function renderProgress(root, p) {
    var body = qs(root, "[data-f=body]");
    var actions = qs(root, "[data-f=actions]");
    var pct = p.total > 0 ? Math.floor((p.downloaded / p.total) * 100) : 0;

    if (p.state === "downloading" || (p.state === "completed" && !p.launched && !p.error)) {
      body.innerHTML =
        '<div class="update-progress-label">正在下载 ' + pct + "%（" +
        fmtMB(p.downloaded) + " / " + fmtMB(p.total) + " MB）</div>" +
        '<div class="update-progress"><i style="width:' + pct + '%"></i></div>';
      if (actions) actions.hidden = true;
      return "downloading";
    }

    if (p.state === "launched" || (p.state === "completed" && p.launched)) {
      body.innerHTML =
        '<div class="update-done">✅ 安装包已启动，请完成安装。<br>安装完成后<b>重新打开本软件</b>即可升级到 v' +
        esc(info.latest) + "。</div>";
      if (actions) actions.hidden = true;
      stopPolling();
      return "done";
    }

    if (p.state === "failed" || p.error) {
      stopPolling();
      body.innerHTML =
        '<div class="update-error">下载失败：' + esc(p.error || "未知错误") + "</div>" +
        '<div class="update-actions">' +
        '  <button class="btn btn-primary act-retry">重试</button>' +
        '  <button class="btn act-browser">浏览器下载</button>' +
        "</div>";
      if (actions) actions.hidden = true;
      qs(body, ".act-retry").onclick = applyUpdate;
      qs(body, ".act-browser").onclick = openBrowserDownload;
      return "failed";
    }
    return "idle";
  }

  /* ───────────────── 动作 ───────────────── */

  function applyUpdate() {
    buildOnce();
    UI.postJSON("/api/update/apply").then(function (res) {
      if (res.mode === "browser") {
        UI.toast("已在浏览器中打开下载链接", "ok");
        return;
      }
      /* 应用内下载：开始轮询进度 */
      if (pollTimer) clearInterval(pollTimer);
      renderProgress(els.notice, { state: "downloading", downloaded: 0, total: 0 });
      renderProgress(els.force, { state: "downloading", downloaded: 0, total: 0 });
      pollTimer = setInterval(pollProgress, 600);
      pollProgress();
    }).catch(function (e) {
      UI.toast("✗ " + e.message, "err");
    });
  }

  function pollProgress() {
    UI.getJSON("/api/update/progress").then(function (p) {
      var s1 = renderProgress(els.notice, p);
      var s2 = renderProgress(els.force, p);
      if (s1 !== "downloading" && s2 !== "downloading") stopPolling();
    }).catch(function () { /* 瞬时网络抖动：下次轮询继续 */ });
  }

  function stopPolling() {
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  }

  function openBrowserDownload() {
    UI.postJSON("/api/update/open-browser", {}).then(function () {
      UI.toast("已在浏览器中打开下载链接", "ok");
    }).catch(function (e) {
      UI.toast("✗ " + e.message, "err");
    });
  }

  /* ───────────────── 启动检查 ───────────────── */

  document.addEventListener("DOMContentLoaded", function () {
    UI.getJSON("/api/update/check").then(function (r) {
      if (!r || !r.available) return;
      info = r;
      buildOnce();
      if (r.mandatory) showForce(); else showNotice();
    }).catch(function () { /* 检查失败保持静默 */ });
  });
})();
