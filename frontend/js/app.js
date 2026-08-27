/* AI_MIDI 前端共享工具库 */
(function (global) {
  "use strict";

  /* ---- 全局脚本错误兜底 ----
     toast 提示（同消息 3s 去重防刷屏）+ POST /api/client-error 落后端日志；
     上报自身失败静默，避免错误处理再触发新错误 */
  var lastErrMsg = "", lastErrAt = 0;
  global.addEventListener("error", function (e) {
    if (!e || !e.message) return;
    var now = Date.now();
    if (e.message === lastErrMsg && now - lastErrAt < 3000) return;
    lastErrMsg = e.message;
    lastErrAt = now;
    try {
      toast("✗ 脚本错误: " + e.message, "err");
    } catch (t) {}
    try {
      fetch("/api/client-error", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          message: String(e.message),
          source: e.filename || "",
          line: e.lineno || 0,
          column: e.colno || 0,
          page: location.pathname
        })
      }).catch(function () {});
    } catch (f) {}
  });

  /* ---- DOM 工具 ---- */
  function qs(sel, root) { return (root || document).querySelector(sel); }
  function qsa(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); }

  /* ---- 提示条 ---- */
  function ensureToastZone() {
    var zone = qs(".toast-zone");
    if (!zone) {
      zone = document.createElement("div");
      zone.className = "toast-zone";
      document.body.appendChild(zone);
    }
    return zone;
  }

  function toast(message, kind) {
    var zone = ensureToastZone();
    var el = document.createElement("div");
    el.className = "toast " + (kind || "");
    var span = document.createElement("span");
    span.className = "toast-msg";
    span.textContent = friendlyText(message);
    el.appendChild(span);
    /* 关闭按钮：长/多行提示在自动消失前可手动关闭 */
    var closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "toast-close";
    closeBtn.textContent = "✕";
    closeBtn.title = "关闭";
    el.appendChild(closeBtn);
    zone.appendChild(el);
    /* 时长按文本量自适应：多行提示固定 3.2s 会读不完就消失 */
    var duration = Math.min(9000, Math.max(3200, 1600 + String(message).length * 60));
    var closed = false;
    function dismiss() {
      if (closed) return;
      closed = true;
      el.style.opacity = "0";
      el.style.transition = "opacity 0.3s";
      setTimeout(function () { el.remove(); }, 320);
    }
    closeBtn.addEventListener("click", dismiss);
    setTimeout(dismiss, duration);
  }

  /* 文本级错误文案收口：调用方直接拼接 e.message 的地方（约 40 处）
     也能把浏览器原文（Failed to fetch 等）换成可读中文 */
  function friendlyText(text) {
    var s = String(text == null ? "" : text);
    if (/Failed to fetch|NetworkError|Load failed/i.test(s)) {
      return s.replace(/Failed to fetch|NetworkError[^:]*:?|Load failed/i,
        "网络连接失败，请检查网络后重试");
    }
    return s;
  }

  /* ---- 工具 ---- */
  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function fmtSize(bytes) {
    if (!bytes) return "0 B";
    if (bytes < 1024) return bytes + " B";
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
    return (bytes / 1024 / 1024).toFixed(1) + " MB";
  }

  function fmtDate(s) {
    if (!s) return "";
    var d = new Date(s);
    if (isNaN(d.getTime())) return String(s).slice(0, 10);
    var p = function (n) { return String(n).padStart(2, "0"); };
    return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate());
  }

  /* ---- 网络 ---- */
  /* 非 OK 响应：优先取 JSON detail；响应体非 JSON（502/504 网关页等）时
     回退状态码+文本摘要，避免 r.json() 抛 SyntaxError 显示解析乱码 */
  function throwHttpError(r) {
    return r.text().then(function (t) {
      var detail = "";
      try {
        detail = JSON.parse(t).detail || "";
      } catch (e) {
        detail = (t || "").slice(0, 200);
      }
      throw new Error(detail || ("HTTP " + r.status));
    });
  }

  /* 网络层错误（断网/DNS 失败）：fetch 拒绝时把浏览器原文
     "Failed to fetch" 换成可读文案 */
  function friendlyError(e) {
    if (e instanceof TypeError && /fetch/i.test(e.message || "")) {
      return new Error("网络连接失败，请检查网络后重试");
    }
    return e;
  }

  function getJSON(url) {
    return fetch(url).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError);
  }

  function postJSON(url, body) {
    return fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    }).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError);
  }

  function putJSON(url, body) {
    return fetch(url, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    }).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError);
  }

  function delJSON(url, body) {
    return fetch(url, {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    }).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError);
  }

  /* ---- SSE 消费：POST body，逐事件回调 onEvent(obj)，结束 resolve ----
     opts.signal 可传入 AbortSignal 用于主动停止；
     opts.timeoutMs 可覆盖挂起保护时长（默认 5 分钟无帧视为断线，
     报「连接超时」而非永久挂起——否则界面会一直卡在忙碌状态） */
  var SSE_TIMEOUT_MS = 300000;

  function ssePost(url, body, onEvent, opts) {
    opts = opts || {};
    var externalSignal = opts.signal || null;
    var timeoutMs = opts.timeoutMs || SSE_TIMEOUT_MS;
    /* 内部控制器：与外部 signal 联动（外部中止 → 内部中止），
       超时中止也走这里；用 timedOut 区分「用户停止」与「断线」 */
    var internal = new AbortController();
    var timedOut = false;
    var timer = null;

    function clearTimer() {
      if (timer) { clearTimeout(timer); timer = null; }
    }
    function touchTimer() {
      clearTimer();
      timer = setTimeout(function () {
        timedOut = true;
        internal.abort();
      }, timeoutMs);
    }
    touchTimer();
    if (externalSignal) {
      if (externalSignal.aborted) internal.abort();
      else externalSignal.addEventListener("abort", function () {
        internal.abort();
      }, { once: true });
    }

    return fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
      signal: internal.signal,
    }).then(function (resp) {
      if (!resp.ok) {
        clearTimer();
        return resp.json().then(function (j) { throw new Error(j.detail || ("HTTP " + resp.status)); });
      }
      var reader = resp.body.getReader();
      var decoder = new TextDecoder("utf-8");
      var buffer = "";

      function pump() {
        return reader.read().then(function (result) {
          if (result.done) { clearTimer(); return; }
          touchTimer();   /* 收到数据帧：重置挂起计时 */
          buffer += decoder.decode(result.value, { stream: true });
          var lines = buffer.split("\n");
          buffer = lines.pop();
          lines.forEach(function (line) {
            if (line.indexOf("data: ") === 0) {
              try {
                onEvent(JSON.parse(line.slice(6)));
              } catch (e) { /* 忽略坏帧 */ }
            }
          });
          return pump();
        });
      }

      return pump();
    }).catch(function (e) {
      clearTimer();
      if (timedOut) throw new Error("连接超时（长时间无响应，请重试）");
      throw e;
    });
  }

  /* ---- 跨文档导航动画：方向记录 ---- */
  function initNavDirections() {
    UI.qsa("a[href][data-dir]").forEach(function (a) {
      a.addEventListener("click", function () {
        var dir = a.getAttribute("data-dir");
        document.documentElement.dataset.nav = dir;
        try { sessionStorage.setItem("ai-midi-nav-dir", dir); } catch (e) {}
      });
    });
  }

  /* ---- Markdown 渲染（marked 优先，失败回退迷你解析） ---- */
  /* marked 安全配置：AI 输出中的原始 HTML 一律转义，防止注入 */
  if (window.marked && window.marked.use && window.marked.Renderer) {
    try {
      var _mdRenderer = new window.marked.Renderer();
      _mdRenderer.html = function (token) {
        var raw = (token && token.text != null) ? token.text : String(token);
        return esc(raw);
      };
      window.marked.use({ renderer: _mdRenderer });
    } catch (e) { /* marked 配置失败时按默认行为渲染 */ }
  }

  function md(text) {
    var s = String(text == null ? "" : text);
    if (window.marked && typeof window.marked.parse === "function") {
      try {
        var out = window.marked.parse(s, { breaks: true, gfm: true });
        /* 外链新窗口打开（与旧迷你解析行为一致） */
        return out.replace(/<a href="/g, '<a target="_blank" rel="noopener" href="');
      } catch (e) { /* 渲染失败回退到迷你解析 */ }
    }
    return mdMini(s);
  }

  /* 行内渲染：剥掉段落包裹（用于 <summary> 等单行场景） */
  function mdInline(text) {
    return md(text).trim().replace(/^<p>/, "").replace(/<\/p>$/, "");
  }

  /* 迷你 Markdown 渲染（输入先 esc，输出安全 HTML；无 marked 时的回退） */
  function mdMini(text) {
    var s = esc(text);
    /* 代码块 */
    s = s.split("```").map(function (part, i) {
      if (i % 2 === 0) return part;
      var lines = part.split("\n");
      lines.shift(); /* 去掉语言标记行 */
      return "<pre>" + lines.join("\n") + "</pre>";
    }).join("");
    /* 行内代码 */
    s = s.replace(/`([^`\n]+)`/g, "<code>$1</code>");
    /* 加粗 / 斜体 */
    s = s.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");
    s = s.replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>");
    /* 链接 */
    s = s.replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    /* 标题 */
    s = s.replace(/^##### (.*)$/gm, "<h6>$1</h6>");
    s = s.replace(/^#### (.*)$/gm, "<h5>$1</h5>");
    s = s.replace(/^### (.*)$/gm, "<h4>$1</h4>");
    s = s.replace(/^## (.*)$/gm, "<h3>$1</h3>");
    s = s.replace(/^# (.*)$/gm, "<h2>$1</h2>");
    /* 列表 */
    s = s.replace(/^[-*] (.*)$/gm, "• $1");
    /* 段落 */
    var blocks = s.split(/\n{2,}/).map(function (block) {
      var t = block.trim();
      if (!t) return "";
      if (/^<(h\d|pre|ul|ol)/.test(t)) return t;
      return "<p>" + t.replace(/\n/g, "<br>") + "</p>";
    });
    return blocks.join("\n");
  }

  /* ---- 档案卡片（对话页档案库渲染） ---- */
  function projectCard(proj) {
    var card = document.createElement("article");
    card.className = "card draft brackets proj-card";
    card.dataset.id = proj.id;
    card.dataset.name = proj.name || "未命名";
    var msgCount = proj.message_count || 0;
    card.innerHTML =
      '<div class="proj-name">' + esc(proj.name || "未命名") + "</div>" +
      '<div class="proj-meta">' +
      '<span class="stamp">' + msgCount + " \u6761\u6D88\u606F</span>" +
      '<span class="stamp">' + esc(fmtDate(proj.updated_at)) + "</span>" +
      "</div>" +
      '<div class="proj-actions">' +
      '<button class="btn btn-primary btn-sm action-open">\u6253\u5F00</button>' +
      '<button class="btn btn-secondary btn-sm action-rename">\u91CD\u547D\u540D</button>' +
      '<button class="btn btn-secondary btn-sm action-copy">\u590D\u5236</button>' +
      '<button class="btn btn-danger btn-sm action-delete">\u5220\u9664</button>' +
      "</div>";
    return card;
  }

  /* ---- 走带偏好：暂停后光标是否回退到本次播放起点 ----
     编曲窗与钢琴窗共用；从 /api/settings 读一次并缓存。
     onTransportPrefs 供走带条开关订阅初始值与后续变化 */
  var transportPrefs = { resumeOnPause: false, loaded: false };
  var transportSubs = [];

  function notifyTransportSubs() {
    for (var i = 0; i < transportSubs.length; i++) {
      try { transportSubs[i](transportPrefs); } catch (e) { /* 订阅者异常不阻断 */ }
    }
  }

  function getTransportPrefs() {
    if (!transportPrefs.loaded) {
      transportPrefs.loaded = true;
      getJSON("/api/settings").then(function (s) {
        if (s && typeof s.transport_resume_on_pause === "boolean") {
          transportPrefs.resumeOnPause = s.transport_resume_on_pause;
          notifyTransportSubs();
        }
      }).catch(function () { /* 读取失败保持默认（不回退） */ });
    }
    return transportPrefs;
  }

  /* 走带条开关写入：先改本地缓存（编曲窗/钢琴窗即时一致），
     持久化由调用方经 /api/transport/prefs 完成 */
  function setTransportPref(key, val) {
    transportPrefs[key] = val;
    notifyTransportSubs();
  }

  function onTransportPrefs(fn) {
    transportSubs.push(fn);
    if (transportPrefs.loaded) fn(transportPrefs);
  }

  global.UI = {
    qs: qs, qsa: qsa, toast: toast, esc: esc,
    fmtSize: fmtSize, fmtDate: fmtDate, friendlyText: friendlyText,
    getJSON: getJSON, postJSON: postJSON, putJSON: putJSON, delJSON: delJSON,
    ssePost: ssePost, md: md, mdInline: mdInline, projectCard: projectCard,
    transportPrefs: getTransportPrefs,
    setTransportPref: setTransportPref,
    onTransportPrefs: onTransportPrefs,
  };

  document.addEventListener("DOMContentLoaded", initNavDirections);
})(window);
