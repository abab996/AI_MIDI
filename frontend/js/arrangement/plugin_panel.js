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
  var isMmRef = function (ref        ) { return !!ref && ref.indexOf("VST3|") !== 0; };

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
    var q = "?track=" + encodeURIComponent(t.track);
    if (t.slot >= 0) q += "&slot=" + encodeURIComponent(t.slot);
    if (t.master) q += "&master=1";
    return "/api/engine/plugins/params" + q;
  }

  function bodyOf(t     , extra      ) {
    var body      = { track: t.master ? 0 : t.track, master: !!t.master };
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
    var index = Number(keys[0]);
    var value = state.pending[index];
    delete state.pending[index];
    window.UI.postJSON("/api/engine/plugins/param", bodyOf(t, { index: index, value: value }))
      .then(function (res     ) {
        // 引擎回读：用它校正显示（步进参数会被量化）
        applyEngineParams(res);
      })
      .catch(function (err     ) {
        toast("✗ 参数下发失败：" + ((err && err.message) || "未知错误"), "err");
        refresh();
      });
    if (Object.keys(state.pending).length) scheduleFlush();
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
    if (isMmRef(t.ref)) title.appendChild(el("span", "plg-badge-ai", "MM"));
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
    btn("🖥 插件界面", isMmRef(t.ref) ? "打开 MM 承载窗" : "打开插件原生编辑器", openEditor);
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
    var body = bodyOf(t, { project: t.project, ref: t.ref || "", format: isMmRef(t.ref) ? "mm" : "vst3" });
    if (extra) for (var k in extra) body[k] = extra[k];
    return body;
  }

  function saveState() {
    if (!state.target || !state.target.project) { toast("⚠ 没有打开的工程，无法保存插件状态", "warn"); return; }
    window.UI.postJSON("/api/engine/plugins/state/save", stateBody())
      .then(function (res     ) {
        toast("✓ 插件状态已保存到工程（" + Math.round((res && res.bytes || 0) / 1024) + " KB）", "ok");
      })
      .catch(function (err     ) { toast("✗ 保存插件状态失败：" + ((err && err.message) || ""), "err"); });
  }

  function loadState() {
    if (!state.target || !state.target.project) { toast("⚠ 没有打开的工程，无法恢复插件状态", "warn"); return; }
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
    if (isMmRef(t.ref)) {
      /* MM：无原生窗口，走 mm-ui.html 承载页（sandbox iframe + postMessage 桥） */
      var qs = "?plugin=" + encodeURIComponent(t.ref) + "&slot=" + t.slot;
      if (t.master) qs += "&master=1"; else qs += "&track=" + t.track;
      var theme = document.documentElement.dataset.theme;
      if (theme) qs += "&theme=" + encodeURIComponent(theme);
      var win = window.open("/mm-ui.html" + qs, "mmui_" + t.ref + "_" + t.slot, "width=760,height=560");
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
        var params = (res && res.params) || [];
        if (!state.rows.length) {
          renderBody(state.root, params);
          var titleEl = state.root.querySelector(".plg-title");
          if (res && res.name && titleEl) titleEl.textContent = res.name + " · " + targetLabel(t);
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
      track: typeof target.track === "number" ? target.track : 0,
      slot: typeof target.slot === "number" ? target.slot : -1,
      master: !!target.master,
      ref: target.ref || "",
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
  function restoreAll(ctrl     ) {
    if (!ctrl || !ctrl.projectId) return;
    var jobs        = [];
    var channels = (ctrl.mixer && ctrl.mixer.channels) || {};
    Object.keys(channels).forEach(function (trackId        ) {
      var idx = trackIndexOf(ctrl, trackId);
      if (idx < 0) return;
      var fx = (channels[trackId] && channels[trackId].fx) || [];
      fx.forEach(function (entry     ) {
        if (!entry || !entry.plugin) return;
        jobs.push({ track: idx, slot: Number(entry.slot) || 0, master: false, ref: entry.plugin });
      });
    });
    var masterFx = (ctrl.mixer && ctrl.mixer.master && ctrl.mixer.master.fx) || [];
    masterFx.forEach(function (entry     ) {
      if (!entry || !entry.plugin) return;
      jobs.push({ track: 0, slot: Number(entry.slot) || 0, master: true, ref: entry.plugin });
    });
    /* 轨道乐器（机架里 kind=vst3/mm 的条目）：按注册表拿装载参数 */
    (ctrl.tracks || []).forEach(function (tr     , i        ) {
      var item = rackItemOf(ctrl, tr);
      if (!item || (item.kind !== "vst3" && item.kind !== "mm")) return;
      jobs.push({ track: i, slot: -1, master: false, ref: item.sourceId || item.id, instrument: true, item: item });
    });
    if (!jobs.length) return;
    var failed = 0;
    jobs.reduce(function (chain     , job     ) {
      return chain.then(function () { return restoreOne(ctrl, job).catch(function () { failed++; }); });
    }, Promise.resolve()).then(function () {
      if (failed) toast("⚠ 有 " + failed + " 个插件未能自动恢复（可能未安装或已卸载）", "warn");
    });
  }

  function trackIndexOf(ctrl     , trackId        ) {
    var list = ctrl.tracks || [];
    for (var i = 0; i < list.length; i++) if (list[i] && list[i].id === trackId) return i;
    return -1;
  }

  function rackItemOf(ctrl     , track     ) {
    if (!track || !track.source || !track.source.itemId) return null;
    var items = (ctrl.rack && ctrl.rack.items) || [];
    for (var i = 0; i < items.length; i++) if (items[i] && items[i].id === track.source.itemId) return items[i];
    return null;
  }

  function restoreOne(ctrl     , job     ) {
    if (job.instrument) {
      var item = job.item;
      if (item.kind === "mm") {
        /* MM 乐器：装载要的是原生库路径（引擎按路径 LoadLibrary） */
        return mmLibPathOf(job.ref || item.sourceId).then(function (libPath     ) {
          return loadAndRestore(ctrl, job, { id: job.ref || item.sourceId, format: "mm", path: libPath, track: job.track });
        });
      }
      /* VST3 乐器的引擎 id 形如 VST3|路径|uid，注册表只记了路径：
         用引擎清单按 path 反查 id（查不到就跳过，不猜） */
      return window.UI.getJSON("/api/engine/plugins").then(function (res     ) {
        var list = (res && res.plugins) || [];
        var hit = null;
        for (var i = 0; i < list.length; i++) {
          if (item.path && list[i].path === item.path) { hit = list[i]; break; }
        }
        if (!hit) return Promise.reject(new Error("引擎清单里找不到该 VST3"));
        return loadAndRestore(ctrl, { track: job.track, slot: -1, master: false, ref: hit.id }, { id: hit.id, track: job.track });
      });
    }
    if (isMmRef(job.ref)) {
      /* MM 效果器：ref 是插件 id，装载参数要从已安装清单取 libPath */
      return mmLibPathOf(job.ref).then(function (libPath     ) {
        var body      = { id: job.ref, format: "mm", path: libPath, track: job.master ? 0 : job.track };
        if (job.slot >= 0) body.slot = job.slot;
        return loadAndRestore(ctrl, job, body);
      });
    }
    var body      = { id: job.ref, track: job.master ? 0 : job.track, master: job.master };
    if (job.slot >= 0) body.slot = job.slot;
    return loadAndRestore(ctrl, job, body);
  }

  /** mmLibPathOf 按插件 id 从已安装清单取原生库路径（取不到即拒绝） */
  function mmLibPathOf(pluginID        ) {
    return window.UI.getJSON("/api/engine/plugins/installed").then(function (res     ) {
      var list = (res && res.plugins) || [];
      for (var i = 0; i < list.length; i++) {
        if (list[i] && list[i].id === pluginID) {
          if (list[i].libPath) return String(list[i].libPath);
          return Promise.reject(new Error("MM 插件缺少原生库：" + pluginID));
        }
      }
      return Promise.reject(new Error("MM 插件未安装：" + pluginID));
    });
  }

  function loadAndRestore(ctrl     , job     , loadBody     ) {
    return window.UI.postJSON("/api/engine/plugins/load", loadBody).then(function () {
      var body      = {
        project: ctrl.projectId, track: job.master ? 0 : job.track, master: !!job.master,
        ref: job.ref, format: isMmRef(job.ref) ? "mm" : "vst3"
      };
      if (job.slot >= 0) body.slot = job.slot;
      return window.UI.postJSON("/api/engine/plugins/state/load", body).catch(function () {
        /* 没有保存过状态不算失败：插件已经装回来了 */
      });
    });
  }

  window.PluginPanel = {
    open: open,
    close: close,
    restoreAll: restoreAll,
    isOpen: function () { return state.open; }
  };
})(window);
