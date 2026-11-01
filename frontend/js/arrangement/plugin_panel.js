/* 通用插件参数面板（计划 §4.6）+ 插件状态随工程保存（P5 §4）。
 *
 * 为什么单独成文件：轨道音源（乐器）与 FX 槽共用同一套参数读写与状态存取，
 * 面板只有一份实现，rack.ts（轨头）与 mixer.ts（FX 槽）都调它。
 *
 * 数据通路（全部走已有 HTTP 入口，VST3 与 MM 共用）：
 *   GET  /api/engine/plugins/params?track=&slot=[&master=1]   参数表
 *   POST /api/engine/plugins/param   {track,slot,master,index,value}  写参数（0~1）
 *   POST /api/engine/plugins/editor  {track,slot,master,close?}        原生编辑器（VST3）
 *   POST /api/engine/plugins/state/save {project,track,slot,master,ref,format}
 *   POST /api/engine/plugins/state/load {…}                            状态随工程保存
 *
 * 设计口径（§4.6）：参数下发 30Hz 节流；面板每 0.5s 回读一次引擎权威值，
 * 外部（AI/插件界面/原生窗口）改动会在对应行亮 0.6s 脉冲；键盘可达
 * （←→ ±1%、Shift ±10%、Home/End 极值）；只用既有令牌，动画只用
 * transform/opacity，reduced-motion 下降级。
 */
(function (window     ) {
  "use strict";

  var POLL_MS = 500;      // 引擎回读周期（计划要求"改动 0.2s 高亮回显"）
  var FLUSH_MS = 33;      // 参数下发节流 ≈30Hz
  function resolveFormat(refOrTarget     )         {
    if (refOrTarget && typeof refOrTarget === "object") {
      if (refOrTarget.format === "mm" || refOrTarget.format === "vst3") return refOrTarget.format;
      return resolveFormat(refOrTarget.sourceId || refOrTarget.ref || "");
    }
    var ref = String(refOrTarget || "").trim();
    if (!ref) return "";
    if (ref.indexOf("VST3|") === 0 || /\.vst3$/i.test(ref) || /^vst3[_:]/i.test(ref)) return "vst3";
    if (window.SoundRegistry && typeof window.SoundRegistry.get === "function") {
      var src = window.SoundRegistry.get(ref);
      if (src && (src.kind === "vst3" || src.kind === "mm")) return src.kind;
    }
    /* 识别失败返回空串：等 /params 响应回写 res.format 后恢复完整功能，
       不再瞎猜（猜 mm 会把 VST3 当 MM 走承载窗，猜 vst3 会拿 MM 去开原生编辑器）。 */
    return "";
  }
  var isMmRef = function (refOrTarget     ) { return resolveFormat(refOrTarget) === "mm"; };
  function formatOf(t     )         {
    return t && (t.format === "mm" || t.format === "vst3") ? t.format : "";
  }

  var state      = {
    open: false,
    target: null,
    rows: [],             // [{ index, name, id, min, max, default, value, text, rowEl, sliderEl, valueEl }]
    pending: {},          // index -> value（待下发）
    flushTimer: 0,
    pollTimer: 0,
    dragging: -1
  };

  function toast(msg        , kind         ) {
    if (window.UI && window.UI.toast) window.UI.toast(msg, kind);
  }

  function el(tag        , cls         , text         )      {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function targetLabel(t     ) {
    var where = t.master ? "主总线" : "轨道 #" + (t.track + 1);
    if (t.slot >= 0) where += " · FX " + (t.slot + 1);
    else where += " · 乐器";
    return where;
  }

  /* ── 参数读写 ── */

  function paramsURL(t     ) {
    var q = "?track=" + encodeURIComponent(t.track) + "&project="+encodeURIComponent(t.project||"")+"&generation="+t.generation+"&trackId="+encodeURIComponent(t.trackId||"");
    if (t.slot >= 0) q += "&slot=" + encodeURIComponent(t.slot);
    if (t.master) q += "&master=1";
    return "/api/engine/plugins/params" + q;
  }

  function bodyOf(t     , extra      ) {
    var body      = { track: t.master ? 0 : t.track, master: !!t.master, project:t.project, generation:t.generation, trackId:t.trackId };
    if (t.slot >= 0) body.slot = t.slot;
    if (extra) for (var k in extra) body[k] = extra[k];
    return body;
  }

  function flush() {
    state.flushTimer = 0;
    var t = state.target;
    if (!t) return;
    var keys = Object.keys(state.pending);
    if (!keys.length) return;
    var pending = state.pending;
    state.pending = {};
    /* 一次连接整批下发：避免逐参数排队（N×33ms）与相邻参数乱序 */
    var changes = keys.map(function (k        ) { return { index: Number(k), value: pending[k] }; });
    window.UI.postJSON("/api/engine/plugins/param", bodyOf(t, { changes: changes }))
      .then(function (res     ) {
        // 引擎回读：用它校正显示（步进参数会被量化）
        applyEngineParams(res);
      })
      .catch(function (err     ) {
        toast("✗ 参数下发失败：" + ((err && err.message) || "未知错误"), "err");
        refresh();
      });
  }

  function scheduleFlush() {
    if (state.flushTimer) return;
    state.flushTimer = window.setTimeout(flush, FLUSH_MS);
  }

  function queueParam(index        , value        ) {
    state.pending[index] = value;
    scheduleFlush();
  }

  /* ── 渲染 ── */

  function groupsOf(params       ) {
    var order           = [];
    var byGroup      = {};
    params.forEach(function (p     ) {
      var g = p.group ? String(p.group) : "参数";
      if (!byGroup[g]) { byGroup[g] = []; order.push(g); }
      byGroup[g].push(p);
    });
    return { order: order, byGroup: byGroup };
  }

  function buildParamRow(t     , p     ) {
    var row = el("div", "plg-param-row");
    row.setAttribute("role", "group");

    var name = el("span", "plg-param-name", p.name || p.id || ("#" + p.index));
    if (p.id && p.id !== p.name) name.title = String(p.id);

    var slider = document.createElement("input");
    slider.type = "range";
    slider.className = "plg-param-slider";
    slider.min = "0";
    slider.max = "1";
    slider.step = "0.001";
    slider.value = String(clamp01(p.value));
    slider.setAttribute("role", "slider");
    slider.setAttribute("aria-label", String(p.name || p.id || ("参数 " + p.index)));
    slider.setAttribute("aria-valuemin", "0");
    slider.setAttribute("aria-valuemax", "1");
    slider.setAttribute("aria-valuenow", String(clamp01(p.value)));
    slider.tabIndex = 0;

    var valueEl = el("span", "plg-param-value", fmtValue(p));
    if (p.text) valueEl.title = String(p.text);

    var rec      = {
      index: p.index, name: p.name, id: p.id, default: num(p.default, num(p.defaultValue, 0)),
      min: p.min, max: p.max, text: p.text, value: clamp01(p.value),
      rowEl: row, sliderEl: slider, valueEl: valueEl
    };

    slider.addEventListener("pointerdown", function () { state.dragging = p.index; });
    slider.addEventListener("pointerup", function () { state.dragging = -1; });
    slider.addEventListener("input", function () {
      rec.value = clamp01(Number(slider.value));
      valueEl.textContent = fmtValue(rec);
      slider.setAttribute("aria-valuenow", String(rec.value));
      queueParam(p.index, rec.value);
    });
    slider.addEventListener("change", function () {
      state.dragging = -1;
      rec.value = clamp01(Number(slider.value));
      queueParam(p.index, rec.value);
      flush();
    });
    /* 键盘口径（§4.6）：←→ ±1%、Shift ±10%、Home/End 极值 */
    slider.addEventListener("keydown", function (ev     ) {
      var step = ev.shiftKey ? 0.1 : 0.01;
      var v = rec.value;
      if (ev.key === "ArrowLeft" || ev.key === "ArrowDown") v = clamp01(v - step);
      else if (ev.key === "ArrowRight" || ev.key === "ArrowUp") v = clamp01(v + step);
      else if (ev.key === "Home") v = 0;
      else if (ev.key === "End") v = 1;
      else return;
      ev.preventDefault();
      rec.value = v;
      slider.value = String(v);
      valueEl.textContent = fmtValue(rec);
      slider.setAttribute("aria-valuenow", String(v));
      queueParam(p.index, v);
      flush();
    });
    /* 双击参数名 = 回到默认值（与"重置"同一个语义，范围更小） */
    name.addEventListener("dblclick", function () {
      rec.value = clamp01(rec.default);
      slider.value = String(rec.value);
      valueEl.textContent = fmtValue(rec);
      queueParam(p.index, rec.value);
      flush();
    });
    name.style.cursor = "pointer";
    name.title = (name.title ? name.title + " · " : "") + "双击回到默认值";

    row.appendChild(name);
    row.appendChild(slider);
    row.appendChild(valueEl);
    state.rows.push(rec);
    return row;
  }

  function num(v     , fallback        ) {
    return (typeof v === "number" && isFinite(v)) ? v : fallback;
  }

  function clamp01(v     ) {
    var n = Number(v);
    if (!isFinite(n)) return 0;
    return n < 0 ? 0 : (n > 1 ? 1 : n);
  }

  function fmtValue(p     ) {
    var text = typeof p.text === "string" && p.text ? p.text : null;
    var base = clamp01(p.value).toFixed(3);
    /* MM 参数带实际范围：把实际值算出来一起显示，避免"0~1 是什么"的困惑 */
    if (typeof p.min === "number" && typeof p.max === "number" && p.max > p.min) {
      var real = p.min + clamp01(p.value) * (p.max - p.min);
      base = base + "（" + real.toFixed(2) + "）";
    }
    if (text) base += " · " + text;
    return base;
  }

  function renderBody(root     , params       ) {
    state.rows = [];
    var body = root.querySelector(".plg-body");
    body.textContent = "";
    if (!params.length) {
      body.appendChild(el("div", "plg-empty", "该插件没有可调参数。"));
      return;
    }
    var grouped = groupsOf(params);
    grouped.order.forEach(function (g        ) {
      var group = el("div", "plg-group");
      var head = el("button", "plg-group-head", "▾ " + g + " · " + grouped.byGroup[g].length);
      head.type = "button";
      head.setAttribute("aria-expanded", "true");
      var wrap = el("div", "plg-group-body");
      grouped.byGroup[g].forEach(function (p     ) { wrap.appendChild(buildParamRow(state.target, p)); });
      head.addEventListener("click", function () {
        var hidden = wrap.style.display === "none";
        wrap.style.display = hidden ? "" : "none";
        head.textContent = (hidden ? "▾ " : "▸ ") + g + " · " + grouped.byGroup[g].length;
        head.setAttribute("aria-expanded", hidden ? "true" : "false");
      });
      group.appendChild(head);
      group.appendChild(wrap);
      body.appendChild(group);
    });
  }

  /** applyEngineParams 用引擎的权威值刷新界面（不改正在拖动的行） */
  function applyEngineParams(res     ) {
    var params = (res && res.params) || [];
    if (!state.open) return;
    params.forEach(function (p     ) {
      var rec = rowOf(p.index);
      if (!rec) return;
      var engineValue = clamp01(p.value);
      if (p.index === state.dragging) return;
      if (Math.abs(engineValue - rec.value) > 0.0005) {
        /* 外部改动（AI 工具 / 原生窗口 / 插件自身）→ 回显 + 脉冲高亮 */
        rec.value = engineValue;
        rec.sliderEl.value = String(engineValue);
        rec.sliderEl.setAttribute("aria-valuenow", String(engineValue));
        rec.valueEl.textContent = fmtValue({ value: engineValue, text: p.text, min: p.min, max: p.max });
        pulse(rec.rowEl);
      } else if (p.text && p.text !== rec.text) {
        rec.text = p.text;
        rec.valueEl.textContent = fmtValue({ value: rec.value, text: p.text, min: p.min, max: p.max });
      }
    });
  }

  function rowOf(index        ) {
    for (var i = 0; i < state.rows.length; i++)
      if (state.rows[i].index === index) return state.rows[i];
    return null;
  }

  function pulse(node     ) {
    if (!node) return;
    node.classList.remove("plg-echo");
    /* 强制回流以重启动画（同一行连续被外部改动时也能看到脉冲） */
    void node.offsetWidth;
    node.classList.add("plg-echo");
    window.setTimeout(function () { node.classList.remove("plg-echo"); }, 600);
  }

  /* ── 面板骨架 ── */

  function build(t     ) {
    var root = el("div", "plg-win");
    root.setAttribute("role", "dialog");
    root.setAttribute("aria-label", "插件参数面板");

    var head = el("div", "plg-head");
    var dot = el("span", "plg-dot");
    var title = el("span", "plg-title", (t.name || "插件") + " · " + targetLabel(t));
    title.title = title.textContent;
    if (isMmRef(t)) title.appendChild(el("span", "plg-badge-ai", "MM"));
    head.appendChild(dot);
    head.appendChild(title);

    var actions = el("div", "plg-actions");
    var btn = function (label        , hint        , fn     ) {
      var b = el("button", "plg-btn", label);
      b.type = "button";
      b.title = hint;
      b.addEventListener("click", fn);
      actions.appendChild(b);
      return b;
    };
    btn("🎲 随机", "把所有参数随机化（可撤销：先保存状态）", randomize);
    btn("↺ 重置", "恢复插件默认值", resetAll);
    btn("💾 保存状态", "把当前插件状态存进本工程（重开工程后自动恢复）", saveState);
    btn("📂 恢复状态", "从本工程保存的状态恢复", loadState);
    btn("🖥 插件界面", isMmRef(t) ? "打开 MM 承载窗" : "打开插件原生编辑器", openEditor);
    var closeBtn = el("button", "plg-btn plg-close", "✕");
    closeBtn.type = "button";
    closeBtn.title = "关闭（Esc）";
    closeBtn.addEventListener("click", close);
    actions.appendChild(closeBtn);

    head.appendChild(actions);
    var body = el("div", "plg-body");
    var foot = el("div", "plg-foot", "参数下发 30Hz 节流 · 每 0.5s 回读引擎权威值 · ←→ ±1%、Shift ±10%、Home/End 极值、双击参数名回默认");
    root.appendChild(head);
    root.appendChild(body);
    root.appendChild(foot);

    /* 拖动：标题栏按下即拖（与机架浮窗同一手感） */
    var drag      = null;
    head.addEventListener("pointerdown", function (ev     ) {
      if (ev.target && ev.target.tagName === "BUTTON") return;
      drag = { x: ev.clientX, y: ev.clientY, left: root.offsetLeft, top: root.offsetTop };
      head.setPointerCapture && head.setPointerCapture(ev.pointerId);
    });
    head.addEventListener("pointermove", function (ev     ) {
      if (!drag) return;
      root.style.left = Math.max(0, drag.left + ev.clientX - drag.x) + "px";
      root.style.top = Math.max(0, drag.top + ev.clientY - drag.y) + "px";
      root.style.right = "auto";
    });
    head.addEventListener("pointerup", function () { drag = null; });
    return root;
  }

  /* ── 动作 ── */

  function randomize() {
    state.rows.forEach(function (rec     ) {
      var v = Math.random();
      rec.value = v;
      rec.sliderEl.value = String(v);
      rec.sliderEl.setAttribute("aria-valuenow", String(v));
      rec.valueEl.textContent = fmtValue({ value: v, text: rec.text, min: rec.min, max: rec.max });
      queueParam(rec.index, v);
    });
    flush();
    toast("已随机化 " + state.rows.length + " 个参数", "ok");
  }

  function resetAll() {
    state.rows.forEach(function (rec     ) {
      var v = clamp01(rec.default);
      rec.value = v;
      rec.sliderEl.value = String(v);
      rec.sliderEl.setAttribute("aria-valuenow", String(v));
      rec.valueEl.textContent = fmtValue({ value: v, text: rec.text, min: rec.min, max: rec.max });
      queueParam(rec.index, v);
    });
    flush();
    toast("已恢复默认值", "ok");
  }

  function stateBody(extra      ) {
    var t = state.target;
    var body = bodyOf(t, { project: t.project, ref: t.ref || "", format: isMmRef(t) ? "mm" : "vst3" });
    if (extra) for (var k in extra) body[k] = extra[k];
    return body;
  }

  function saveState() {
    if (!state.target || !state.target.project) { toast("⚠ 没有打开的工程，无法保存插件状态", "warn"); return; }
    if (!formatOf(state.target)) { toast("⚠ 插件类型未识别，无法保存状态（关闭面板重开可重试）", "warn"); return; }
    window.UI.postJSON("/api/engine/plugins/state/save", stateBody())
      .then(function (res     ) {
        toast("✓ 插件状态已保存到工程（" + Math.round((res && res.bytes || 0) / 1024) + " KB）", "ok");
      })
      .catch(function (err     ) { toast("✗ 保存插件状态失败：" + ((err && err.message) || ""), "err"); });
  }

  function loadState() {
    if (!state.target || !state.target.project) { toast("⚠ 没有打开的工程，无法恢复插件状态", "warn"); return; }
    if (!formatOf(state.target)) { toast("⚠ 插件类型未识别，无法恢复状态（关闭面板重开可重试）", "warn"); return; }
    window.UI.postJSON("/api/engine/plugins/state/load", stateBody())
      .then(function () {
        toast("✓ 已恢复插件状态", "ok");
        refresh();
      })
      .catch(function (err     ) { toast("✗ 恢复插件状态失败：" + ((err && err.message) || ""), "err"); });
  }

  function openEditor() {
    var t = state.target;
    if (!t || !t.ref) { toast("⚠ 这个位置没有插件", "warn"); return; }
    var fmt = formatOf(t);
    if (!fmt) { toast("⚠ 插件类型未识别，无法打开插件界面（关闭面板重开可重试）", "warn"); return; }
    if (fmt === "mm") {
      /* MM：无原生窗口，走 mm-ui.html 承载页（sandbox iframe + postMessage 桥） */
      var qs = "?plugin=" + encodeURIComponent(t.ref) + "&slot=" + t.slot+"&project="+encodeURIComponent(t.project)+"&generation="+t.generation+"&trackId="+encodeURIComponent(t.trackId||"");
      if (t.master) qs += "&master=1"; else qs += "&track=" + t.track;
      var theme = document.documentElement.dataset.theme;
      if (theme) qs += "&theme=" + encodeURIComponent(theme);
      var win = window.UI.openPluginUI("/mm-ui.html" + qs, "mmui_" + t.ref + "_" + t.slot);
      if (!win) toast("⚠ 浏览器拦截了弹窗：请允许本站弹窗后重试", "warn");
      return;
    }
    window.UI.postJSON("/api/engine/plugins/editor", bodyOf(t))
      .then(function () { toast("已打开插件界面（原生窗口）", "ok"); })
      .catch(function (err     ) { toast("✗ 打开插件界面失败：" + ((err && err.message) || ""), "err"); });
  }

  function refresh() {
    var t = state.target;
    if (!t) return Promise.resolve();
    return window.UI.getJSON(paramsURL(t))
      .then(function (res     ) {
        if (res && (res.format === "mm" || res.format === "vst3")) t.format = res.format;
        var params = (res && res.params) || [];
        if (!state.rows.length) {
          renderBody(state.root, params);
          var titleEl = state.root.querySelector(".plg-title");
          if (res && res.name && titleEl) {
            titleEl.textContent = res.name + " · " + targetLabel(t);
            if (isMmRef(t)) titleEl.appendChild(el("span", "plg-badge-ai", "MM"));
          }
        } else {
          applyEngineParams(res);
        }
      })
      .catch(function (err     ) {
        var body = state.root.querySelector(".plg-body");
        if (body && !state.rows.length) {
          body.textContent = "";
          body.appendChild(el("div", "plg-empty", "读取插件参数失败：" + ((err && err.message) || "未知错误")));
        }
      });
  }

  /* ── 生命周期 ── */

  function open(ctrl     , target     ) {
    if (!ctrl || !ctrl.projectId) { toast("⚠ 请先打开一个工程", "warn"); return; }
    close();
    state.open = true;
    state.target = {
      project: ctrl.projectId,
      generation: ctrl.projectEpoch,
      trackId: target.master ? "" : (ctrl.tracks[target.track || 0] || {}).id,
      track: typeof target.track === "number" ? target.track : 0,
      slot: typeof target.slot === "number" ? target.slot : -1,
      master: !!target.master,
      ref: target.ref || "",
      sourceId: target.sourceId || "",
      format: target.format || resolveFormat(target),
      name: target.name || ""
    };
    state.root = build(state.target);
    state.root.style.left = Math.max(40, Math.round((window.innerWidth || 1280) / 2 - 240)) + "px";
    state.root.style.top = "120px";
    document.body.appendChild(state.root);
    refresh();
    state.pollTimer = window.setInterval(function () { if (state.open) refresh(); }, POLL_MS);
    document.addEventListener("keydown", onKeyDown, true);
  }

  function onKeyDown(ev     ) {
    if (ev.key === "Escape" && state.open) {
      ev.stopPropagation();
      close();
    }
  }

  function close() {
    state.open = false;
    state.dragging = -1;
    var closing = state.target;
    var keys = Object.keys(state.pending);
    if (closing && keys.length) {
      /* 关窗兜底：剩余待下发参数一次性 fire-and-forget 发出，避免丢改动 */
      var closingPending = state.pending;
      var closingChanges = keys.map(function (k        ) { return { index: Number(k), value: closingPending[k] }; });
      window.UI.postJSON("/api/engine/plugins/param", bodyOf(closing, { changes: closingChanges }))
        .catch(function () { /* 关窗兜底请求失败即放弃，0.5s 回读与下次打开会纠正 */ });
    }
    state.pending = {};
    if (state.flushTimer) { window.clearTimeout(state.flushTimer); state.flushTimer = 0; }
    if (state.pollTimer) { window.clearInterval(state.pollTimer); state.pollTimer = 0; }
    document.removeEventListener("keydown", onKeyDown, true);
    if (state.root && state.root.parentNode) {
      state.root.classList.add("plg-out");
      var node = state.root;
      window.setTimeout(function () { if (node.parentNode) node.parentNode.removeChild(node); }, 180);
    }
    state.root = null;
    state.rows = [];
    state.target = null;
  }

  /* ── 工程载入时把插件装回引擎并恢复状态（P5 §4） ──
     引擎重启的会话内重放由守护器负责；这里是"重开工程/重启应用"路径：
     工程里记着 plugin 引用，就重新装载并推回保存的状态。 */
  var engineQueue               = Promise.resolve();
  var session      = null;
  function enqueue(ctrl     , task                  , valid              ) {
    var project=ctrl.projectId,epoch=ctrl.projectEpoch;
    var current=function(){return ctrl.projectId===project && ctrl.projectEpoch===epoch && (!valid || valid());};
    var result=engineQueue.catch(function(){}).then(async function(){if(!current())return false;await task();return current();});
    engineQueue=result.catch(function(){});
    return result;
  }
  function identity(ctrl     , job     ) {
    var body      = {project:ctrl.projectId,generation:ctrl.projectEpoch,master:!!job.master,ref:job.ref,format:job.format};
    if(!job.master)body.trackId=job.trackId;
    if(job.slot>=0)body.slot=job.slot;
    return body;
  }
  function resolvePlugin(ref        ) {
    var registry=window.SoundRegistry;
    return Promise.resolve(registry ? registry.load() : []).then(function(){
      var src=registry && registry.get(ref);
      if(src && !src.enabled)throw new Error("插件已禁用："+ref);
      if(src && (src.kind==="mm" || src.kind==="vst3"))return window.UI.getJSON("/api/sound/plugin?id="+encodeURIComponent(src.id));
      if(ref.indexOf("VST3|")===0) {
        return window.UI.getJSON("/api/engine/plugins").then(function(res     ){
          var path=src && registry.diskPathOf(src);
          var hit=(res.plugins||[]).find(function(p     ){return p.id===ref || path && p.path===path && (!src.classUid || p.id===src.classUid || p.uid===src.classUid);});
          if(!hit)throw new Error("VST3 未安装："+ref);
          return {id:hit.id,format:"vst3"};
        });
      }
      return window.UI.getJSON("/api/engine/plugins/installed").then(function(res     ){
        var matches=(res.plugins||[]).filter(function(p     ){return p.id===ref;});
        if(matches.length>1)throw new Error("多个插件版本已安装，请选择音源注册表条目："+ref);
        var hit=matches[0];
        if(!hit||!hit.libPath)throw new Error("插件引用无法解析："+ref);
        return {id:hit.id,format:"mm",path:hit.libPath};
      });
    });
  }
  function collectJobs(ctrl     ) {
    var jobs       =[];
    (ctrl.tracks||[]).forEach(function(t     ){
      var item=rackItemOf(ctrl,t);
      if(item&&(item.kind==="mm"||item.kind==="vst3"))jobs.push({trackId:t.id,slot:-1,ref:item.sourceId||item.path||item.id});
      (((ctrl.mixer||{}).channels||{})[t.id]?.fx||[]).forEach(function(f     ){if(f&&(f.sourceId||f.plugin))jobs.push({trackId:t.id,slot:f.slot,ref:f.sourceId||f.plugin});});
    });
    (ctrl.mixer?.master?.fx||[]).forEach(function(f     ){if(f&&(f.sourceId||f.plugin))jobs.push({master:true,slot:f.slot,ref:f.sourceId||f.plugin});});
    return jobs;
  }
  function trackIndexOf(ctrl     ,id        ){return (ctrl.tracks||[]).findIndex(function(t     ){return t.id===id;});}
  function rackItemOf(ctrl     ,t     ){return (ctrl.rack?.items||[]).find(function(i     ){return i.id===t.source?.itemId;});}
  function restoreAll(ctrl     ) {
    if(!ctrl)return Promise.resolve();
    if(window.AudioBackend && !window.AudioBackend.isEngine()){ctrl.audioSync="ready";return Promise.resolve();}
    var snapshot=JSON.parse(JSON.stringify({projectId:ctrl.projectId,projectEpoch:ctrl.projectEpoch,tracks:ctrl.tracks,rack:ctrl.rack,mixer:ctrl.mixer}));
    var jobs=collectJobs(snapshot), signature=JSON.stringify([snapshot.projectId,snapshot.projectEpoch,snapshot.tracks.map(function(t     ){return t.id;}),jobs]);
    var requestSignature=signature+JSON.stringify(snapshot.mixer);
    if(ctrl._audioRequestedSignature===requestSignature && ctrl.audioSync!=="failed")return engineQueue.catch(function(){});
    ctrl._audioRequestedSignature=requestSignature;
    var sequence=ctrl._audioSyncRequest=(ctrl._audioSyncRequest||0)+1;
    if((!session || session.signature!==signature) && ctrl.isPlaying && ctrl.pausePlayback)ctrl.pausePlayback(true);
    var current=function(){return ctrl.projectId===snapshot.projectId&&ctrl.projectEpoch===snapshot.projectEpoch&&ctrl._audioSyncRequest===sequence;};
    ctrl.audioSync="waiting";
    engineQueue=engineQueue.catch(function(){}).then(async function(){
      if(!current())return;
      var fresh=!session||session.signature!==signature;
      if(fresh) {
        /* 上次恢复从未到达 ready（signature 为空）时快照还是引擎默认状态，
           备份回去会把默认状态写进工程 .state 文件——整个备份跳过。 */
        if(session && session.signature)for(var old of session.jobs){
          if(!current())return;
          try{await window.UI.postJSON("/api/engine/plugins/state/save",identity(session.snapshot,old));}catch(e     ){throw new Error(old.ref+" 状态备份失败："+e.message);}
        }
        if(!current())return;
        await window.UI.postJSON("/api/engine/plugins/session",{project:snapshot.projectId||"",generation:snapshot.projectEpoch,tracks:snapshot.tracks.map(function(t     ){return t.id;})});
        session={snapshot:snapshot,signature:"",jobs:[]};
        for(var job of jobs){
          if(!current())return;
          var resolved=await resolvePlugin(job.ref);
          if(!current())return;
          job.format=resolved.format;
          await window.UI.postJSON("/api/engine/plugins/load",Object.assign(identity(snapshot,job),resolved));
          session.jobs.push(job);
        }
      }
      if(!current())return;
      var bridge=window.EngineBridge;
      if(bridge&&bridge.setMaster)await bridge.setMaster(snapshot.mixer?.master?.gain===undefined?1:snapshot.mixer.master.gain);
      for(var tr=-1;tr<snapshot.tracks.length;++tr){
        if(!current())return;
        var fx=tr<0?snapshot.mixer?.master?.fx:snapshot.mixer?.channels?.[snapshot.tracks[tr].id]?.fx;
        if(bridge&&bridge.setFxChain)await bridge.setFxChain(tr,(fx||[]).map(function(f     ){return {slot:f.slot,bypass:!!f.bypass,wet:f.wet===undefined?1:f.wet};}));
      }
      if(fresh)for(var loaded of session.jobs){
        if(!current())return;
        try{await window.UI.postJSON("/api/engine/plugins/state/load",identity(snapshot,loaded));}catch(e     ){
          if(e.status===409 && e.data?.code==="plugin_state_reference_mismatch")toast(loaded.ref+" 已使用初始状态；旧插件状态已保留","warn");
          else if(e.status!==404)throw new Error(loaded.ref+" 状态恢复失败："+e.message);
        }
      }
      if(!current())return;
      session.signature=signature;session.snapshot=snapshot;ctrl.audioSync="ready";
      saveAllWarned=false;
    }).catch(function(e     ){if(current()){ctrl.audioSync="failed";toast("工程音频恢复失败："+e.message,"err");}throw e;});
    return engineQueue.catch(function(){});
  }

  var saveAllWarned = false;
  function saveAll(ctrl     ) {
    if (!ctrl || !ctrl.projectId) return Promise.resolve();
    if (window.AudioBackend && !window.AudioBackend.isEngine()) return Promise.resolve();
    if (!session || !session.jobs || !session.jobs.length) return Promise.resolve();
    if (!session.signature) {
      /* 上次音频恢复未完成：快照可能是引擎默认状态，保存会把默认值固化进工程 */
      if (!saveAllWarned) { saveAllWarned = true; toast("插件状态未保存：上次音频恢复未完成", "warn"); }
      return Promise.resolve();
    }
    var project = ctrl.projectId;
    var epoch = ctrl.projectEpoch;
    return enqueue(ctrl, async function () {
      var failCount = 0;
      for (var job of session.jobs) {
        if (ctrl.projectId !== project || ctrl.projectEpoch !== epoch) return;
        try {
          await window.UI.postJSON("/api/engine/plugins/state/save", identity(session.snapshot || ctrl, job));
        } catch (e     ) {
          failCount++;
          if (console && console.warn) console.warn("插件状态保存失败:", job.ref, e);
        }
      }
      if (failCount > 0) toast(failCount + " 个插件状态保存失败", "err");
    });
  }

  window.PluginPanel = {
    open: open,
    close: close,
    restoreAll: restoreAll,
    saveAll: saveAll,
    resolvePlugin: resolvePlugin,
    enqueue: enqueue,
    isOpen: function () { return state.open; }
  };
})(window);
