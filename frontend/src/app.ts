/* AI_MIDI 前端共享工具库 */
(function (global: Window) {
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
      /* 用户可读文案而非原始异常直出（"Cannot read properties of
         undefined" 这类英文技术文本不可理解也不可操作）；原文仅落日志 */
      toast("✗ 出现内部错误，请重试；若持续出现请重启应用并在控制台查看详情", "err");
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
  function qs(sel: string, root?: ParentNode): Element | null { return (root || document).querySelector(sel); }
  function qsa(sel: string, root?: ParentNode): DomEl[] { return Array.prototype.slice.call((root || document).querySelectorAll(sel)) as DomEl[]; }

  /* ---- 提示条 ---- */
  function ensureToastZone() {
    var zone = qs(".toast-zone");
    if (!zone) {
      zone = document.createElement("div");
      zone.className = "toast-zone";
      /* 读屏可感知：全部 toast 经 aria-live 播报（此前屏幕阅读器
         完全收不到任何操作反馈） */
      zone.setAttribute("role", "status");
      zone.setAttribute("aria-live", "polite");
      document.body.appendChild(zone);
    }
    return zone;
  }

  function toast(message: unknown, kind?: string) {
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
  function friendlyText(text: unknown) {
    var s = String(text == null ? "" : text);
    if (/Failed to fetch|NetworkError|Load failed/i.test(s)) {
      return s.replace(/Failed to fetch|NetworkError[^:]*:?|Load failed/i,
        "网络连接失败，请检查网络后重试");
    }
    return s;
  }

  /* 本机接入点判定，与后端 config.IsLocalBaseURL 同一标准：只有 localhost /
     ::1 / 127.0.0.0/8 算本地。本地推理服务（Ollama、LM Studio、vLLM、
     llama.cpp）默认不校验密钥，所以设置页与对话页都不能因为"密钥为空"
     就把它们当成没配好。前缀匹配 127. 会被 127.evil.com 骗过，这里按域名
     分段和 IPv4 全段校验。 */
  function isLocalEndpoint(baseURL: unknown) {
    var raw = String(baseURL == null ? "" : baseURL).trim();
    if (!raw) return false;
    if (raw.indexOf("://") < 0) raw = "https://" + raw;
    var host = "";
    try {
      host = new URL(raw).hostname.toLowerCase();
    } catch (e) {
      return false;
    }
    // 有的引擎把 IPv6 写成 "[::1]"，先剥方括号再判定
    if (host.charAt(0) === "[" && host.charAt(host.length - 1) === "]") {
      host = host.slice(1, -1);
    }
    if (host === "localhost" || host === "::1") return true;
    return /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host);
  }

  /* ---- 工具 ---- */
  function esc(s: unknown) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function fmtSize(bytes: number) {
    if (!bytes) return "0 B";
    if (bytes < 1024) return bytes + " B";
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
    return (bytes / 1024 / 1024).toFixed(1) + " MB";
  }

  function fmtDate(s: string | undefined) {
    if (!s) return "";
    var d = new Date(s);
    if (isNaN(d.getTime())) return String(s).slice(0, 10);
    var p = function (n: number) { return String(n).padStart(2, "0"); };
    return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate());
  }

  /* ---- 网络 ---- */
  /* 非 OK 响应：优先取 JSON detail；响应体非 JSON（502/504 网关页等）时
     回退状态码+文本摘要，避免 r.json() 抛 SyntaxError 显示解析乱码 */
  function throwHttpError(r: Response) {
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
  function friendlyError(e: unknown) {
    if (e instanceof TypeError && /fetch/i.test(e.message || "")) {
      return new Error("网络连接失败，请检查网络后重试");
    }
    return e;
  }

  function getJSON<T>(url: string): Promise<T> {
    return fetch(url).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError) as Promise<T>;
  }

  function postJSON<T>(url: string, body?: unknown): Promise<T> {
    return fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    }).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError) as Promise<T>;
  }

  function putJSON<T>(url: string, body?: unknown): Promise<T> {
    return fetch(url, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    }).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError) as Promise<T>;
  }

  function delJSON<T>(url: string, body?: unknown): Promise<T> {
    return fetch(url, {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    }).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(friendlyError) as Promise<T>;
  }

  /* 打开外部链接（系统默认浏览器）：走后端 /api/open-url——该端点仅放行
     本应用自己的 GitHub 链接（防开放跳转）；后端不可达/拒绝时兜底新窗口，
     保证浏览器模式与桌面模式行为一致 */
  function openExternal(url: string): Promise<unknown> {
    return fetch("/api/open-url", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url: String(url) }),
    }).then(function (r) {
      if (!r.ok) return throwHttpError(r);
      return r.json();
    }).catch(function () {
      try { window.open(url, "_blank", "noopener"); } catch (e) { /* 弹窗被拦截则静默 */ }
    });
  }

  /* ---- SSE 消费：POST body，逐事件回调 onEvent(obj)，结束 resolve ----
     opts.signal 可传入 AbortSignal 用于主动停止；
     opts.timeoutMs 可覆盖挂起保护时长（默认 5 分钟无帧视为断线，
     报「连接超时」而非永久挂起——否则界面会一直卡在忙碌状态） */
  var SSE_TIMEOUT_MS = 300000;

  function ssePost<T>(url: string, body: unknown, onEvent: (ev: T) => void, opts?: SseOpts): Promise<unknown> {
    opts = opts || {};
    var externalSignal = opts.signal || null;
    var timeoutMs = opts.timeoutMs || SSE_TIMEOUT_MS;
    /* 内部控制器：与外部 signal 联动（外部中止 → 内部中止），
       超时中止也走这里；用 timedOut 区分「用户停止」与「断线」 */
    var internal = new AbortController();
    var timedOut = false;
    var timer: number | null = null;

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
      var reader = resp.body!.getReader();
      var decoder = new TextDecoder("utf-8");
      var buffer = "";

      function pump(): Promise<void> {
        return reader.read().then(function (result): Promise<void> | void {
          if (result.done) { clearTimer(); return; }
          touchTimer();   /* 收到数据帧：重置挂起计时 */
          buffer += decoder.decode(result.value, { stream: true });
          var lines = buffer.split("\n");
          buffer = lines.pop()!;
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

  /* ---- 聊天流双通道：桌面模式走 Wails 事件桥，浏览器模式走 HTTP SSE ----
     Wails Windows 的 assetserver 把整个响应体缓存到 Finish() 才一次性交给
     WebView2（且包装层不实现 Flusher），HTTP SSE 在桌面模式物理上无法流式——
     所有增量帧攒到生成结束一起到达。因此桌面模式经 window.go 绑定 +
     runtime.EventsOn（原生 IPC，即时送达）；浏览器模式行为不变。
     kind: "chat" | "answer"；onEvent(ev) 逐事件回调；onEnd() 流结束（含
     正常/异常/后端 done），保证只触发一次。 */
  var warnedBridgeFallback = false;   /* 桌面模式桥未生效：每页仅提示一次 */
  function streamStart(kind: string, body: StreamStartBody, onEvent: (ev: unknown) => void, onEnd: () => void, opts?: SseOpts): Promise<unknown> {
    var appBindings = (window.go && window.go.app && window.go.app.App) || null;
    var bridged = appBindings && window.runtime
      && typeof window.runtime.EventsOn === "function"
      && typeof appBindings.ChatStreamStart === "function"
      && typeof appBindings.AnswerStreamStart === "function";

    if (!bridged) {
      /* 桌面模式（WebView2）下桥未生效：不再无声降级——上报日志 + 每页一次
         提示，便于报障定位。此前静默回退被 Wails 缓存的 SSE 正是"一直转圈、
         退出重进才刷新"的根源之一 */
      if (window.chrome && window.chrome.webview) {
        try {
          fetch("/api/client-error", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              message: "桌面模式事件桥未生效，聊天流回退 HTTP SSE（实时显示将不可用）",
              source: "app.js:streamStart",
              line: 0,
              column: 0,
              page: location.pathname
            })
          }).catch(function () {});
        } catch (e) {}
        if (!warnedBridgeFallback) {
          warnedBridgeFallback = true;
          toast("⚠ 桌面实时通道未就绪，聊天回复可能不实时——请重启应用后重试", "warn");
        }
      }
      var url = kind === "answer" ? "/api/answer" : "/api/chat";
      return UI.ssePost(url, body, onEvent, opts).then(onEnd, function (e) {
        /* onEnd 先于错误抛出：调用方的收尾（chatDone）幂等 */
        onEnd();
        throw e;
      });
    }

    var sid = "s_" + Date.now() + "_" + Math.random().toString(36).slice(2, 8);
    var evtName = "chat:evt:" + sid;
    var endName = "chat:end:" + sid;
    var ended = false;
    var timeoutMs = (opts && opts.timeoutMs) || SSE_TIMEOUT_MS;
    /* 桥路径与 ssePost 一致的空闲看门狗：后端卡死（如上游停滞持有项目锁）
       时不能无限转圈——超时收尾复位 busy 态并抛出可见错误；任一事件帧
       到达即重置。此前桥路径无任何超时兜底 */
    var settled = false;
    var settleResolve: (value?: void) => void, settleReject: (reason?: unknown) => void;
    var settle = new Promise<void>(function (res, rej) { settleResolve = res; settleReject = rej; });
    var timer: number | null = null;
    function clearTimer() {
      if (timer) { clearTimeout(timer); timer = null; }
    }
    function touchTimer() {
      clearTimer();
      timer = setTimeout(function () {
        finish();
        if (!settled) { settled = true; settleReject(new Error("连接超时（长时间无响应，请重试）")); }
      }, timeoutMs);
    }
    function finish() {
      if (ended) return;
      ended = true;
      clearTimer();
      try { window.runtime!.EventsOff(evtName); } catch (e) {}
      try { window.runtime!.EventsOff(endName); } catch (e) {}
      onEnd();
    }
    window.runtime!.EventsOn(evtName, function (data) {
      if (ended) return;
      touchTimer();   /* 有帧到达：重置挂起计时 */
      try {
        var ev = (typeof data === "string") ? JSON.parse(data) : data;
        onEvent(ev);
      } catch (e) { /* 忽略坏帧 */ }
    });
    window.runtime!.EventsOn(endName, function () {
      finish();
      if (!settled) { settled = true; settleResolve(); }
    });

    var p: Promise<void>;
    if (kind === "answer") {
      p = appBindings!.AnswerStreamStart(body.project_id as string, body.question_id as string, body.answers, sid);
    } else {
      p = appBindings!.ChatStreamStart(body.project_id as string, body.message as string, !!body.edit, body.task_id || "", sid);
    }
    touchTimer();
    /* 绑定调用立即返回（Go 侧起 goroutine 后台执行）；settle 在流真正结束
       （chat:end）时 resolve、超时/绑定异常时 reject——调用方的 .catch 能
       显示错误，onEnd 兜底收尾（chatDone 幂等） */
    return Promise.resolve(p).then(function () { return settle; }).catch(function (e) {
      finish();
      throw e;
    });
  }

  /* ---- 跨文档导航动画：方向记录 ---- */
  function initNavDirections() {
    UI.qsa("a[href][data-dir]").forEach(function (a) {
      a.addEventListener("click", function () {
        var dir = a.getAttribute("data-dir") as string;
        document.documentElement.dataset.nav = dir;
        try { sessionStorage.setItem("ai-midi-nav-dir", dir); } catch (e) {}
      });
    });
  }

  /* ---- Markdown 渲染（marked 优先，失败回退迷你解析） ---- */
  /* marked 安全配置：AI 输出中的原始 HTML 一律转义，防止注入 */
  /* isSafeLinkHref 链接 scheme 白名单：http/https/mailto/页内锚点/相对路径。
     marked v12 已移除 sanitize，[x](javascript:...) 会原样产出可点击链接，
     必须在这里拦掉（LLM 输出可被间接提示词注入携带恶意链接）。 */
  function isSafeLinkHref(href: unknown) {
    var h = String(href == null ? "" : href).replace(/[\t\n\r]/g, "").trim();
    var lower = h.toLowerCase();
    if (lower === "" || lower.charAt(0) === "#") return true;
    if (/^(https?:|mailto:)/.test(lower)) return true;
    return lower.indexOf(":") === -1; /* 无 scheme 的相对路径放行，其余（javascript:/vbscript:/data: 等）拒绝 */
  }

  if (window.marked && window.marked.use && window.marked.Renderer) {
    try {
      var _mdRenderer = new window.marked.Renderer();
      _mdRenderer.html = function (token: MarkedHtmlToken | null) {
        var raw = (token && token.text != null) ? token.text : String(token);
        return esc(raw);
      };
      var _defaultLink = window.marked.Renderer.prototype.link;
      /* 兼容两种 renderer 签名：旧版 link(href, title, text) 与新版 link(token)。
         本仓库自带的 marked.min.js 为旧版签名（实测）。 */
      _mdRenderer.link = function (this: unknown, a?: MarkedHtmlToken | string | null, b?: unknown, c?: unknown) {
        if (a && typeof a === "object") {
          /* 新版 token 签名 */
          if (!isSafeLinkHref(a.href)) {
            return esc((a.text != null) ? a.text : "");
          }
          return _defaultLink.call(this, a);
        }
        /* 旧版 (href, title, text) 签名；c 为已解析的行内 HTML */
        if (!isSafeLinkHref(a)) {
          return (c == null) ? "" : String(c);
        }
        return _defaultLink.call(this, a, b, c);
      };
      window.marked.use({ renderer: _mdRenderer });
    } catch (e) { /* marked 配置失败时按默认行为渲染 */ }
  }

  function md(text: unknown) {
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
  function mdInline(text: unknown) {
    return md(text).trim().replace(/^<p>/, "").replace(/<\/p>$/, "");
  }

  /* 迷你 Markdown 渲染（输入先 esc，输出安全 HTML；无 marked 时的回退） */
  function mdMini(text: unknown) {
    var s = esc(text);
    /* 代码块 */
    s = s.split("```").map(function (part: string, i: number) {
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
    var blocks = s.split(/\n{2,}/).map(function (block: string) {
      var t = block.trim();
      if (!t) return "";
      if (/^<(h\d|pre|ul|ol)/.test(t)) return t;
      return "<p>" + t.replace(/\n/g, "<br>") + "</p>";
    });
    return blocks.join("\n");
  }

  /* ---- 档案卡片（对话页档案库渲染） ---- */
  function projectCard(proj: ProjectSummary) {
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
  var transportPrefs: TransportPrefs = { resumeOnPause: false, loaded: false };
  var transportSubs: Array<(prefs: TransportPrefs) => void> = [];

  function notifyTransportSubs() {
    for (var i = 0; i < transportSubs.length; i++) {
      try { transportSubs[i](transportPrefs); } catch (e) { /* 订阅者异常不阻断 */ }
    }
  }

  function getTransportPrefs() {
    if (!transportPrefs.loaded) {
      transportPrefs.loaded = true;
      getJSON<SettingsResponse>("/api/settings").then(function (s) {
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
  function setTransportPref(key: string, val: boolean) {
    transportPrefs[key] = val;
    notifyTransportSubs();
  }

  function onTransportPrefs(fn: (prefs: TransportPrefs) => void) {
    transportSubs.push(fn);
    if (transportPrefs.loaded) fn(transportPrefs);
  }

  /* ═══════════ 应用内确认弹窗 ═══════════
     替代 window.confirm：原生弹窗用的是浏览器/WebView 的外观，跟图纸主题
     两回事，而且会连带阻塞渲染。样式复用 chat.html 那套
     .modal-overlay / .modal.card.brackets，所以两处长得完全一样。

     用法：UI.confirm("要问的话")  或
           UI.confirm({ text, title, okText, cancelText, danger })
     返回 Promise<boolean>：确认 true，取消/Esc/点遮罩 false。 */
  var uiConfirmState: { resolve: ((v: boolean) => void) | null; el: HTMLElement | null } = {
    resolve: null,
    el: null,
  };

  function uiReducedMotion() {
    return typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  }

  function ensureConfirmDom() {
    if (uiConfirmState.el && document.contains(uiConfirmState.el)) return uiConfirmState.el;
    var overlay = document.createElement("div");
    overlay.className = "modal-overlay";
    overlay.id = "uiConfirmOverlay";
    overlay.hidden = true;
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    var box = document.createElement("div");
    box.className = "modal card brackets";
    var title = document.createElement("div");
    title.className = "modal-title";
    title.id = "uiConfirmTitle";
    var text = document.createElement("div");
    text.className = "confirm-text";
    var actions = document.createElement("div");
    actions.className = "modal-actions";
    var cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "btn btn-secondary";
    var ok = document.createElement("button");
    ok.type = "button";
    ok.className = "btn btn-danger";
    actions.appendChild(cancel);
    actions.appendChild(ok);
    box.appendChild(title);
    box.appendChild(text);
    box.appendChild(actions);
    overlay.appendChild(box);
    document.body.appendChild(overlay);
    overlay.setAttribute("aria-labelledby", "uiConfirmTitle");

    /* 点遮罩空白处 = 取消；点弹窗内部不冒泡成取消 */
    overlay.addEventListener("click", function (e) {
      if (e.target === overlay) settleConfirm(false);
    });
    overlay.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        e.stopPropagation();
        settleConfirm(false);
      }
    });
    cancel.addEventListener("click", function () { settleConfirm(false); });
    ok.addEventListener("click", function () { settleConfirm(true); });

    uiConfirmState.el = overlay;
    return overlay;
  }

  var confirmFocusReturn: HTMLElement | null = null;

  function settleConfirm(result: boolean) {
    var overlay = uiConfirmState.el;
    var resolve = uiConfirmState.resolve;
    uiConfirmState.resolve = null;
    if (resolve) resolve(result);
    if (!overlay) return;
    var el: HTMLElement = overlay;
    if (uiReducedMotion()) {
      el.hidden = true;
      el.classList.remove("modal-in", "modal-out");
    } else {
      el.classList.remove("modal-in");
      el.classList.add("modal-out");
      setTimeout(function () {
        /* 弹窗关完再复位：期间又开了一次的话，别把新的那次一起藏掉 */
        if (uiConfirmState.el !== el || uiConfirmState.resolve) return;
        el.hidden = true;
        el.classList.remove("modal-out");
      }, 220);
    }
    var back = confirmFocusReturn;
    confirmFocusReturn = null;
    if (back && back.focus && document.contains(back)) {
      try { back.focus(); } catch (e) {}
    }
  }

  function confirmDialog(opts: any): Promise<boolean> {
    var o = typeof opts === "string" ? { text: opts } : (opts || {});
    if (uiConfirmState.resolve) settleConfirm(false);   /* 前一个未决的先按取消收尾 */
    var overlay = ensureConfirmDom();
    var box = overlay.querySelector(".modal") as HTMLElement;
    var titleEl = overlay.querySelector(".modal-title") as HTMLElement;
    var textEl = overlay.querySelector(".confirm-text") as HTMLElement;
    var buttons = overlay.querySelectorAll(".modal-actions .btn");
    var cancel = buttons[0] as HTMLButtonElement;
    var ok = buttons[1] as HTMLButtonElement;
    titleEl.textContent = o.title || "确认操作";
    textEl.textContent = friendlyText(o.text == null ? "" : o.text);
    cancel.textContent = o.cancelText || "取消";
    ok.textContent = o.okText || "确定";
    /* 危险操作沿用 .btn-danger（红字描边），常规操作降为次要按钮 */
    ok.className = o.danger === false ? "btn btn-secondary" : "btn btn-danger";
    if (box) box.setAttribute("aria-label", titleEl.textContent || "确认操作");

    confirmFocusReturn = document.activeElement as HTMLElement;
    overlay.hidden = false;
    if (!uiReducedMotion()) {
      overlay.classList.remove("modal-in", "modal-out");
      void overlay.offsetWidth;
      overlay.classList.add("modal-in");
    }
    setTimeout(function () { try { ok.focus(); } catch (e) {} }, 30);

    return new Promise(function (resolve) {
      uiConfirmState.resolve = resolve;
    });
  }

  /* ---- 应用内输入弹窗 ----
     替代 window.prompt：原生 prompt 用浏览器/WebView 外观且阻塞渲染。
     UI.prompt("标题", "默认值") → Promise<string|null>（取消为 null） */
  var uiPromptState: { resolve: ((v: string | null) => void) | null; el: HTMLElement | null } = {
    resolve: null,
    el: null,
  };
  var promptFocusReturn: HTMLElement | null = null;

  function settlePrompt(result: string | null) {
    var overlay = uiPromptState.el;
    var resolve = uiPromptState.resolve;
    uiPromptState.resolve = null;
    if (resolve) resolve(result);
    if (!overlay) return;
    var el: HTMLElement = overlay;
    if (uiReducedMotion()) {
      el.hidden = true;
      el.classList.remove("modal-in", "modal-out");
    } else {
      el.classList.remove("modal-in");
      el.classList.add("modal-out");
      setTimeout(function () {
        if (uiPromptState.el !== el || uiPromptState.resolve) return;
        el.hidden = true;
        el.classList.remove("modal-out");
      }, 220);
    }
    var back = promptFocusReturn;
    promptFocusReturn = null;
    if (back && back.focus && document.contains(back)) {
      try { back.focus(); } catch (e) {}
    }
  }

  function ensurePromptDom() {
    if (uiPromptState.el && document.contains(uiPromptState.el)) return uiPromptState.el;
    var overlay = document.createElement("div");
    overlay.className = "modal-overlay";
    overlay.id = "uiPromptOverlay";
    overlay.hidden = true;
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    overlay.setAttribute("aria-labelledby", "uiPromptTitle");
    var box = document.createElement("div");
    box.className = "modal card brackets";
    var title = document.createElement("div");
    title.className = "modal-title";
    title.id = "uiPromptTitle";
    var input = document.createElement("input");
    input.type = "text";
    input.className = "prompt-input";
    input.autocomplete = "off";
    var actions = document.createElement("div");
    actions.className = "modal-actions";
    var cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "btn btn-secondary";
    var ok = document.createElement("button");
    ok.type = "button";
    ok.className = "btn btn-primary";
    actions.appendChild(cancel);
    actions.appendChild(ok);
    box.appendChild(title);
    box.appendChild(input);
    box.appendChild(actions);
    overlay.appendChild(box);
    document.body.appendChild(overlay);

    overlay.addEventListener("click", function (e) {
      if (e.target === overlay) settlePrompt(null);
    });
    overlay.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        e.stopPropagation();
        settlePrompt(null);
      } else if (e.key === "Enter") {
        e.stopPropagation();
        settlePrompt(input.value);
      }
    });
    cancel.addEventListener("click", function () { settlePrompt(null); });
    ok.addEventListener("click", function () { settlePrompt(input.value); });

    uiPromptState.el = overlay;
    return overlay;
  }

  function promptDialog(title: string, defaultValue?: string): Promise<string | null> {
    if (uiPromptState.resolve) settlePrompt(null);   /* 前一个未决的先收尾 */
    var overlay = ensurePromptDom();
    var titleEl = overlay.querySelector(".modal-title") as HTMLElement;
    var input = overlay.querySelector(".prompt-input") as HTMLInputElement;
    var buttons = overlay.querySelectorAll(".modal-actions .btn");
    (buttons[0] as HTMLButtonElement).textContent = "取消";
    (buttons[1] as HTMLButtonElement).textContent = "确定";
    titleEl.textContent = title || "请输入";
    input.value = defaultValue == null ? "" : String(defaultValue);

    promptFocusReturn = document.activeElement as HTMLElement;
    overlay.hidden = false;
    if (!uiReducedMotion()) {
      overlay.classList.remove("modal-in", "modal-out");
      void overlay.offsetWidth;
      overlay.classList.add("modal-in");
    }
    setTimeout(function () {
      try { input.focus(); input.select(); } catch (e) {}
    }, 30);

    return new Promise(function (resolve) {
      uiPromptState.resolve = resolve;
    });
  }

  global.UI = {
    qs: qs, qsa: qsa, toast: toast, esc: esc,
    confirm: confirmDialog,
    prompt: promptDialog,
    fmtSize: fmtSize, fmtDate: fmtDate, friendlyText: friendlyText,
    isLocalEndpoint: isLocalEndpoint,
    getJSON: getJSON, postJSON: postJSON, putJSON: putJSON, delJSON: delJSON,
    openExternal: openExternal,
    ssePost: ssePost, md: md, mdInline: mdInline, projectCard: projectCard,
    streamStart: streamStart,
    transportPrefs: getTransportPrefs,
    setTransportPref: setTransportPref,
    onTransportPrefs: onTransportPrefs,
  };

  /* 鼠标点击过的按钮保持焦点，空格键会再次触发它（发送/播放等按钮
     因此被意外重复激活）。冒泡阶段在按钮自身 handler 之后 blur——
     键盘 Tab 导航的焦点不受影响 */
  document.addEventListener("click", function (e) {
    var t = e.target as any;
    while (t && t !== document) {
      if (t.tagName === "BUTTON") {
        t.blur();
        return;
      }
      t = t.parentElement;
    }
  });

  document.addEventListener("DOMContentLoaded", initNavDirections);
})(window);
