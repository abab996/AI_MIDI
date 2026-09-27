/* 混音台 v1（P4，规格见 4.5 节）
 *
 * 底部升起面板：通道条与轨道 1:1（增删轨道即时增删通道）、主通道固定右侧。
 * 每条通道：色条 / 名称（双击改名，与轨道头联动）/ 自绘推子（dB 刻度 + 峰值
 * 保持）/ 声像 / M,S / 8 个 FX 槽 / 双条电平表。
 *
 * 数据：
 *   · 权威状态在 arrangement.mixer（channels[trackId] / master）。推子改的是
 *     track.volume（既有语义：0~1 线性增益，轨道头滑杆也改它），pan / fx /
 *     master 只存在 mixer 里；
 *   · 写入走 ops API（与 AI 同一条路径）。拖拽期间只打本地 + 引擎，松手才发
 *     一次 op —— 60fps 的 op 洪流会白占管道与磁盘；
 *   · 电平表每帧只改 transform / opacity（见 4.10），面板关闭即停 rAF；
 *   · FX 槽在 P4 只承载槽参数（旁通 / 干湿比 / 清空）。插件装载在 P5（VST3）/
 *     P6（MM）开放，槽菜单如实标注未开放，不做假状态。
 */
(function (window) {
  "use strict";

  var UI = window.UI;

  /** UI.toast 在类型里是可选的（浏览器模式可能没有）——统一走这层包一下 */
  function toast(msg: string, kind?: string) {
    if (UI && UI.toast) UI.toast(msg, kind);
  }

  var MAX_SLOTS = 8;
  var MIN_DB = -60;
  var MAX_DB = 6;
  var MASTER_TRACK = -1;

  type MixerUiState = {
    open: boolean;
    raf: number;
    ids: string[];
    peakHold: { [key: string]: number };
  };

  var state: MixerUiState = { open: false, raf: 0, ids: [], peakHold: {} };

  /* ═══════════ 数值换算 ═══════════ */

  /** 增益(0~1 线性) → dB 文本 */
  function gainToDbText(gain: number) {
    if (!(gain > 0.0001)) return "-∞";
    var db = 20 * Math.log10(gain);
    if (db <= MIN_DB) return "-∞";
    return (db >= 0 ? "+" : "") + db.toFixed(1);
  }

  /** 推子位置 ↔ 增益：线性（与 track.volume 既有语义一致），显示成 dB 刻度 */
  function posToGain(pos: number) { return clamp01(pos); }
  function gainToPos(gain: number) { return clamp01(gain); }

  /** 峰值 → 表高比例（-60dB 起画） */
  function levelToRatio(level: number) {
    if (!(level > 0.00001)) return 0;
    var db = 20 * Math.log10(level);
    if (db <= MIN_DB) return 0;
    if (db >= MAX_DB) return 1;
    return (db - MIN_DB) / (MAX_DB - MIN_DB);
  }

  function clamp01(v: number) { return v < 0 ? 0 : (v > 1 ? 1 : v); }

  /* ═══════════ 状态访问 ═══════════ */

  function mixerState(ctrl: ArrangeController) {
    if (!ctrl.mixer) ctrl.mixer = { channels: {}, master: { gain: 1, fx: [] } };
    if (!ctrl.mixer.channels) ctrl.mixer.channels = {};
    if (!ctrl.mixer.master) ctrl.mixer.master = { gain: 1, fx: [] };
    return ctrl.mixer;
  }

  function channelOf(ctrl: ArrangeController, trackId: string) {
    var mixer = mixerState(ctrl);
    var ch = mixer.channels[trackId];
    if (!ch) {
      ch = { gain: 1, pan: 0, mute: false, solo: false, fx: [] };
      mixer.channels[trackId] = ch;
    }
    if (!ch.fx) ch.fx = [];
    return ch;
  }

  function masterOf(ctrl: ArrangeController) {
    var m = mixerState(ctrl).master!;
    if (typeof m.gain !== "number") m.gain = 1;
    if (!m.fx) m.fx = [];
    return m;
  }

  /** 通道增益以 track.volume 为准（轨道头滑杆同源），mixer 里的 gain 是镜像 */
  function gainOf(ctrl: ArrangeController, idx: number) {
    var track = ctrl.tracks[idx];
    if (!track) return 1;
    if (typeof track.volume === "number") return track.volume;
    return channelOf(ctrl, track.id).gain || 1;
  }

  function panOf(ctrl: ArrangeController, idx: number) {
    var track = ctrl.tracks[idx];
    if (!track) return 0;
    return channelOf(ctrl, track.id).pan || 0;
  }

  /* ═══════════ 开合 ═══════════ */

  function isOpen() { return state.open; }

  function toggle(ctrl: ArrangeController) {
    if (state.open) close(ctrl); else open(ctrl);
  }

  function open(ctrl: ArrangeController) {
    var panel = document.getElementById("mxrPanel");
    if (!panel) return;
    state.open = true;
    panel.hidden = false;
    panel.classList.remove("mxr-in");
    void panel.offsetWidth;
    panel.classList.add("mxr-in");
    var btn = document.getElementById("mxrBtn");
    if (btn) {
      btn.classList.add("active");
      btn.setAttribute("aria-expanded", "true");
    }
    render(ctrl);
    startMeters(ctrl);
  }

  function close(_ctrl: ArrangeController) {
    var panel = document.getElementById("mxrPanel");
    state.open = false;
    stopMeters();
    if (panel) {
      panel.classList.remove("mxr-in");
      panel.hidden = true;
    }
    var btn = document.getElementById("mxrBtn");
    if (btn) {
      btn.classList.remove("active");
      btn.setAttribute("aria-expanded", "false");
    }
  }

  function bind(ctrl: ArrangeController) {
    var btn = document.getElementById("mxrBtn");
    if (btn) btn.addEventListener("click", function () { toggle(ctrl); });
    var closeBtn = document.getElementById("mxrCloseBtn");
    if (closeBtn) closeBtn.addEventListener("click", function () { close(ctrl); });
    var lanes = document.getElementById("mxrLanes");
    if (lanes) {
      lanes.addEventListener("pointerdown", onPanelPointerDown);
      lanes.addEventListener("dblclick", onPanelDblClick);
      lanes.addEventListener("wheel", onPanelWheel, { passive: false });
      lanes.addEventListener("keydown", onPanelKeyDown);
    }
    var master = document.getElementById("mxrMaster");
    if (master) {
      master.addEventListener("pointerdown", onPanelPointerDown);
      master.addEventListener("dblclick", onPanelDblClick);
      master.addEventListener("wheel", onPanelWheel, { passive: false });
      master.addEventListener("keydown", onPanelKeyDown);
    }
  }

  /* ═══════════ 渲染 ═══════════ */

  function render(ctrl: ArrangeController) {
    var lanes = document.getElementById("mxrLanes");
    if (!lanes) return;
    lanes.innerHTML = "";
    state.ids = ctrl.tracks.map(function (t) { return t.id; });

    for (var i = 0; i < ctrl.tracks.length; i++) {
      lanes.appendChild(buildStrip(ctrl, i));
    }
    if (!ctrl.tracks.length) {
      var empty = document.createElement("div");
      empty.className = "mxr-empty";
      empty.textContent = "还没有轨道——先在编曲窗新建一条轨道";
      lanes.appendChild(empty);
    }
    var master = document.getElementById("mxrMaster");
    if (master) buildMasterInto(ctrl, master);
    var count = document.getElementById("mxrCount");
    if (count) count.textContent = ctrl.tracks.length + " CH";
  }

  /** 轨道增删/改名/换色后增量对齐：id 序列一致就只刷新内容（不整块重建） */
  function syncTracks(ctrl: ArrangeController) {
    if (!state.open) return;
    var lanes = document.getElementById("mxrLanes");
    if (!lanes) return;
    var ids = ctrl.tracks.map(function (t) { return t.id; });
    var same = ids.length === state.ids.length;
    if (same) {
      for (var k = 0; k < ids.length; k++) {
        if (ids[k] !== state.ids[k]) { same = false; break; }
      }
    }
    if (same) {
      for (var i = 0; i < ctrl.tracks.length; i++) refreshStrip(ctrl, i);
      return;
    }
    render(ctrl);
  }

  function buildStrip(ctrl: ArrangeController, idx: number) {
    var track = ctrl.tracks[idx];
    var strip = document.createElement("div");
    strip.className = "mxr-strip";
    strip.dataset.idx = String(idx);
    strip.dataset.trackId = track.id;
    strip.tabIndex = 0;
    strip.setAttribute("role", "group");
    strip.setAttribute("aria-label", "通道 " + (idx + 1) + " " + (track.name || ""));
    strip.classList.toggle("muted", !!track.mute);
    strip.classList.toggle("soloed", !!track.solo);

    var color = document.createElement("div");
    color.className = "mxr-color";
    color.style.background = track.color || "var(--color-primary)";
    strip.appendChild(color);

    var name = document.createElement("div");
    name.className = "mxr-name";
    name.textContent = track.name || ("轨道 " + (idx + 1));
    name.title = "双击改名（与轨道头同步）";
    name.dataset.act = "rename";
    strip.appendChild(name);

    var pan = document.createElement("div");
    pan.className = "mxr-pan";
    pan.dataset.act = "pan";
    pan.tabIndex = 0;
    pan.title = "声像（拖动调整，双击居中，←→ 微调）";
    pan.setAttribute("role", "slider");
    pan.setAttribute("aria-label", (track.name || "") + " 声像");
    var knob = document.createElement("div");
    knob.className = "mxr-pan-knob";
    pan.appendChild(knob);
    var panText = document.createElement("span");
    panText.className = "mxr-pan-text";
    pan.appendChild(panText);
    strip.appendChild(pan);
    paintPan(pan, panOf(ctrl, idx));

    strip.appendChild(buildFxRack(ctrl, idx, false));

    var faderRow = document.createElement("div");
    faderRow.className = "mxr-fader-row";
    faderRow.appendChild(buildMeter());
    faderRow.appendChild(buildFader(ctrl, idx, false));
    faderRow.appendChild(buildMeter());
    strip.appendChild(faderRow);

    var value = document.createElement("div");
    value.className = "mxr-value";
    value.textContent = gainToDbText(gainOf(ctrl, idx));
    strip.appendChild(value);

    var ms = document.createElement("div");
    ms.className = "mxr-ms";
    ms.appendChild(buildMsButton("M", "mute", !!track.mute, "静音"));
    ms.appendChild(buildMsButton("S", "solo", !!track.solo, "独奏"));
    strip.appendChild(ms);

    return strip;
  }

  function buildMsButton(label: string, act: string, on: boolean, title: string) {
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "mxr-ms-btn" + (act === "solo" ? " solo" : "") + (on ? " on" : "");
    btn.textContent = label;
    btn.dataset.act = act;
    btn.title = title;
    btn.tabIndex = -1;
    btn.setAttribute("aria-pressed", on ? "true" : "false");
    return btn;
  }

  function refreshStrip(ctrl: ArrangeController, idx: number) {
    var lanes = document.getElementById("mxrLanes");
    if (!lanes) return;
    var strip = lanes.querySelector('.mxr-strip[data-idx="' + idx + '"]') as HTMLElement | null;
    var track = ctrl.tracks[idx];
    if (!strip || !track) return;
    var name = strip.querySelector(".mxr-name");
    if (name) name.textContent = track.name || ("轨道 " + (idx + 1));
    var color = strip.querySelector(".mxr-color") as HTMLElement | null;
    if (color) color.style.background = track.color || "var(--color-primary)";
    strip.classList.toggle("muted", !!track.mute);
    strip.classList.toggle("soloed", !!track.solo);
    var mBtn = strip.querySelector('.mxr-ms-btn[data-act="mute"]') as HTMLElement | null;
    if (mBtn) {
      mBtn.classList.toggle("on", !!track.mute);
      mBtn.setAttribute("aria-pressed", track.mute ? "true" : "false");
    }
    var sBtn = strip.querySelector('.mxr-ms-btn[data-act="solo"]') as HTMLElement | null;
    if (sBtn) {
      sBtn.classList.toggle("on", !!track.solo);
      sBtn.setAttribute("aria-pressed", track.solo ? "true" : "false");
    }
    var pan = strip.querySelector(".mxr-pan");
    if (pan) paintPan(pan, panOf(ctrl, idx));
    var fader = strip.querySelector(".mxr-fader") as HTMLElement | null;
    if (fader) updateThumb(fader, gainOf(ctrl, idx));
    var value = strip.querySelector(".mxr-value");
    if (value) value.textContent = gainToDbText(gainOf(ctrl, idx));
    refreshFxRack(ctrl, idx, strip);
  }

  function buildMasterInto(ctrl: ArrangeController, host: HTMLElement) {
    host.innerHTML = "";
    var m = masterOf(ctrl);
    var label = document.createElement("div");
    label.className = "mxr-master-label";
    label.textContent = "MASTER";
    host.appendChild(label);

    host.appendChild(buildFxRack(ctrl, MASTER_TRACK, true));

    var faderRow = document.createElement("div");
    faderRow.className = "mxr-fader-row";
    faderRow.appendChild(buildMeter());
    faderRow.appendChild(buildFader(ctrl, MASTER_TRACK, true));
    faderRow.appendChild(buildMeter());
    host.appendChild(faderRow);

    var value = document.createElement("div");
    value.className = "mxr-value";
    value.textContent = gainToDbText(typeof m.gain === "number" ? m.gain : 1);
    host.appendChild(value);

    var reset = document.createElement("button");
    reset.type = "button";
    reset.className = "mxr-master-reset";
    reset.textContent = "复位 0dB";
    reset.title = "主推子复位到 0dB（双击主推子同效）";
    reset.addEventListener("click", function () { setMasterGain(ctrl, 1.0, true); });
    host.appendChild(reset);
  }

  /* ── 推子 / 声像 / 电平表 ── */

  function buildFader(ctrl: ArrangeController, idx: number, isMaster: boolean) {
    var fader = document.createElement("div");
    fader.className = "mxr-fader";
    fader.dataset.act = isMaster ? "master-fader" : "fader";
    if (!isMaster) fader.dataset.idx = String(idx);
    fader.tabIndex = 0;
    fader.setAttribute("role", "slider");
    fader.setAttribute("aria-label", (isMaster ? "主总线" : "通道 " + (idx + 1)) + " 音量");
    fader.setAttribute("aria-valuemin", "0");
    fader.setAttribute("aria-valuemax", "1");
    var track = document.createElement("div");
    track.className = "mxr-fader-track";
    fader.appendChild(track);
    var thumb = document.createElement("div");
    thumb.className = "mxr-fader-thumb";
    fader.appendChild(thumb);
    updateThumb(fader, isMaster ? (masterOf(ctrl).gain || 1) : gainOf(ctrl, idx));
    return fader;
  }

  function updateThumb(fader: HTMLElement, gain: number) {
    var thumb = fader.querySelector(".mxr-fader-thumb") as HTMLElement | null;
    if (thumb) thumb.style.bottom = (gainToPos(gain) * 100).toFixed(2) + "%";
    fader.setAttribute("aria-valuenow", gainToPos(gain).toFixed(3));
    fader.setAttribute("aria-valuetext", gainToDbText(gain) + " dB");
  }

  function buildMeter() {
    var wrap = document.createElement("div");
    wrap.className = "mxr-meter";
    var fill = document.createElement("div");
    fill.className = "mxr-meter-fill";
    wrap.appendChild(fill);
    var peak = document.createElement("div");
    peak.className = "mxr-meter-peak";
    wrap.appendChild(peak);
    return wrap;
  }

  function buildFxRack(ctrl: ArrangeController, idx: number, isMaster: boolean) {
    var rack = document.createElement("div");
    rack.className = "mxr-fx";
    if (!isMaster) rack.dataset.idx = String(idx);
    rack.dataset.master = isMaster ? "1" : "0";
    var ch = isMaster ? masterOf(ctrl) : channelOf(ctrl, ctrl.tracks[idx].id);
    var fx: any[] = ch.fx || [];
    for (var slot = 0; slot < MAX_SLOTS; slot++) {
      rack.appendChild(buildFxSlot(fx[slot], slot));
    }
    return rack;
  }

  function buildFxSlot(entry: any, slot: number) {
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "mxr-fx-slot";
    btn.dataset.slot = String(slot);
    btn.dataset.act = "fx";
    btn.tabIndex = -1;
    var plugin = entry && entry.plugin ? String(entry.plugin) : "";
    var bypass = entry ? entry.bypass !== false : true;
    btn.classList.toggle("loaded", !!plugin);
    btn.classList.toggle("bypass", bypass);
    btn.textContent = plugin ? shortPluginName(plugin) : String(slot + 1);
    btn.title = plugin
      ? "FX " + (slot + 1) + "：" + plugin + (bypass ? "（旁通）" : "")
      : "FX " + (slot + 1) + "：空槽（P5 起可装载插件）";
    btn.setAttribute("aria-label", btn.title);
    return btn;
  }

  function shortPluginName(ref: string) {
    var name = String(ref).split(/[\\/]/).pop() || ref;
    name = name.replace(/\.(vst3|dll|mm)$/i, "");
    if (name.indexOf(":") >= 0) name = name.split(":").pop() || name;
    return name.length > 7 ? name.slice(0, 6) + "…" : name;
  }

  function refreshFxRack(ctrl: ArrangeController, idx: number, scope: HTMLElement) {
    var rack = scope.querySelector(".mxr-fx") as HTMLElement | null;
    if (!rack) return;
    var ch = channelOf(ctrl, ctrl.tracks[idx].id);
    var fx: any[] = ch.fx || [];
    UI.qsa(".mxr-fx-slot", rack).forEach(function (btn: any, i: number) {
      var fresh = buildFxSlot(fx[i], i);
      btn.className = fresh.className;
      btn.textContent = fresh.textContent;
      btn.title = fresh.title;
    });
  }

  function paintPan(panEl: any, pan: number) {
    var knob = panEl.querySelector(".mxr-pan-knob") as HTMLElement | null;
    if (knob) knob.style.transform = "rotate(" + (pan * 135).toFixed(1) + "deg)";
    var text = panEl.querySelector(".mxr-pan-text") as HTMLElement | null;
    if (text) {
      var v = Math.round(pan * 100);
      text.textContent = v === 0 ? "C" : (v < 0 ? "L" + (-v) : "R" + v);
    }
  }

  /* ═══════════ 交互 ═══════════ */

  function stripIndexOf(el: HTMLElement | null) {
    var strip = el && el.closest ? el.closest(".mxr-strip") : null;
    return strip ? parseInt((strip as HTMLElement).dataset.idx || "-1", 10) : -1;
  }

  function onPanelPointerDown(e: any) {
    var ctrl = window.Arrange;
    if (!ctrl) return;
    var t = e.target as HTMLElement;
    var act = t.dataset ? t.dataset.act : "";
    var idx = stripIndexOf(t);

    if (act === "fader" || act === "master-fader" || act === "pan") {
      e.preventDefault();
      var isMaster = act === "master-fader";
      t.classList.add("dragging");
      try { t.setPointerCapture(e.pointerId); } catch (err) {}
      var move = function (ev: any) {
        if (act === "pan") applyPanFromEvent(ctrl!, idx, t, ev);
        else applyFaderFromEvent(ctrl!, isMaster ? MASTER_TRACK : idx, t, ev);
      };
      var up = function () {
        t.classList.remove("dragging");
        document.removeEventListener("pointermove", move);
        document.removeEventListener("pointerup", up);
        /* 松手才写（拖拽中的中间值只打本地 + 引擎防抖同步） */
        if (act === "pan") commitChannel(ctrl!, idx, { pan: panOf(ctrl!, idx) });
        else commitFader(ctrl!, isMaster ? MASTER_TRACK : idx);
      };
      document.addEventListener("pointermove", move);
      document.addEventListener("pointerup", up);
      move(e);
      return;
    }

    if (act === "mute" && idx >= 0) { e.preventDefault(); toggleMute(ctrl, idx); return; }
    if (act === "solo" && idx >= 0) { e.preventDefault(); toggleSolo(ctrl, idx); return; }
    if (act === "fx") {
      e.preventDefault();
      var rack = t.closest(".mxr-fx") as HTMLElement | null;
      var slot = parseInt(t.dataset.slot || "0", 10);
      var isMasterRack = rack ? rack.dataset.master === "1" : false;
      openFxMenu(ctrl, isMasterRack ? MASTER_TRACK : idx, slot, t);
      return;
    }
  }

  function onPanelDblClick(e: any) {
    var ctrl = window.Arrange;
    if (!ctrl) return;
    var t = e.target as HTMLElement;
    var act = t.dataset ? t.dataset.act : "";
    var idx = stripIndexOf(t);
    if (act === "fader" && idx >= 0) { setGain(ctrl, idx, 1.0, true); return; }
    if (act === "master-fader") { setMasterGain(ctrl, 1.0, true); return; }
    if (act === "pan" && idx >= 0) {
      var track = ctrl.tracks[idx];
      if (!track) return;
      channelOf(ctrl, track.id).pan = 0;
      paintPan(t, 0);
      syncEngineChannel(ctrl, idx);
      commitChannel(ctrl, idx, { pan: 0 });
      return;
    }
    if (act === "rename" && idx >= 0) renameTrack(ctrl, idx, t);
  }

  function onPanelWheel(e: any) {
    var ctrl = window.Arrange;
    if (!ctrl) return;
    var t = e.target as HTMLElement;
    var act = t.dataset ? t.dataset.act : "";
    if (act !== "fader" && act !== "master-fader") return;
    e.preventDefault();
    var step = (e.shiftKey ? 0.005 : 0.02) * (e.deltaY > 0 ? -1 : 1);
    var idx = stripIndexOf(t);
    if (act === "master-fader") setMasterGain(ctrl, clamp01((masterOf(ctrl).gain || 1) + step), true);
    else if (idx >= 0) setGain(ctrl, idx, clamp01(gainOf(ctrl, idx) + step), true);
  }

  /** 键盘：↑↓ 推子（Shift 细调）、←→ 声像、Enter/Space 切 M/S（4.5 键盘可达） */
  function onPanelKeyDown(e: any) {
    var ctrl = window.Arrange;
    if (!ctrl) return;
    var t = e.target as HTMLElement;
    var act = t.dataset ? t.dataset.act : "";
    var idx = stripIndexOf(t);
    var k = e.key;
    if (act === "fader" || act === "master-fader") {
      if (k !== "ArrowUp" && k !== "ArrowDown") return;
      e.preventDefault();
      e.stopPropagation();
      var stepDb = e.shiftKey ? 0.5 : 2;
      var dir = k === "ArrowUp" ? 1 : -1;
      var cur = act === "master-fader" ? (masterOf(ctrl).gain || 1) : gainOf(ctrl, idx);
      var next = clamp01(cur * Math.pow(10, (dir * stepDb) / 20));
      if (act === "master-fader") setMasterGain(ctrl, next, true);
      else setGain(ctrl, idx, next, true);
      return;
    }
    if (act === "pan") {
      if (k !== "ArrowLeft" && k !== "ArrowRight") return;
      e.preventDefault();
      e.stopPropagation();
      var track = ctrl.tracks[idx];
      if (!track) return;
      var pan = panOf(ctrl, idx) + (k === "ArrowRight" ? 0.05 : -0.05);
      pan = Math.max(-1, Math.min(1, pan));
      channelOf(ctrl, track.id).pan = pan;
      paintPan(t, pan);
      syncEngineChannel(ctrl, idx);
      commitChannel(ctrl, idx, { pan: pan });
      return;
    }
    if (act === "mute" && (k === "Enter" || k === " ")) { e.preventDefault(); toggleMute(ctrl, idx); return; }
    if (act === "solo" && (k === "Enter" || k === " ")) { e.preventDefault(); toggleSolo(ctrl, idx); return; }
  }

  function applyFaderFromEvent(ctrl: ArrangeController, idx: number, fader: HTMLElement, ev: any) {
    var rect = fader.getBoundingClientRect();
    var pos = rect.height > 0 ? (rect.bottom - ev.clientY) / rect.height : 0;
    if (idx === MASTER_TRACK) setMasterGain(ctrl, clamp01(pos), false);
    else setGain(ctrl, idx, clamp01(pos), false);
  }

  function applyPanFromEvent(ctrl: ArrangeController, idx: number, panEl: HTMLElement, ev: any) {
    if (idx < 0) return;
    var rect = panEl.getBoundingClientRect();
    var pan = rect.width > 0 ? ((ev.clientX - rect.left) / rect.width) * 2 - 1 : 0;
    pan = Math.max(-1, Math.min(1, pan));
    var track = ctrl.tracks[idx];
    if (!track) return;
    channelOf(ctrl, track.id).pan = pan;
    paintPan(panEl, pan);
    syncEngineChannel(ctrl, idx);
  }

  /* ═══════════ 写入（本地 + 引擎 + ops） ═══════════ */

  /** 同步到引擎：走编曲窗的 applyMixSafe（整表 80ms 防抖合并）。
      拖拽期间 60fps 逐轨 IPC 会打满管道，合并后一次下发全部通道，
      且天然覆盖"删轨后尾槽 active=false"的收缩语义 */
  function syncEngineChannel(ctrl: ArrangeController, idx: number) {
    if (ctrl.applyMixSafe) { ctrl.applyMixSafe(); return; }
    var track = ctrl.tracks[idx] as any;
    if (!track) return;
    var bridge = window.EngineBridge;
    if (!bridge || !bridge.setTrackMix) return;
    try {
      bridge.setTrackMix(idx, gainOf(ctrl, idx), panOf(ctrl, idx), !!track.mute, !!track.solo, true);
    } catch (e) {}
  }

  function setGain(ctrl: ArrangeController, idx: number, gain: number, commit: boolean) {
    var track = ctrl.tracks[idx];
    if (!track) return;
    track.volume = clamp01(gain);
    channelOf(ctrl, track.id).gain = track.volume;
    var lanes = document.getElementById("mxrLanes");
    var strip = lanes ? lanes.querySelector('.mxr-strip[data-idx="' + idx + '"]') : null;
    if (strip) {
      var fader = strip.querySelector(".mxr-fader") as HTMLElement | null;
      if (fader) updateThumb(fader, track.volume);
      var value = strip.querySelector(".mxr-value");
      if (value) value.textContent = gainToDbText(track.volume);
    }
    syncEngineChannel(ctrl, idx);
    if (commit) {
      commitChannel(ctrl, idx, { gain: track.volume });
      ctrl.renderTracks();
    }
  }

  function setMasterGain(ctrl: ArrangeController, gain: number, commit: boolean) {
    var m = masterOf(ctrl);
    m.gain = clamp01(gain);
    var host = document.getElementById("mxrMaster");
    if (host) {
      var fader = host.querySelector(".mxr-fader") as HTMLElement | null;
      if (fader) updateThumb(fader, m.gain);
      var value = host.querySelector(".mxr-value");
      if (value) value.textContent = gainToDbText(m.gain);
    }
    var bridge = window.EngineBridge;
    if (bridge && bridge.setMaster) {
      try { bridge.setMaster(m.gain); } catch (e) {}
    }
    if (commit) commitOps(ctrl, [{ op: "setMaster", gain: m.gain }]);
  }

  function toggleMute(ctrl: ArrangeController, idx: number) {
    var track = ctrl.tracks[idx];
    if (!track) return;
    track.mute = !track.mute;
    channelOf(ctrl, track.id).mute = track.mute;
    var lanes = document.getElementById("mxrLanes");
    var strip = lanes ? (lanes.querySelector('.mxr-strip[data-idx="' + idx + '"]') as HTMLElement | null) : null;
    if (strip) {
      strip.classList.toggle("muted", !!track.mute);
      var btn = strip.querySelector('.mxr-ms-btn[data-act="mute"]') as HTMLElement | null;
      if (btn) {
        btn.classList.toggle("on", !!track.mute);
        btn.setAttribute("aria-pressed", track.mute ? "true" : "false");
      }
    }
    syncEngineChannel(ctrl, idx);
    commitChannel(ctrl, idx, { mute: track.mute });
    ctrl.renderTracks();
  }

  function toggleSolo(ctrl: ArrangeController, idx: number) {
    var track = ctrl.tracks[idx];
    if (!track) return;
    track.solo = !track.solo;
    channelOf(ctrl, track.id).solo = track.solo;
    /* 独奏是全局语义：其余通道的"被压下"状态也要刷新 */
    var lanes = document.getElementById("mxrLanes");
    if (lanes) {
      UI.qsa(".mxr-strip", lanes).forEach(function (el: any) {
        var i = parseInt(el.dataset.idx || "-1", 10);
        var tr = ctrl.tracks[i];
        if (!tr) return;
        el.classList.toggle("soloed", !!tr.solo);
        var btn = el.querySelector('.mxr-ms-btn[data-act="solo"]');
        if (btn) {
          btn.classList.toggle("on", !!tr.solo);
          btn.setAttribute("aria-pressed", tr.solo ? "true" : "false");
        }
      });
    }
    syncEngineChannel(ctrl, idx);
    commitChannel(ctrl, idx, { solo: track.solo });
    ctrl.renderTracks();
  }

  function commitChannel(ctrl: ArrangeController, idx: number, patch: any) {
    var op: any = { op: "setTrackMix", track: idx };
    if (patch.gain !== undefined) op.gain = patch.gain;
    if (patch.pan !== undefined) op.pan = patch.pan;
    if (patch.mute !== undefined) op.mute = patch.mute;
    if (patch.solo !== undefined) op.solo = patch.solo;
    if (Object.keys(op).length <= 2) return;
    commitOps(ctrl, [op]);
  }

  function commitFader(ctrl: ArrangeController, idx: number) {
    if (idx === MASTER_TRACK) commitOps(ctrl, [{ op: "setMaster", gain: masterOf(ctrl).gain }]);
    else commitChannel(ctrl, idx, { gain: gainOf(ctrl, idx) });
  }

  /** 走 ops API（与 AI 同一条路径）；失败回退整份 PUT，绝不静默丢改动 */
  function commitOps(ctrl: ArrangeController, ops: any[]) {
    if (ctrl.arrangeOps) {
      ctrl.arrangeOps(ops).then(function (res: any) {
        if (res && res.ok) return;
        ctrl.scheduleSave();
      }).catch(function () {
        ctrl.scheduleSave();
      });
      return;
    }
    ctrl.scheduleSave();
  }

  /* ── FX 槽菜单 ── */

  function openFxMenu(ctrl: ArrangeController, idx: number, slot: number, anchor: HTMLElement) {
    var ch = idx === MASTER_TRACK ? masterOf(ctrl) : channelOf(ctrl, ctrl.tracks[idx].id);
    var fx: any[] = ch.fx || [];
    var entry = fx[slot] || { slot: slot, plugin: "", bypass: true, wet: 1.0 };
    var isBypass = entry.bypass !== false;
    var wet = entry.wet === undefined ? 1 : entry.wet;
    var rect = anchor.getBoundingClientRect();
    var items: any[] = [{
      heading: (idx === MASTER_TRACK ? "主总线" : "通道 " + (idx + 1)) + " · FX " + (slot + 1) +
        (entry.plugin ? "：" + entry.plugin : "（空槽）")
    }];
    items.push({
      label: isBypass ? "● 启用槽位" : "○ 设为旁通",
      action: function () { commitFx(ctrl, idx, slot, { bypass: isBypass }); }
    });
    [0, 0.25, 0.5, 0.75, 1].forEach(function (w) {
      items.push({
        label: "干湿比 " + Math.round(w * 100) + "%" + (Math.abs(wet - w) < 0.001 ? " ✓" : ""),
        action: function () { commitFx(ctrl, idx, slot, { wet: w }); }
      });
    });
    items.push("-");
    items.push({
      label: entry.plugin ? "🔌 更换插件…" : "🔌 装载插件…",
      action: function () { openPluginPicker(ctrl, idx, slot); }
    });
    /* 插件界面入口按类型分流：VST3 走引擎原生编辑器；MM（.mm 包里的原生库）
       没有原生窗口，走 mm-ui.html 承载页（sandbox iframe + postMessage 桥） */
    var isMm = !!entry.plugin && entry.plugin.indexOf("VST3|") !== 0;
    items.push({
      label: isMm ? "🎛 插件界面（MM 承载窗）…" : "🎛 插件界面（原生编辑器）…",
      disabled: !entry.plugin,
      action: function () { openPluginEditor(ctrl, idx, slot, isMm, entry.plugin || ""); }
    });
    items.push({
      label: "🗑 清空槽位",
      disabled: !entry.plugin && isBypass && wet === 1,
      action: function () { commitFx(ctrl, idx, slot, { off: true }); }
    });
    ctrl.showMenu(items, rect.left, rect.top - 8);
  }

  /** openPluginPicker 拉引擎插件清单（GET /api/engine/plugins）→ 菜单选择装载。
      浏览器模式没有原生引擎：接口 503，这里给可解释提示而不是空菜单。 */
  function openPluginPicker(ctrl: ArrangeController, idx: number, slot: number) {
    var where = (idx === MASTER_TRACK ? "主总线" : "通道 " + (idx + 1)) + " · FX " + (slot + 1);
    UI.getJSON("/api/engine/plugins").then(function (res: any) {
      var list = (res && res.plugins) || [];
      if (!list.length) {
        toast("还没有扫描到 VST3 插件：请在设置页「声音管理」里扫描插件目录", "warn");
        return;
      }
      var items: any[] = [{ heading: "装载插件到 " + where }];
      list.slice(0, 60).forEach(function (p: any) {
        items.push({
          label: (p.instrument ? "🎹 " : "🔧 ") + p.name + (p.vendor ? "（" + p.vendor + "）" : ""),
          action: function () {
            UI.postJSON("/api/engine/plugins/load", {
              id: p.id, track: idx === MASTER_TRACK ? 0 : idx, slot: slot,
              master: idx === MASTER_TRACK
            }).then(function () {
              commitFx(ctrl, idx, slot, { plugin: p.id, bypass: false });
              toast("✓ 已装载 " + p.name, "ok");
            }).catch(function (err: any) {
              toast("✗ 装载失败: " + (err && err.message ? err.message : "未知错误"), "err");
            });
          }
        });
      });
      ctrl.showMenu(items, 240, 180);
    }).catch(function () {
      toast("原生引擎未启动：VST3 插件仅在桌面版可用（浏览器模式请用 SF2/采样包音源）", "warn");
    });
  }

  /** openPluginEditor VST3 → 引擎原生编辑器；MM → mm-ui.html 承载页 */
  function openPluginEditor(ctrl: ArrangeController, idx: number, slot: number, isMm?: boolean, pluginRef?: string) {
    if (isMm) {
      var id = String(pluginRef || "").trim();
      var qs = "?plugin=" + encodeURIComponent(id) + "&slot=" + slot;
      if (idx === MASTER_TRACK) qs += "&master=1";
      else qs += "&track=" + idx;
      var theme = document.documentElement.dataset.theme;
      if (theme) qs += "&theme=" + encodeURIComponent(theme);
      var win = window.open("/mm-ui.html" + qs, "mmui_" + id + "_" + slot, "width=760,height=560");
      if (!win) toast("⚠ 浏览器拦截了弹窗：请允许本站弹窗后重试", "warn");
      return;
    }
    UI.postJSON("/api/engine/plugins/editor", {
      track: idx === MASTER_TRACK ? 0 : idx, slot: slot, master: idx === MASTER_TRACK
    }).then(function () {
      toast("已打开插件界面（原生窗口）", "ok");
    }).catch(function (err: any) {
      toast("✗ 打开插件界面失败: " + (err && err.message ? err.message : "未知错误"), "err");
    });
  }

  function commitFx(ctrl: ArrangeController, idx: number, slot: number, patch: any) {
    var op: any = { op: "setFxSlot", slot: slot };
    if (idx === MASTER_TRACK) op.master = true; else op.track = idx;
    if (patch.plugin !== undefined) op.plugin = patch.plugin;
    if (patch.bypass !== undefined) op.bypass = patch.bypass;
    if (patch.wet !== undefined) op.wet = patch.wet;
    if (patch.off) op.off = true;

    /* 本地先落（菜单即时反馈），再与服务端对齐 */
    var ch = idx === MASTER_TRACK ? masterOf(ctrl) : channelOf(ctrl, ctrl.tracks[idx].id);
    var fx: any[] = ch.fx || (ch.fx = []);
    while (fx.length < MAX_SLOTS) fx.push({ slot: fx.length, bypass: true, wet: 1.0 });
    var entry = fx[slot] || (fx[slot] = { slot: slot, bypass: true, wet: 1.0 });
    if (patch.off) { entry.plugin = ""; entry.bypass = true; entry.wet = 1; }
    if (patch.plugin !== undefined) entry.plugin = patch.plugin;
    if (patch.bypass !== undefined) entry.bypass = patch.bypass;
    if (patch.wet !== undefined) entry.wet = patch.wet;

    if (idx === MASTER_TRACK) {
      var host = document.getElementById("mxrMaster");
      if (host) buildMasterInto(ctrl, host);
    } else {
      var lanes = document.getElementById("mxrLanes");
      var strip = lanes ? (lanes.querySelector('.mxr-strip[data-idx="' + idx + '"]') as HTMLElement | null) : null;
      if (strip) refreshFxRack(ctrl, idx, strip);
    }

    /* 引擎：P4 可下发槽参数；插件引用会被引擎如实拒绝（不静默） */
    var bridge = window.EngineBridge;
    var setChain = bridge ? bridge.setFxChain : undefined;
    if (setChain) {
      var payload: any = {
        slot: slot,
        bypass: entry.bypass !== false,
        wet: entry.wet === undefined ? 1 : entry.wet
      };
      if (entry.plugin) payload.plugin = entry.plugin;
      if (patch.off) payload.off = true;
      try {
        setChain.call(bridge, idx === MASTER_TRACK ? -1 : idx, [payload]).catch(function (err: any) {
          var msg = err && err.message ? err.message : String(err);
          if (msg.indexOf("P5") >= 0 || msg.indexOf("P6") >= 0) {
            toast("ℹ 插件装载将在 P5（VST3）/ P6（MM）开放，槽位参数已保存", "warn");
          } else {
            toast("⚠ FX 链下发失败: " + msg, "err");
          }
        });
      } catch (e) {}
    }
    commitOps(ctrl, [op]);
  }

  function renameTrack(ctrl: ArrangeController, idx: number, _nameEl: HTMLElement) {
    /* 复用编曲窗的重命名弹窗（与 F2 同一条流程）：写入走 ops renametrack，
       轨道头与混音台随后一起刷新，不会出现两处名字不一致 */
    if (ctrl.openRenameModal) ctrl.openRenameModal(idx);
  }

  /* ═══════════ 电平表 ═══════════ */

  function startMeters(ctrl: ArrangeController) {
    stopMeters();
    var tick = function () {
      if (!state.open) return;
      paintMeters(ctrl);
      state.raf = window.requestAnimationFrame(tick);
    };
    state.raf = window.requestAnimationFrame(tick);
  }

  function stopMeters() {
    if (state.raf) {
      window.cancelAnimationFrame(state.raf);
      state.raf = 0;
    }
  }

  function paintMeters(ctrl: ArrangeController) {
    var levels = window.__engineLevels || [];
    var lanes = document.getElementById("mxrLanes");
    if (lanes) {
      UI.qsa(".mxr-strip", lanes).forEach(function (strip: any) {
        var idx = parseInt(strip.dataset.idx || "-1", 10);
        var track = ctrl.tracks[idx];
        if (!track) return;
        paintMeterPair(strip, track.id, levels[idx] || 0);
      });
    }
    var masterHost = document.getElementById("mxrMaster");
    if (masterHost) {
      var masterLevel = typeof window.__engineMasterLevel === "number" ? window.__engineMasterLevel : 0;
      paintMeterPair(masterHost, "master", masterLevel);
    }
  }

  function paintMeterPair(scope: HTMLElement, key: string, level: number) {
    var ratio = levelToRatio(level);
    var prev = state.peakHold[key] || 0;
    var hold = ratio > prev ? ratio : Math.max(ratio, prev - 0.008); // 峰值保持缓慢回落
    state.peakHold[key] = hold;

    var meters = UI.qsa(".mxr-meter", scope);
    if (!meters.length) return;
    var ratios = [ratio, ratio * 0.92]; // 单声道数据也给一点视觉宽度
    meters.forEach(function (m: any, i: number) {
      var r = ratios[i % 2];
      var fill = m.querySelector(".mxr-meter-fill") as HTMLElement | null;
      if (fill) fill.style.transform = "scaleY(" + r.toFixed(3) + ")";
      m.classList.toggle("hot", r > 0.92);
    });
    var peakEl = meters[0].querySelector(".mxr-meter-peak") as HTMLElement | null;
    if (peakEl) {
      peakEl.style.bottom = (hold * 100).toFixed(2) + "%";
      peakEl.style.opacity = hold > 0.02 ? "1" : "0";
    }
  }

  /* ═══════════ 对外 ═══════════ */

  window.ArrMixer = {
    MAX_SLOTS: MAX_SLOTS,
    MIN_DB: MIN_DB,
    MAX_DB: MAX_DB,
    MASTER_TRACK: MASTER_TRACK,
    state: state,
    bind: bind,
    isOpen: isOpen,
    open: open,
    close: close,
    toggle: toggle,
    render: render,
    syncTracks: syncTracks,
    refreshStrip: refreshStrip,
    gainToDbText: gainToDbText,
    posToGain: posToGain,
    gainToPos: gainToPos,
    levelToRatio: levelToRatio,
    channelOf: channelOf,
    masterOf: masterOf,
    gainOf: gainOf,
    panOf: panOf,
    setGain: setGain,
    setMasterGain: setMasterGain,
    toggleMute: toggleMute,
    toggleSolo: toggleSolo,
    commitFx: commitFx,
    paintMeters: paintMeters,
    shortPluginName: shortPluginName,
    _resetForTest: function () {
      state.peakHold = {};
      state.ids = [];
      state.open = false;
      if (state.raf) window.cancelAnimationFrame(state.raf);
      state.raf = 0;
    }
  };
})(window);
