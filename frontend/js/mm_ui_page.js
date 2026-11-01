/* MM 插件 UI 承载页脚本（mm-ui.html 专用，P6 / 规格 4.7）。
 *
 * 三级结构：宿主（chat.html / 混音台） ── postMessage ──▶ 本页（承载窗，
 * same-origin） └─ sandbox iframe ──▶ 插件自绘 UI。
 *
 * 本页做协议翻译与校验：把插件的 mm.* 消息翻译成宿主 HTTP 调用，把宿主的
 * 参数/走带/主题推给插件，并丢弃字段类型非法的消息（插件 UI 崩坏不得影响
 * 宿主）。原为 mm-ui.html 内联脚本——CSP 恢复并收紧（script-src 去
 * 'unsafe-inline'）后搬出；以非 defer 挂在 authfetch.js 之后（令牌包裹
 * fetch 必须先行）。
 */
(function (window     ) {
  "use strict";

  var q = new URLSearchParams(location.search);
  var pluginId = q.get("plugin") || "";
  var track = q.get("track") !== null ? parseInt(q.get("track") || "", 10) : -1;
  var slot = q.get("slot") !== null ? parseInt(q.get("slot") || "", 10) : -1;
  var isMaster = q.get("master") === "1";
  if (isMaster) track = -1;

  var elName = document.getElementById("mmName")               ;
  var elStatus = document.getElementById("mmStatus")               ;
  var elParams = document.getElementById("mmParams")               ;
  var elSkeleton = document.getElementById("mmSkeleton")               ;
  var frame = document.getElementById("plug")       ;

  /* 参数模型：宿主权威。id → {index, name, min, max, value} */
  var paramsById      = {};
  var paramsByIndex      = {};

  function target() {
    var t      = { project: q.get("project") || "", generation: Number(q.get("generation")), trackId: q.get("trackId") || "" };
    if (isMaster) t.master = true; else t.track = track;
    if (slot >= 0) t.slot = slot;
    return t;
  }

  function loadParams() {
    var qs = new URLSearchParams({ project: q.get("project") || "", generation: q.get("generation") || "0", trackId: q.get("trackId") || "" });
    if (isMaster) qs.set("master", "1"); else qs.set("track", String(track));
    if (slot >= 0) qs.set("slot", String(slot));
    return fetch("/api/engine/plugins/params?" + qs.toString())
      .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error("HTTP " + r.status)); })
      .then(function (data     ) {
        if (data && data.error) throw new Error(data.error);
        elName.textContent = (data.name || pluginId) + (data.format ? " · " + data.format : "");
        paramsById = {}; paramsByIndex = {};
        (data.params || []).forEach(function (p     , i        ) {
          var rec = {
            index: typeof p.index === "number" ? p.index : i,
            id: p.id || ("param" + i),
            name: p.name || p.id || ("参数 " + i),
            min: typeof p.min === "number" ? p.min : 0,
            max: typeof p.max === "number" ? p.max : 1,
            value: typeof p.value === "number" ? p.value : 0
          };
          paramsById[rec.id] = rec;
          paramsByIndex[rec.index] = rec;
        });
        renderParams();
        pushParamsToPlugin();
        return data;
      });
  }

  function pushParamsToPlugin() {
    var list = Object.keys(paramsById).map(function (id        ) {
      var p = paramsById[id];
      return { id: id, index: p.index, value: p.value, text: String(p.value) };
    });
    post({ type: "mm.params", params: list });
  }

  function renderParams() {
    elParams.textContent = "";
    var ids = Object.keys(paramsById);
    if (!ids.length) {
      var empty = document.createElement("div");
      empty.className = "mm-empty";
      empty.textContent = "该插件没有可调参数";
      elParams.appendChild(empty);
      return;
    }
    ids.forEach(function (id        ) {
      var p = paramsById[id];
      var row = document.createElement("div");
      row.className = "mm-row";
      var nm = document.createElement("span");
      nm.className = "nm"; nm.textContent = p.name; nm.title = id;
      var range = document.createElement("input")                    ;
      range.type = "range";
      /* 引擎返回的 p.value **已经是 0~1 归一化值**（宿主按参数曲线算好，
         见 MmInstance::paramsToVar / PluginHost::paramsToVar）；写入方向
         setParam(index, normalized) 也是归一化值。所以这里直接当滑杆位置，
         不能再用 (value-min)/(max-min) 二次换算——那样对量程不是 0~1 的
         参数（如 20~20000 的截止频率）滑杆位置会与真实值不符。 */
      var norm = Math.max(0, Math.min(1, Number(p.value) || 0));
      range.min = "0"; range.max = "1"; range.step = "0.001"; range.value = String(norm);
      range.setAttribute("aria-label", p.name);
      range.setAttribute("aria-valuenow", String(norm));
      var vl = document.createElement("span");
      vl.className = "vl"; vl.textContent = p.value.toFixed(2);
      range.addEventListener("input", function () {
        var v = parseFloat(range.value);
        vl.textContent = v.toFixed(2);
        setParam(p.index, v);
      });
      row.appendChild(nm); row.appendChild(range); row.appendChild(vl);
      elParams.appendChild(row);
    });
  }

  function setParam(index        , normalized        ) {
    var body      = target();
    body.index = index;
    body.value = normalized;
    post({ type: "mm.paramChanged", id: paramsByIndex[index] ? paramsByIndex[index].id : String(index), value: normalized });
    return fetch("/api/engine/plugins/param", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
    }).then(function (r) {
      if (!r.ok) { elStatus.textContent = "参数写入失败"; }
    }).catch(function () { elStatus.textContent = "参数写入失败"; });
  }

  /** 只接受字段类型合法的消息；其余丢弃并计数（插件 UI 不得影响承载页）。
      来源必须就是那个 sandbox iframe：插件文档是不透明源（无
      allow-same-origin），ev.origin 为空串无法作为判据，因此核对消息源
      窗口对象——同源顶层页面无法伪造 ev.source。 */
  var dropped = 0;
  function onPluginMessage(ev     ) {
    if (ev.source !== frame.contentWindow) return;
    var d = ev.data;
    if (!d || typeof d !== "object" || typeof d.type !== "string" || d.type.indexOf("mm.") !== 0) return;
    if (d.type === "mm.ready") {
      elSkeleton.hidden = true;
      elStatus.textContent = "插件已就绪";
      loadParams().catch(function (e     ) { elStatus.textContent = "参数读取失败：" + e.message; });
      return;
    }
    if (d.type === "mm.setParam" && typeof d.id === "string" && typeof d.value === "number") {
      var rec = paramsById[d.id];
      if (!rec) { dropped++; elStatus.textContent = "未知参数 " + d.id; return; }
      setParam(rec.index, Math.max(0, Math.min(1, d.value)));
      return;
    }
    if (d.type === "mm.resize" && typeof d.width === "number" && typeof d.height === "number") {
      if (d.width > 0 && d.height > 0 && d.width <= 4096 && d.height <= 4096)
        frame.style.height = Math.max(120, d.height) + "px";
      return;
    }
    if (d.type === "mm.tooltip") { elStatus.textContent = String(d.text || ""); return; }
    dropped++;
  }
  window.addEventListener("message", onPluginMessage);

  function post(msg     ) {
    if (!frame.contentWindow) return;
    try {
      /* 目标源必须是 "*"：插件 iframe 带 sandbox（无 allow-same-origin），
         其文档是不透明源——父页无法算出它的源，指定具体 origin 会导致
         消息被静默丢弃（插件收不到 ready/参数推送）。插件代码本身已在
         沙箱内，无法借这条通道提权。 */
      frame.contentWindow.postMessage(msg, "*");
    } catch (e) {}
  }

  /** 宿主消息来源判定：必须是打开本页的窗口。
      实际关系视打开方式而定——混音台用 window.open 打开本页时，发送方
      是父窗口同时也可能是 opener；两种都要接受，否则走带/主题推送失效。
      插件 iframe 无法冒充（它的 source 是 frame.contentWindow）。 */
  function fromHost(ev     ) {
    if (ev.source !== window.parent && ev.source !== window.opener) return false;
    if (ev.origin && ev.origin !== "null" && ev.origin !== location.origin) return false;
    return true;
  }

  /* 主题：跟随系统/宿主 query（?theme=parchment），并推给插件 UI */
  var theme = q.get("theme") || (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "parchment" : "dark");
  document.documentElement.dataset.theme = theme;
  window.addEventListener("message", function (ev     ) {
    if (!fromHost(ev)) return;
    var d = ev.data;
    if (d && d.type === "mm.theme" && (d.theme === "dark" || d.theme === "parchment")) {
      theme = d.theme;
      document.documentElement.dataset.theme = theme;
    }
  });

  document.getElementById("mmParamsBtn") .addEventListener("click", function (                 ) {
    elParams.hidden = !elParams.hidden;
    this.setAttribute("aria-pressed", elParams.hidden ? "false" : "true");
  });
  document.getElementById("mmReloadBtn") .addEventListener("click", function () {
    loadFrame();
  });

  /* ── 窗口缩放（§4.7：右下角缩放把手 + 尺寸记忆）──
     本页是 window.open 打开的，因此 window.resizeTo 可用（Chromium 对脚本
     打开的窗口允许改尺寸）；WebView/被拦时静默降级为只调 iframe 高度。
     尺寸按插件 id 记忆，下次打开沿用（manifest 的 ui 尺寸仍是初值）。 */
  var SIZE_KEY = "melodymate-mm-ui-size:";
  function readSavedSize() {
    try {
      var raw = localStorage.getItem(SIZE_KEY + pluginId);
      if (!raw) return null;
      var v = JSON.parse(raw);
      if (v && v.w >= 320 && v.h >= 240 && v.w <= 4096 && v.h <= 4096) return v;
    } catch (e) {}
    return null;
  }
  function saveSize(w        , h        ) {
    try { localStorage.setItem(SIZE_KEY + pluginId, JSON.stringify({ w: w, h: h })); } catch (e) {}
  }
  function currentSize() {
    return { w: Math.round(window.outerWidth || window.innerWidth), h: Math.round(window.outerHeight || window.innerHeight) };
  }
  function applySize(w        , h        ) {
    try { window.resizeTo(w, h); } catch (e) {}
    /* 兜底：即便窗口尺寸改不了，也让插件 iframe 跟随（内容区仍可变） */
    frame.style.height = Math.max(120, h - 96) + "px";
  }
  (function initGrip() {
    var grip = document.getElementById("mmGrip")       ;
    if (!grip) return;
    var saved = readSavedSize();
    if (saved) applySize(saved.w, saved.h);
    var start      = null;
    grip.addEventListener("pointerdown", function (ev     ) {
      var cur = currentSize();
      start = { x: ev.clientX, y: ev.clientY, w: cur.w, h: cur.h };
      try { grip.setPointerCapture(ev.pointerId); } catch (e) {}
      ev.preventDefault();
    });
    grip.addEventListener("pointermove", function (ev     ) {
      if (!start) return;
      var w = Math.max(360, Math.round(start.w + (ev.clientX - start.x)));
      var h = Math.max(280, Math.round(start.h + (ev.clientY - start.y)));
      applySize(w, h);
    });
    var finish = function () {
      if (!start) return;
      start = null;
      var cur = currentSize();
      saveSize(cur.w, cur.h);
      elStatus.textContent = "窗口尺寸 " + cur.w + "×" + cur.h + "（已记住）";
    };
    grip.addEventListener("pointerup", finish);
    grip.addEventListener("pointercancel", finish);
    /* 键盘可达：方向键 ±20px，Shift ±4px（与滑杆同一套无障碍口径） */
    grip.addEventListener("keydown", function (ev     ) {
      var step = ev.shiftKey ? 4 : 20;
      var cur = currentSize();
      var w = cur.w, h = cur.h;
      if (ev.key === "ArrowLeft") w -= step;
      else if (ev.key === "ArrowRight") w += step;
      else if (ev.key === "ArrowUp") h -= step;
      else if (ev.key === "ArrowDown") h += step;
      else return;
      ev.preventDefault();
      w = Math.max(360, w); h = Math.max(280, h);
      applySize(w, h);
      saveSize(w, h);
    });
  })();

  var resourceSession = "";
  var resourceGeneration = 0, hostClosed = false;
  function releaseResource(session        ) { return fetch("/api/engine/plugins/ui-session/revoke", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session: session }), keepalive: true }).catch(function () {}); }
  function revokeResource() { if (resourceSession) { releaseResource(resourceSession); resourceSession = ""; } }
  function closeHost() { hostClosed = true; resourceGeneration++; revokeResource(); }
  window.addEventListener("beforeunload", closeHost);
  window.MMPluginHostClose = closeHost;
  window.addEventListener("keydown", function (event     ) {
    if (event.key === "Escape" && window.parent !== window) {
      event.stopPropagation();
      window.parent.postMessage({ type: "mm.close" }, location.origin);
    }
  });
  function loadFrame() {
    if (hostClosed) return;
    revokeResource();
    var generation = ++resourceGeneration;
    elSkeleton.hidden = false;
    elStatus.textContent = "载入插件界面…";
    /* 认证后签发一次性 ticket，再兑换目录受限的资源会话。
       返回实际地址后直接导航，兼容 Wails WebView2 的资源拦截器。 */
    fetch("/api/engine/plugins/ui-ticket", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ plugin: pluginId, project: q.get("project") || "", generation: Number(q.get("generation")) }) })
      .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error("HTTP " + r.status)); })
      .then(function (t     ) {
        if (hostClosed || generation !== resourceGeneration) { releaseResource(t.ticket); throw new Error("插件窗口已关闭或重载"); }
        resourceSession = t.ticket;
        return fetch("/api/engine/plugins/ui?response=json&ticket=" + encodeURIComponent(t.ticket))
          .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error("HTTP " + r.status)); });
      })
      .then(function (grant     ) {
        if (hostClosed || generation !== resourceGeneration) { releaseResource(grant.session); throw new Error("插件窗口已关闭或重载"); }
        frame.hidden = false;
        frame.src = grant.url;
        return loadParams().then(function () {
          post({ type: "mm.theme", theme: theme });
        }).catch(function () {
          /* 参数读取失败不阻断界面：插件 UI 仍可能可用 */
          post({ type: "mm.theme", theme: theme });
        });
      })
      .catch(function (e     ) {
        if (hostClosed || generation !== resourceGeneration) return;
        elStatus.textContent = "插件界面加载失败：" + (e && e.message ? e.message : "未知错误");
        elSkeleton.hidden = true;
      });
  }

  if (!pluginId) {
    elName.textContent = "缺少 plugin 参数";
    elStatus.textContent = "用法：mm-ui.html?plugin=<id>&track=<n> 或 ?plugin=<id>&slot=<n>（主链加 &master=1）";
    elSkeleton.hidden = true;
    return;
  }

  /* 走带（规范 mm.transport）：由**宿主窗口**推给本页，本页转发给插件 iframe。
     这里不轮询任何 HTTP：走带状态在宿主（编曲页）手里，不是引擎查询接口。
     宿主未接走带时插件也不会收到该消息，插件界面必须自行降级（不依赖它）。 */
  window.addEventListener("message", function (ev     ) {
    if (!fromHost(ev)) return;
    var d = ev.data;
    if (!d || d.type !== "mm.transport") return;
    if (typeof d.bpm !== "number") return;
    window.__mmTransport = { bpm: d.bpm, playing: !!d.playing, beat: typeof d.beat === "number" ? d.beat : 0 };
    elStatus.textContent = "BPM " + Math.round(d.bpm) + (d.playing ? " · ▶ 走带中" : " · ⏸ 停止");
    post({ type: "mm.transport", bpm: d.bpm, playing: !!d.playing, beat: window.__mmTransport.beat });
  });

  loadFrame();
})(window);
