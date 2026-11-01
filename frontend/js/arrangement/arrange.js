/* 编排窗口核心控制器 (FL Studio Playlist 风格)
   状态管理 / 渲染 / Clip 手势 / 拖放 / 走带 / 右键菜单 / 快捷键 */
(function (window) {
  "use strict";

  var UI = window.UI;
  var MidiParse = window.MidiParse;

  /* ═══════════ 常量 ═══════════ */
  var HEADER_W = 200;      // 轨道头列宽
  var RULER_H = 34;        // 标尺高度
  var BAR_BEATS = 4;       // 每小节拍数
  var MIN_BARS = 64;       // 内容最小小节数
  var MIN_CLIP_LEN = 0.125;

  var TRACK_COLORS = [
    "#00B8CC", "#FF8C00", "#06D6A0", "#5B8DEF", "#E86FB4",
    "#FFD166", "#9B8CFF", "#F4743B", "#4CC9F0", "#7ED957"
  ];

  var SNAPS = [
    { v: 0, label: "关闭吸附" },
    { v: 4, label: "1 小节" },
    { v: 1, label: "1 拍" },
    { v: 0.5, label: "1/2 拍" },
    { v: 0.25, label: "1/4 拍" }
  ];

  function uid(prefix     ) {
    return prefix + "_" + Date.now().toString(36) + "_" + Math.random().toString(36).slice(2, 7);
  }

  function hexToRgba(hex     , alpha     ) {
    var m = /^#([0-9a-f]{6})$/i.exec(hex || "");
    if (!m) return "rgba(0,184,204," + alpha + ")";
    var n = parseInt(m[1], 16);
    return "rgba(" + ((n >> 16) & 255) + "," + ((n >> 8) & 255) + "," + (n & 255) + "," + alpha + ")";
  }

  function clamp(v     , lo     , hi     ) { return Math.max(lo, Math.min(hi, v)); }

  function baseName(path     ) {
    return String(path || "").split(/[\\/]/).pop();
  }

  /* ═══════════ 控制器状态 ═══════════ */

  function Arrange(                       ) {
    this.el = {};
    this.initialized = false;
    this.isOpen = false;
    this.isFocused = false;       // 编排窗口持有快捷键焦点

    this.projectId = null;
    this.loadedProjectId = null;

    this.bpm = 120;
    this.snap = 0.25;
    this.ppb = 26;                // 每拍像素（横向缩放）
    this.loop = { on: false, start: 0, end: 16 };
    this.metronome = false;

    this.tracks = [];
    /* 项目乐器箱（arrangement.rack.items）：轨道与钢琴窗的音源只能来自这里。
       条目模型/解析/渲染见 arrangement/rack.ts（window.ArrRack）。 */
    this.rack = { items: [] };
    /* 钢琴窗音源选择（arrangement.pianoRoll）：跨会话保持 */
    this.pianoRollSource = { itemId: "", presetId: "" };
    /* 混音台状态（P4；P2 起登记 gain/pan/mute/solo，AI 经 ops 也能改） */
    this.mixer = { channels: {} };
    /* 服务端修订号：SSE arrangement 事件与保存回执带回，判断新鲜度 */
    this.rev = 0;
    this.projectEpoch = 0;
    this._saves = Object.create(null);
    this.saveConflict = null;
    this.selectedTrackIdx = 0;
    this.selectedClips = [];
    this.clipboard = null;

    this.midiFiles = [];
    this.dirs = [];               // 已注册素材根目录（settings.material_dirs 镜像）

    this.playheadBeat = 0;
    this.playbackOriginBeat = 0;  // 本次播放的起始位置（暂停回退目标）
    this.isPlaying = false;

    this.engine = new window.ArrangeEngine();
    this.engine.controller = this;
    var self = this;
    this.engine.getTracks = function () { return self.tracks; };
    this.engine.onSoundFontLoaded = function () { self.showHUD("✓ 轨道音源已加载"); };
    this.engine.onSoundFontError = function (tid     , err     ) {
      UI.toast ("✗ 轨道音源加载失败: " + (err && err.message ? err.message : "未知错误"), "err");
    };
    /* 引擎模式播放中引擎掉线（崩溃/重启）：自动停止走带 + 清引擎残留音 +
       明确提示（严格路由，不无声空转） */
    this.engine.onEngineLost = function () {
      if (self.isPlaying) {
        self.stopEngineClock();
        try { if (window.EngineBridge && window.EngineBridge.panic) window.EngineBridge.panic().catch(function(){}); } catch (e) {}
        UI.toast ("⚠ 音频引擎已中断，播放已停止（引擎恢复后可重新播放）", "warn");
      }
    };

    this.undoStack = [];
    this.redoStack = [];

    this.saveTimer = null;
    this.dirty = false;
    this.dragState = null;
    this.rulerDrag = null;
    this.menuEl = null;
    this.menuOutsideHandler = null;
    this.hudTimer = null;
    this.rafId = null;
    this.frameCount = 0;
    this.meterEls = {};
    this.thumbObserver = null;
    this.hoverDropEl = null;
  }

  /* ═══════════ 初始化与 DOM 绑定 ═══════════ */

  Arrange.prototype.init = function (                       ) {
    if (this.initialized) return;
    this.initialized = true;

    var ids = ["arrStage", "chatFoldPane", "arrangeToggleBtn",
      "arrHomeBtn", "arrPlayBtn", "arrPlayIconPath", "arrPlayLabel", "arrStopBtn", "arrLoopBtn", "arrMetroBtn", "arrResumeBtn",
      "arrBpmInput", "arrPosDisplay", "arrSnapDropdown", "arrSnapBtn", "arrSnapMenu",
      "arrZoomOutBtn", "arrZoomRange", "arrZoomInBtn", "arrUndoBtn", "arrRedoBtn",
      "arrSaveStamp", "arrRackBtn", "arrHudBadge",
      "arrMidiCount", "arrMidiList",
      "arrTracksScroll", "arrInner", "arrRulerCanvas", "arrLanes", "arrAddTrackRow", "arrAddTrackBtn", "arrPlayline",
      "matBody", "matTabs", "matSearch", "arrAddDirBtn",
      "mxrBtn", "mxrPanel", "mxrLanes", "mxrMaster", "mxrCloseBtn", "mxrCount"];
    for (var i = 0; i < ids.length; i++) {
      this.el[ids[i]] = document.getElementById(ids[i]);
    }
    this.el.rulerSticky = this.el.arrRulerCanvas ? this.el.arrRulerCanvas.parentElement : null;
    this.rulerCtx = this.el.arrRulerCanvas ? this.el.arrRulerCanvas.getContext("2d") : null;

    // 编排窗口常驻 DOM，用 class 控制显隐（去掉初始 hidden）
    var ws = document.getElementById("arrangeWorkspace");
    if (ws) ws.hidden = false;

    this.bindToggle();
    this.bindTransport();
    this.bindSnapDropdown();
    this.bindTracksArea();
    this.bindPanels();
    this.bindModals();
    this.bindOutputRack();
    this.bindKeyboard();
    this.bindFocusManager();

    var self = this;
    window.addEventListener("resize", function () {
      if (self.isOpen) { self.resizeRulerCanvas(); self.renderRuler(); }
    });

    /* 走带偏好异步载入后点亮「暂停回起点」按钮（含后续变化） */
    if (UI.onTransportPrefs) {
      UI.onTransportPrefs(function (p) { self.syncResumeUI(p); });
    }
    if (this.el.arrTracksScroll) {
      this.el.arrTracksScroll.addEventListener("scroll", function () {
        if (self.isOpen) self.renderRuler();
      }, { passive: true });
    }

    // 片段缩略图：进入视口才绘制（性能保障措施）
    if (typeof IntersectionObserver !== "undefined" && this.el.arrTracksScroll) {
      this.thumbObserver = new IntersectionObserver(function (entries) {
        entries.forEach(function (en) {
          if (en.isIntersecting) self.drawThumbFor(en.target);
        });
      }, { root: this.el.arrTracksScroll, rootMargin: "120px" });
    }

    this.loadMaterialDirs();
  };

  Arrange.prototype.$ = function (id     ) { return this.el[id] || document.getElementById(id); };

  /* ═══════════ 折叠展开 ═══════════ */

  Arrange.prototype.bindToggle = function (                       ) {
    var self = this;
    if (this.el.arrangeToggleBtn) {
      this.el.arrangeToggleBtn.addEventListener("click", function () {
        self.toggle();
      });
    }
  };

  Arrange.prototype.toggle = function (                       ) {
    if (this.isOpen) this.close(); else this.open();
  };

  Arrange.prototype.open = function (                       ) {
    if (this.isOpen) return;
    this.isOpen = true;
    var ws = document.getElementById("arrangeWorkspace");
    if (ws) {
      ws.classList.remove("closing");
      ws.classList.add("open");
    }

    if (this.el.chatFoldPane) this.el.chatFoldPane.classList.add("folded");
    if (this.el.arrangeToggleBtn) {
      this.el.arrangeToggleBtn.classList.add("active");
      this.el.arrangeToggleBtn.textContent = "▦ 返回对话";
    }

    this.engine.resume();
    var self = this;
    /* 早期测试版提示：每次启动首次打开编曲窗时弹出（点「知道了」后本会话不再弹） */
    var betaOverlay = document.getElementById("arrBetaModal");
    if (betaOverlay && betaOverlay.hidden) {
      try {
        if (sessionStorage.getItem("melodymate_arr_beta_v1") !== "1") {
          setTimeout(function () {
            if (self.isOpen) self.showOverlay(betaOverlay);
          }, 500);
        }
      } catch (e) { /* sessionStorage 不可用时每次打开都提示 */ }
    }
    // 等折叠动画落定再测量画布（380~520ms 大型窗口动效区间）
    setTimeout(function () {
      self.resizeRulerCanvas();
      self.renderAll();
      self.setFocus(true);
    }, 480);
    this.startUILoop();
  };

  Arrange.prototype.close = function (                       ) {
    if (!this.isOpen) return;
    this.doSave();   // 关窗即冲刷防抖窗口内的编辑（此前 <800ms 内的改动静默丢失）
    this.isOpen = false;
    this.stopPlayback();
    this.setFocus(false);
    this.closeMenu();

    var ws = document.getElementById("arrangeWorkspace");
    if (ws) {
      ws.classList.remove("open");
      ws.classList.add("closing");
    }
    if (this.el.chatFoldPane) this.el.chatFoldPane.classList.remove("folded");
    if (this.el.arrangeToggleBtn) {
      this.el.arrangeToggleBtn.classList.remove("active");
      this.el.arrangeToggleBtn.textContent = "▦ 编曲窗";
    }
    this.stopUILoop();
  };

  /* ═══════════ 项目接入与持久化 ═══════════ */

  /** chat.js 打开/切换项目时调用；projectId 为 null 表示返回档案库 */
  Arrange.prototype.setProject = function (                         projectId     ) {
    if (this.projectId === projectId) return;
    this.doSave();   // 切走前冲刷：此后 projectId 被清空，挂起的保存定时器会静默空转
    clearTimeout(this.saveTimer);
    window.ArrangementSync.cancelConflict();
    this._conflictDialog=false;
    this.saveInFlight=false;
    this.stopPlayback();
    this.undoStack = [];
    this.redoStack = [];
    this.selectedClips = [];
    this.selectedTrackIdx = 0;
    this.playheadBeat = 0;
    this.projectId = projectId || null;
    var epoch = ++this.projectEpoch;
    this.audioSync = "waiting";
    var requestedProject = this.projectId;
    this.saveConflict = null;
    this.loadedProjectId = null;

    if (!this.projectId) {
      this.tracks = [];
      if(window.PluginPanel)window.PluginPanel.restoreAll(this);
      if (this.el.arrLanes) this.el.arrLanes.innerHTML = "";
      if (this.isOpen) this.close();
      return;
    }

    var self = this;
    /* 全局 BPM 与编排数据并行加载：全局 BPM 是唯一事实，
       覆盖编排快照里的本地 bpm（旧版数据迁移兼容） */
    var bpmReq = UI.getJSON("/api/projects/" + encodeURIComponent(this.projectId) + "/bpm")
      .catch(function () { return null; });
    var record = this._saves[requestedProject ] || (this._saves[requestedProject ] = { base: null, inflight: null, draft: null });
    Promise.resolve(record.inflight).then(function () { return Promise.all([
      UI.getJSON("/api/projects/" + encodeURIComponent(requestedProject ) + "/arrangement"),
      bpmReq
    ]); }).then(function (results) {
        if (self.projectEpoch !== epoch || self.projectId !== requestedProject) return;
        var data      = results[0];
        var bpmData      = results[1];
        if (data && data.tracks) {
          self.deserialize(data);
        } else {
          self.defaultState();
        }
        if (bpmData && bpmData.bpm) {
          self.bpm = clamp(Number(bpmData.bpm) || 120, 30, 300);
          self.engine.bpm = self.bpm;
        }
        self.loadedProjectId = self.projectId;
        self.dirty = false;
        record.base = window.ArrangementSync.copy(data && data.tracks ? data : self.serialize());
        record.base.rev = self.rev;
        self.setSaveStamp("SAVED", false);
        /* v1 老工程（或音源引用被修复）在 deserialize 里已迁移成 v2：
           立即回写，否则磁盘上仍是 v1——下次 AI 定向修改又要重迁一遍，
           而且界面显示的音源与文件内容长期不一致。放在 dirty=false 之后，
           避免刚置的脏标记被这里清掉。 */
        if (self._needsMigrationSave || !data || !data.tracks) {
          self._needsMigrationSave = false;
          self.scheduleSave();
        }
        self.syncTransportUI();
        if (self.isOpen) self.renderAll();
        /* P5 §4：把工程里记录的插件装回引擎并恢复状态（FX 槽 + 插件乐器）。
           浏览器模式没有原生引擎，restoreAll 内部逐个失败也不抛（静默跳过）。 */
        if (window.PluginPanel) window.PluginPanel.restoreAll(self);
        return window.ArrangementSync.draft("get", requestedProject ).then(function (draft     ) {
          if (self.projectEpoch !== epoch || self.projectId !== requestedProject) return;
          draft = record.draft || draft;
          if (!draft || window.ArrangementSync.equal(draft.local, record.base)) return;
          var merged = window.ArrangementSync.merge(draft.base, draft.local, record.base);
          self._applyMergedState(merged.value);
          if (merged.conflicts.length) self._pauseForConflict(draft.base, draft.local, record.base, merged.conflicts);
          else self.scheduleSave();
        }).catch(function () {});
      })
      .catch(function (e) {
        if (self.projectEpoch !== epoch || self.projectId !== requestedProject) return;
        /* 读取失败不再静默回退空白（用户会以为编排数据丢了） */
        if (UI.toast) UI.toast ("⚠ 编排数据读取失败，已载入空白编排: " + ((e && e.message) || ""), "err");
        self.defaultState();
        self.loadedProjectId = null;
        if (self.isOpen) self.renderAll();
      });
  };

  /** chat.js 文件清单变化时同步左栏 MIDI 列表 */
  Arrange.prototype.refreshMidiList = function (                         files     ) {
    this.midiFiles = files || [];
    this.renderMidiList();
  };

  Arrange.prototype.defaultState = function (                       ) {
    this.bpm = 120;
    this.loop = { on: false, start: 0, end: 16 };
    this.rack = { items: [] };
    this.mixer = { channels: {} };
    this.rev = 0;
    /* 默认机架：一个内置合成器条目（轨道音源引用它，机架不为空——
       "轨道音源只能来自机架"这条规则从空工程起就成立） */
    var synth = window.ArrRack.synthItem("sawtooth", "合成器 · 锯齿波");
    window.ArrRack.upsert(this.rack.items, synth);
    var track = this.makeTrack(1);
    track.source = { itemId: synth.id, presetId: "sawtooth" };
    this.tracks = [track];
    this.pianoRollSource = { itemId: synth.id, presetId: "sawtooth" };
    window.ArrRack.setItems(this.rack.items);
    this.syncTransportUI();
  };

  Arrange.prototype.makeTrack = function (                         index     ) {
    return {
      id: uid("tr"),
      name: "Track " + index,
      color: TRACK_COLORS[(index - 1) % TRACK_COLORS.length],
      mute: false,
      solo: false,
      volume: 0.8,
      /* v2：机架条目引用（由调用方填入；空引用会被 deserialize 兜底修正） */
      source: { itemId: "", presetId: "" },
      clips: []
    };
  };

  /** 干净的持久化结构：剥离运行时字段（_rev/_peaks 等），供保存与撤销快照共用 */
  Arrange.prototype.cleanState = function (                       ) {
    return {
      version: 2,
      rev: this.rev || 0,
      bpm: this.bpm,
      snap: this.snap,
      ppb: this.ppb,
      loop: { on: !!this.loop.on, start: this.loop.start, end: this.loop.end },
      tracks: this.tracks.map(function (t) {
        return {
          id: t.id,
          name: t.name,
          color: t.color,
          mute: !!t.mute,
          solo: !!t.solo,
          volume: t.volume,
          source: t.source,
          clips: (t.clips || []).map(function (c) {
            var clip      = {
              id: c.id,
              type: c.type,
              name: c.name,
              start: c.start,
              length: c.length,
              mute: !!c.mute,
              fadeIn: c.fadeIn || 0,
              fadeOut: c.fadeOut || 0,
              gain: c.gain !== undefined ? c.gain : 1
            };
            if (c.type === "midi") {
              clip.fullName = c.fullName || "";
              clip.notes = c.notes || [];
            } else {
              clip.src = c.src;
              clip.offset = c.offset || 0;
            }
            return clip;
          })
        };
      }),
      /* 机架只持久化真实条目：派生采样（工程内已用采样）每次渲染时重算 */
      rack: { items: (this.rack && this.rack.items ? this.rack.items : []).map(function (it     ) {
        return window.ArrRack.cloneItem(it);
      }) },
      pianoRoll: {
        itemId: (this.pianoRollSource && this.pianoRollSource.itemId) || "",
        presetId: (this.pianoRollSource && this.pianoRollSource.presetId) || ""
      },
      mixer: this.mixer || { channels: {} }
    };
  };

  Arrange.prototype.serialize = function (                       ) {
    return this.cleanState();
  };

  Arrange.prototype.deserialize = function (                         data     ) {
    var ArrRack = window.ArrRack;
    var self = this;
    this.bpm = clamp(Number(data.bpm) || 120, 30, 300);
    this.snap = Number(data.snap) >= 0 ? Number(data.snap) : 0.25;
    this.ppb = clamp(Number(data.ppb) || 26, 8, 80);
    this.loop = {
      on: !!(data.loop && data.loop.on),
      start: Number(data.loop && data.loop.start) || 0,
      end: Number(data.loop && data.loop.end) || 16
    };

    /* ── v1 → v2 迁移 ──
       v1 的轨道音源自带 {type,wave,tone,libId,presetId}，机架由前端硬编码
       枚举；v2 把音源收进项目机架（rack.items），轨道只留 {itemId,presetId}
       （见《架构升级实施计划》3.2.2）。这里按轨道旧音源造机架条目并把轨道
       指过去，随后正常保存即写成 v2。服务端
       internal/project/arrangement_ops.go 的 MigrateArrangementV2 是同一套
       规则的服务端副本——AI 在工程未被前端打开时也要能读改 v1 老工程。 */
    var isV2 = Number(data.version) >= 2;
    /* 本次载入是否发生了迁移/修复：由 setProject 决定是否立即回写磁盘 */
    var migratedAny = !isV2;
    this.rev = Number(data.rev) || 0;
    this.rack = { items: [] };
    if (isV2 && data.rack && Array.isArray(data.rack.items)) {
      data.rack.items.forEach(function (it     ) {
        if (it && it.id) self.rack.items.push(ArrRack.cloneItem(it));
      });
    }
    this.mixer = (isV2 && data.mixer && typeof data.mixer === "object" && data.mixer.channels)
      ? { channels: data.mixer.channels, master: data.mixer.master }
      : { channels: {} };

    this.tracks = (Array.isArray(data.tracks) ? data.tracks : []).map(function (t     , i     ) {
      var track = self.makeTrack(i + 1);
      if (t.id) track.id = String(t.id);
      if (t.name) track.name = t.name;
      if (t.color) track.color = t.color;
      track.mute = !!t.mute;
      track.solo = !!t.solo;
      track.volume = clamp(t.volume !== undefined ? Number(t.volume) : 0.8, 0, 1);
      if (t.source && typeof t.source === "object") {
        var src = t.source;
        if (!src.itemId) {
          // v1 音源：迁移出一条机架条目，轨道改为引用它
          var migrated = ArrRack.migrateV1Source(src);
          migratedAny = true;
          if (migrated) {
            ArrRack.upsert(self.rack.items, migrated);
            src = { itemId: migrated.id, presetId: migrated.presetId || "" };
          } else {
            src = { itemId: "", presetId: "" };
          }
        }
        track.source = {
          itemId: String(src.itemId || ""),
          presetId: (src.presetId === undefined || src.presetId === null) ? "" : String(src.presetId)
        };
      }
      track.clips = (Array.isArray(t.clips) ? t.clips : []).map(function (c     ) {
        var clip      = {
          id: c.id || uid("clip"),
          type: c.type === "audio" ? "audio" : "midi",
          name: c.name || "Clip",
          start: Math.max(0, Number(c.start) || 0),
          length: Math.max(MIN_CLIP_LEN, Number(c.length) || BAR_BEATS),
          mute: !!c.mute,
          fadeIn: Math.max(0, Number(c.fadeIn) || 0),
          fadeOut: Math.max(0, Number(c.fadeOut) || 0),
          _rev: 0
        };
        if (clip.type === "midi") {
          clip.fullName = c.fullName || c.name || "";
          clip.notes = Array.isArray(c.notes) ? c.notes : [];
        } else {
          clip.src = c.src || null;
          clip.offset = Math.max(0, Number(c.offset) || 0);
          clip.gain = clamp(Number(c.gain !== undefined ? c.gain : 1), 0, 4);
        }
        return clip;
      });
      return track;
    });
    // 引擎模式：29-31 为试听/钢琴窗专用轨，加载时截断（webaudio 模式 32 轨）
    var engMode2 = window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine();
    var cutLimit = engMode2 ? 29 : 32;
    if (this.tracks.length > cutLimit) {
      this.tracks = this.tracks.slice(0, cutLimit);
      this.showHUD(engMode2
        ? "⚠ 工程超过 29 条轨道（引擎轨位上限，29-31 为试听/钢琴窗专用），已保留前 29 条"
        : "⚠ 工程超过 32 条轨道，已保留前 32 条");
    }
    if (!this.tracks.length) this.tracks = [this.makeTrack(1)];

    /* 机架与音源引用的一致性兜底：机架不能为空；轨道不能指向不存在的条目
       （AI 移出条目、手改 JSON、老工程丢了音色库都会走到这里）。 */
    if (this.normalizeRack()) migratedAny = true;
    this._needsMigrationSave = migratedAny;

    // 钢琴窗音源：v1 无持久化字段，指向首个机架条目
    var pr = (data.pianoRoll && typeof data.pianoRoll === "object") ? data.pianoRoll : null;
    var prItemId = pr && pr.itemId ? String(pr.itemId) : "";
    if (!ArrRack.find(this.rack.items, prItemId)) {
      var firstItem = this.rack.items[0];
      this.pianoRollSource = {
        itemId: firstItem ? firstItem.id : "",
        presetId: firstItem ? (firstItem.presetId || "") : ""
      };
    } else {
      this.pianoRollSource = {
        itemId: prItemId,
        presetId: pr && pr.presetId ? String(pr.presetId) : ""
      };
    }

    this._pruneTrackNodes();
    this.engine.bpm = this.bpm;
    this.engine.loop = this.loop;   // 播放中载入新数据也保持循环边界同步
    /* 混音下发：引擎模式下唯一入口是 applyMixSafe→setTrackMix，此前
       deserialize 完全不下发——引擎按默认 gain=1.0/mute=false 播放，
       与界面上的轨道音量/静音/独奏显示不符（含收缩尾槽，见 applyMixSafe） */
    this.applyMixSafe();
    this.syncTransportUI();
    this.notifyRackChanged();
  };

  /** normalizeRack 保证"机架非空 + 音源引用有效"：
      空机架补内置合成器；轨道/钢琴窗指向的条目不存在时回落到首个条目。
      返回是否做过修复（调用方据此决定要不要回写磁盘）。 */
  Arrange.prototype.normalizeRack = function (                       ) {
    var ArrRack = window.ArrRack;
    var repaired = false;
    if (!this.rack || !this.rack.items) { this.rack = { items: [] }; repaired = true; }
    if (!this.rack.items.length) {
      ArrRack.upsert(this.rack.items, ArrRack.synthItem("sawtooth", "合成器 · 锯齿波"));
      repaired = true;
    }
    var fallback = this.rack.items[0];
    var self = this;
    this.tracks.forEach(function (t     ) {
      var src = t.source || {};
      if (!ArrRack.find(self.rack.items, src.itemId)) {
        t.source = { itemId: fallback.id, presetId: fallback.presetId || "" };
        repaired = true;
      }
    });
    var pr = this.pianoRollSource || { itemId: "", presetId: "" };
    if (!ArrRack.find(this.rack.items, pr.itemId)) {
      this.pianoRollSource = { itemId: fallback.id, presetId: fallback.presetId || "" };
      repaired = true;
    }
    if (!this.mixer || !this.mixer.channels) { this.mixer = { channels: {} }; repaired = true; }
    return repaired;
  };

  /** notifyRackChanged 机架变化后同步镜像（audio_engine 按 track.source 解析
      音源时读它）、重绘机架窗口、联动钢琴窗音色显示。 */
  Arrange.prototype.notifyRackChanged = function (                       ) {
    window.ArrRack.setItems(this.rack.items);
    var win = document.getElementById("arrRackWindow");
    if (win && !win.hidden) window.ArrRack.render(this);
    /* 选择器开着就地重绘：入架成功后条目要从"其他音源"挪到"已在架" */
    window.ArrRack.refreshPicker();
    if (window.PianoRoll && window.PianoRoll.syncRackSource) {
      try { window.PianoRoll.syncRackSource(); } catch (e) {}
    }
  };

  /** 项目文件清单更新后同步清理：被删除（用户删除或 AI 调 delete_midi）
      的 MIDI 文件，编曲轨道上对应的 midi clip 一并移除，避免悬垂引用
      （clip.notes 还在内存里，播放/导出会继续用它，但源文件已不存在） */
  Arrange.prototype.syncFiles = function (                         files     ) {
    var valid                         = {};
    (files || []).forEach(function (f     ) {
      if (f && f.name) valid[f.name] = true;
    });
    var changed = false;
    this.tracks.forEach(function (t) {
      if (!t.clips || !t.clips.length) return;
      var kept = t.clips.filter(function (c) {
        if (!c || c.type !== "midi") return true;
        if (valid[c.fullName ] || valid[c.name]) return true;
        changed = true;
        return false;
      });
      if (kept.length !== t.clips.length) t.clips = kept;
    });
    if (changed) {
      this.renderTracks();
      this.updateContentWidth();
      this.scheduleSave();
    }
  };

  Arrange.prototype.scheduleSave = function (                       ) {
    if(window.PluginPanel && this.loadedProjectId===this.projectId)window.PluginPanel.restoreAll(this);
    if (!this.projectId) return;
    this.dirty = true;
    this.setSaveStamp("EDIT…", true);
    if (this.saveConflict) { this._stashDraft(); this.setSaveStamp("保存冲突 · 点击处理", true); return; }
    var self = this;
    clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(function () { self.doSave(); }, 800);
  };

  Arrange.prototype.doSave = function (                       ) {
    var self = this;
    if (!this.projectId) return Promise.resolve();
    var project = this.projectId, epoch = this.projectEpoch;
    var record = this._saves[project] || (this._saves[project] = { base: window.ArrangementSync.copy(this.serialize()), inflight: null, draft: null });
    if (record.inflight) { if(this.dirty)this._stashDraft(); return record.inflight; }
    if (!this.dirty || this.saveConflict || this.loadedProjectId !== project) return Promise.resolve();
    var snapshot = window.ArrangementSync.copy(this.serialize());
    snapshot.rev = Number(record.base && record.base.rev) || 0;
    var active = function () { return self.projectId === project && self.projectEpoch === epoch; };
    this.dirty = false;
    this.saveInFlight = true;
    this.setSaveStamp("SAVING", false);
    var base = window.ArrangementSync.copy(record.base);
    record.lastError=null;
    record.inflight = this._stashDraft().then(function () { return UI.putJSON     ("/api/projects/" + encodeURIComponent(project) + "/arrangement", snapshot); })
      .then(function (result     ) {
        snapshot.rev = result.rev;
        if (!record.base || result.rev >= record.base.rev) record.base = snapshot;
        if (active()) { self.rev = record.base.rev; self.setSaveStamp(self.dirty ? "EDIT…" : "SAVED", !!self.dirty); }
        if (window.PluginPanel && window.PluginPanel.saveAll) {
          window.PluginPanel.saveAll(self);
        }
        if (!record.draft || window.ArrangementSync.equal(record.draft.local, Object.assign({}, snapshot, {rev: record.draft.local.rev}))) {
          record.draft = null;
          return window.ArrangementSync.draft("delete", project).catch(function () {});
        }
      })
      .catch(function (e     ) {
        record.lastError=e;
        if (e.status === 409 && e.data && e.data.arrangement) {
          var remote = record.base && record.base.rev > e.data.arrangement.rev ? record.base : e.data.arrangement;
          var local = active() ? self.serialize() : (record.draft ? record.draft.local : snapshot);
          var merged = window.ArrangementSync.merge(base, local, remote);
          record.base = remote;
          record.lastError=null;
          record.draft = {base:base,local:local,remote:remote,conflicts:merged.conflicts};
          if(active()) {
            self._applyMergedState(merged.value);
            self.dirty = true;
            if(merged.conflicts.length) self._pauseForConflict(base, local, remote, merged.conflicts);
          }
          return window.ArrangementSync.draft("put", project, record.draft).catch(function(){ if(active())UI.toast ("恢复草稿保存失败，请保持窗口打开", "err"); });
        }
        if (active()) { self.dirty = true; self.setSaveStamp("SAVE ERR", true); if (UI.toast) UI.toast ("✗ 编排保存失败: " + e.message, "err"); }
      }).then(function () {
        record.inflight = null;
        if (active()) { self.saveInFlight = false; if(self.dirty && !self.saveConflict)self.scheduleSave(); }
      });
    return record.inflight;
  };

  Arrange.prototype._stashDraft = function (                       ) {
    if (!this.projectId) return Promise.resolve();
    var record=this._saves[this.projectId];
    if (!record) return Promise.resolve();
    record.draft={base:this.saveConflict?this.saveConflict.base:record.base,local:window.ArrangementSync.copy(this.serialize()),remote:this.saveConflict && this.saveConflict.remote,conflicts:this.saveConflict && this.saveConflict.conflicts};
    return window.ArrangementSync.draft("put",this.projectId,record.draft).catch(function(){if(UI.toast)UI.toast ("恢复草稿保存失败，请保持窗口打开", "err");});
  };

  Arrange.prototype._applyMergedState = function (                         data     ) {
    var previous      = Object.create(null);
    this.tracks.forEach(function(t){previous[t.id]=t;});
    this.deserialize(data);
    this.tracks=this.tracks.map(function(t){
      var old=previous[t.id]; if(!old)return t;
      var clips     =Object.create(null); old.clips.forEach(function(c     ){clips[c.id]=c;});
      t.clips=t.clips.map(function(c     ){var prior=clips[c.id];if(!prior)return c;Object.assign(prior,c);prior._rev=(prior._rev||0)+1;return prior;});
      return Object.assign(old,t);
    });
    if(this.isOpen)this.renderAll();
    if(window.ArrMixer)window.ArrMixer.syncTracks(this);
    if(window.PluginPanel)window.PluginPanel.restoreAll(this);
  };

  Arrange.prototype._pauseForConflict = function (                         base     , local     , remote     , conflicts          ) {
    this.saveConflict={base:base,local:local,remote:remote,conflicts:conflicts};
    this.dirty=true; clearTimeout(this.saveTimer); this._stashDraft();
    this.setSaveStamp("保存冲突 · 点击处理",true);
    var self=this;
    if(this.el.arrSaveStamp) { this.el.arrSaveStamp.tabIndex=0; this.el.arrSaveStamp.onclick=function(){self.resolveSaveConflict();}; this.el.arrSaveStamp.onkeydown=function(e               ){if(e.key==="Enter"||e.key===" "){e.preventDefault();self.resolveSaveConflict();}}; }
    this.resolveSaveConflict();
  };

  Arrange.prototype.resolveSaveConflict = function (                       ) {
    if(!this.saveConflict || this._conflictDialog)return;
    var self=this, epoch=this.projectEpoch, conflict=this.saveConflict;
    this._conflictDialog=true;
    window.ArrangementSync.chooseConflict(conflict.conflicts).then(function(choice     ){
      self._conflictDialog=false;
      if(epoch!==self.projectEpoch || !choice || !self.saveConflict)return;
      var current=self._saves[self.projectId ].base;
      var merged=window.ArrangementSync.merge(conflict.base,self.serialize(),current);
      self._applyMergedState(choice==="local" ? merged.value : window.ArrangementSync.merge(conflict.base,current,self.serialize()).value);
      self.saveConflict=null; self.rev=current.rev; self.scheduleSave();
    });
  };

  Arrange.prototype.setSaveStamp = function (                         text     , warn     ) {
    var el = this.el.arrSaveStamp;
    if (!el) return;
    el.textContent = text;
    el.className = warn ? "stamp danger" : "stamp ok";
  };

  /* ═══════════ 撤销 / 重做 ═══════════ */

  Arrange.prototype.snapshot = function (                       ) {
    return JSON.stringify(this.cleanState());
  };

  /** 清理已不存在轨道的发声链（整体替换 tracks 的路径：载入/撤销/重做） */
  Arrange.prototype._pruneTrackNodes = function (                       ) {
    var live                         = {};
    this.tracks.forEach(function (t) { live[t.id] = true; });
    for (var id in this.engine.trackNodes) {
      if (!live[id]) {
        this.engine.stopTrackSources(id);
        var nodes = this.engine.trackNodes[id];
        if (nodes.synth) nodes.synth.stopAll();
        if (nodes.soundfont) nodes.soundfont.stopAll();
        try { nodes.gain .disconnect(); nodes.analyser .disconnect(); } catch (e) {}
        delete this.engine.trackNodes[id];
      }
    }
  };

  Arrange.prototype.restoreSnapshot = function (                         json     ) {
    var s = JSON.parse(json);
    this.bpm = s.bpm || 120;
    this.loop = s.loop || { on: false, start: 0, end: 16 };
    this.tracks = s.tracks || [];
    /* 机架/钢琴窗/混音台同属工程状态：撤销一次"移出机架"必须把它们一起
       回退，否则条目回来了而轨道仍指向已删 id（无声轨）。 */
    if (s.rack && Array.isArray(s.rack.items)) {
      this.rack = { items: s.rack.items.map(function (it     ) { return window.ArrRack.cloneItem(it); }) };
    }
    if (s.pianoRoll && typeof s.pianoRoll === "object") {
      this.pianoRollSource = {
        itemId: String(s.pianoRoll.itemId || ""),
        presetId: String(s.pianoRoll.presetId || "")
      };
    }
    if (s.mixer && typeof s.mixer === "object") this.mixer = s.mixer;
    // 引擎/导出 32 轨上限：撤销/重做恢复超限快照时截断
    var engMode3 = window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine();
    var cutLimit3 = engMode3 ? 29 : 32;
    if (this.tracks.length > cutLimit3) {
      this.tracks = this.tracks.slice(0, cutLimit3);
      this.showHUD(engMode3
        ? "⚠ 快照超过 29 条轨道（引擎轨位上限），已保留前 29 条"
        : "⚠ 快照超过 32 条轨道，已保留前 32 条");
    }
    if (!this.tracks.length) this.tracks = [this.makeTrack(1)];
    this.normalizeRack();
    this._pruneTrackNodes();
    /* 撤销/重做后混音与调度表都要回推引擎：applyMixSafe 一并完成
       Web 混音、原生 setTrackMix（含收缩尾槽）与采样表重排——
       此前只调 web-only 的 engine.applyMix，引擎侧保留被撤销前的旧
       gain/mute/solo（UI 显示未静音但仍在响 / 或静音后不放声） */
    this.applyMixSafe();
    this.engine.bpm = this.bpm;
    this.engine.loop = this.loop;   // 撤销/重做同步循环边界（此前播放中撤销循环改动仍按旧边界跑）
    this.syncTransportUI();
    this.renderAll();
    this.notifyRackChanged();
  };

  Arrange.prototype.pushHistory = function (                       ) {
    this.undoStack.push(this.snapshot());
    if (this.undoStack.length > 50) this.undoStack.shift();
    this.redoStack = [];
  };

  Arrange.prototype.undo = function (                       ) {
    if (!this.undoStack.length) { this.showHUD("没有更多可撤销的操作"); return; }
    this.redoStack.push(this.snapshot());
    this.restoreSnapshot(this.undoStack.pop());
    this.showHUD("↩ 撤销 (Ctrl+Z)");
    this.scheduleSave();
  };

  Arrange.prototype.redo = function (                       ) {
    if (!this.redoStack.length) { this.showHUD("没有更多可重做的操作"); return; }
    this.undoStack.push(this.snapshot());
    this.restoreSnapshot(this.redoStack.pop());
    this.showHUD("↪ 重做 (Ctrl+Y)");
    this.scheduleSave();
  };

  /* ═══════════ 走带控制 ═══════════ */

  Arrange.prototype.secondsPerBeat = function (                       ) {
    return 60 / (this.bpm || 120);
  };

  Arrange.prototype.bindTransport = function (                       ) {
    var self = this;
    var on = function (           id     , fn     ) {
      var b = this.$(id);
      if (b) b.addEventListener("click", function () { fn(); b.blur(); });
    }.bind(this);

    on("arrHomeBtn", function () { self.seekTo(0, true); self.showHUD("⏮ 回到开头 (Home)"); });
    on("arrPlayBtn", function () { self.togglePlay(); });
    on("arrStopBtn", function () { self.stopPlayback(true); });
    on("arrLoopBtn", function () { self.toggleLoop(); });
    on("arrMetroBtn", function () { self.toggleMetro(); });
    on("arrResumeBtn", function () { self.toggleResumeOnPause(); });
    on("arrUndoBtn", function () { self.undo(); });
    on("arrRedoBtn", function () { self.redo(); });
    on("arrExportWavBtn", function () { self.exportWav(); });
    on("arrZoomInBtn", function () { self.zoomStep(1); });
    on("arrZoomOutBtn", function () { self.zoomStep(-1); });

    if (this.el.arrBpmInput) {
      this.el.arrBpmInput.addEventListener("change", function (         ) {
        self.setBpm(Number(this.value) || 120);
      });
    }
    if (this.el.arrZoomRange) {
      this.el.arrZoomRange.addEventListener("input", function (         ) {
        self.setPpb(Number(this.value));
      });
    }

    // 总音量滑块：接编曲引擎 masterGain（此前 0.9 硬编码，每轨有音量
    // 滑块但没有总线输出控制，音量不合适只能调系统音量）
    var volRange = document.getElementById("arrVolRange")                           ;
    if (volRange) {
      /* 0 是合法音量：`parseInt(x) || 90` 会把 0 读成 90（拉到静音后
         重启音量自己跳回 90%），按 NaN 判定 */
      var savedVol = 90;
      try {
        var raw = localStorage.getItem("arrMasterVolume");
        if (raw !== null) {
          var n = parseInt(raw, 10);
          if (!isNaN(n)) savedVol = n;
        }
      } catch (e) {}
      volRange.value = String(Math.max(0, Math.min(100, savedVol)));
      self.applyMasterVolume(savedVol / 100);
      volRange.addEventListener("input", function () {
        var v = (parseInt(this.value, 10) || 0) / 100;
        self.applyMasterVolume(v);
        try { localStorage.setItem("arrMasterVolume", String(Math.round(v * 100))); } catch (e) {}
      });
    }

    // Ctrl+滚轮缩放 / Shift+滚轮或触摸板横滚：横向滚动时挂起播放头跟随 2s
    if (this.el.arrTracksScroll) {
      this.el.arrTracksScroll.addEventListener("wheel", function (e     ) {
        if (e.ctrlKey) {
          e.preventDefault();
          self.setPpb(self.ppb + (e.deltaY < 0 ? 3 : -3));
        } else if (e.shiftKey || e.deltaX) {
          self._followSuspendUntil = performance.now() + 2000;
        }
      }, { passive: false });
    }
  };

  Arrange.prototype.syncTransportUI = function (                       ) {
    if (this.el.arrBpmInput) this.el.arrBpmInput.value = String(this.bpm);
    if (this.el.arrZoomRange) this.el.arrZoomRange.value = String(this.ppb);
    if (this.el.arrLoopBtn) this.el.arrLoopBtn.classList.toggle("active", !!this.loop.on);
    if (this.el.arrMetroBtn) this.el.arrMetroBtn.classList.toggle("active", !!this.metronome);
    var snapLabel = "关闭吸附";
    for (var i = 0; i < SNAPS.length; i++) {
      if (SNAPS[i].v === this.snap) { snapLabel = SNAPS[i].label; break; }
    }
    var lbl = this.el.arrSnapBtn ? this.el.arrSnapBtn.querySelector(".pr-dropdown-label") : null;
    if (lbl) lbl.textContent = snapLabel;
    this.updatePosDisplay();
  };

  Arrange.prototype.togglePlay = function (                       ) {
    if (this.isPlaying) this.pausePlayback();
    else this.startPlayback();
  };

  Arrange.prototype.startPlayback = function (                       ) {
    if(window.AudioBackend && window.AudioBackend.isEngine() && this.audioSync!=="ready"){
      UI.toast (this.audioSync==="failed"?"工程音频恢复失败，请检查插件后重试":"工程音频正在同步，请稍后播放",this.audioSync==="failed"?"err":"warn");return;
    }
    /* 引擎模式播放门控（严格路由）：引擎未就绪不播放、不静默回退 */
    if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()
        && !window.AudioBackend.isEngineReady()) {
      /* 门控提示双通道：toast（显眼、不被 HUD 顶掉）+ HUD 状态条 */
      if (window.UI && window.UI.toast) UI.toast ("✗ 音频引擎未就绪，播放不可用（可在设置页切换 WEBAUDIO 模式）", "err");
      this.showHUD("✗ 音频引擎未就绪，播放不可用（可在设置页切换 WEBAUDIO 模式）");
      return;
    }
    if(window.AudioBackend?.isNativePreferred()){
      var pendingSource=this.tracks.find((track)=>{var nodes    =this.engine.ensureTrack(track);return nodes && (nodes.sfLoading || nodes._useNative===false);});
      if(pendingSource){UI.toast ("工程音源正在同步："+(pendingSource.name||pendingSource.id),"warn");return;}
    }
    this.engine.resume().catch(function (e     ) {
      console.warn("[Arrange] AudioContext 恢复失败:", e);
    });
    /* 与钢琴卷帘互斥：两个窗口各自持有独立走带，同时播放会叠加发声
       （引擎模式下轨 0-28 与卷帘轨 30/31 同时响，WEBAUDIO 下共享
       AudioContext 叠加），且两边节拍基准不同 */
    try {
      if (window.PianoRoll && window.PianoRoll.isPlaying && window.PianoRoll.stopPlayback) {
        window.PianoRoll.stopPlayback();
        if (window.UI && window.UI.toast) UI.toast ("钢琴卷帘播放已停止（编曲窗接管）", "warn");
      }
    } catch (e) {}
    this.engine.metronome = this.metronome;
    this.engine.loop = this.loop;
    this.engine.bpm = this.bpm;
    // 记录本次播放的起始位置（「暂停后恢复光标位置」回退目标）
    this.playbackOriginBeat = Math.max(0, this.playheadBeat);
    this.isPlaying = true;
    var self = this;
    var playResult = this.engine.play(this.playheadBeat);
    // 引擎启动失败时回滚播放态（engine.play 内部已 toast 提示）
    if (playResult && typeof playResult.catch === "function") {
      playResult.catch(function () { self.isPlaying = false; self.updatePlayButton(); });
    }
    // 原生优先时：同步 JUCE 走带与素材调度（全走JUCE，尾音自然不截断）
    try {
      if (window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred() && window.EngineBridge) {
        try { window.EngineBridge.setLoop(this.loop.on, this.loop.start, this.loop.end); } catch(e) {}
        try { window.EngineBridge.setTempo(this.bpm); } catch(e) {}
        try { window.EngineBridge.locate(this.playheadBeat); } catch(e) {}
        try { window.EngineBridge.play(); } catch(e) {}
        // 批量调度音频素材（按全局采样率，引擎内重采样+包络，统一尾音）
        this.sendSampleSchedule();
        this._lastSampleSig = this.sampleScheduleSig();
      }
    } catch(e) {}
    this.updatePlayButton();
  };

  /** tempSuspend=true：为拖拽走带等操作临时停摆，不执行「回退到起点」 */
  Arrange.prototype.pausePlayback = function (                         tempSuspend     ) {
    var at = Math.max(0, this.engine.currentBeat());
    this.stopEngineClock();
    /* 「暂停后恢复光标位置」开启时回退到本次播放起点；
       关闭时停在暂停处（原行为） */
    var resumeOnPause = UI.transportPrefs ? UI.transportPrefs().resumeOnPause : false;
    if (!tempSuspend && resumeOnPause && this.playbackOriginBeat !== undefined) {
      at = this.playbackOriginBeat;
      this.showHUD("⏪ 光标已回到本次播放起点");
    }
    this.playheadBeat = Math.max(0, at);
    this.updatePlayline();
    this.updatePosDisplay();
    this.updatePlayButton();
  };

  Arrange.prototype.stopPlayback = function (                         resetHead     ) {
    var wasPlayingAt = this.isPlaying ? Math.max(0, this.engine.currentBeat()) : this.playheadBeat;
    this.stopEngineClock();
    if (resetHead) {
      this.playheadBeat = this.loop.on ? this.loop.start : 0;
    } else {
      this.playheadBeat = wasPlayingAt;
    }
    this.updatePlayline();
    this.updatePosDisplay();
    this.updatePlayButton();
  };

  Arrange.prototype.stopEngineClock = function (                       ) {
    this.engine.stopSchedule();
    this.isPlaying = false;
    if (this._reschedTimer) { clearTimeout(this._reschedTimer); this._reschedTimer = null; }
    try {
      if (window.EngineBridge) {
        try { window.EngineBridge.stop(); } catch(e) {}
        try { window.EngineBridge.clearSamples(); } catch(e) {}
      }
    } catch(e) {}
    this.updatePlayButton();
  };

  Arrange.prototype.updatePlayButton = function (                       ) {
    if (!this.el.arrPlayLabel) return;
    this.el.arrPlayLabel.textContent = this.isPlaying ? "暂停" : "播放";
    if (this.el.arrPlayIconPath) {
      this.el.arrPlayIconPath.setAttribute("d", this.isPlaying
        ? "M5 3.5h2.25v9H5zM8.75 3.5H11v9H8.75z"
        : "m11.596 8.697-6.363 3.692c-.54.313-1.233-.066-1.233-.697V4.308c0-.63.692-1.01 1.233-.696l6.363 3.692a.802.802 0 0 1 0 1.393z");
    }
  };

  Arrange.prototype.toggleLoop = function (                       ) {
    this.loop.on = !this.loop.on;
    if (this.loop.on && this.loop.end <= this.loop.start) {
      this.loop.start = 0;
      this.loop.end = BAR_BEATS * 4;
    }
    this.engine.loop = this.loop;
    try { if (window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred() && window.EngineBridge && window.EngineBridge.setLoop) { try{ window.EngineBridge.setLoop(this.loop.on, this.loop.start, this.loop.end); }catch(e){} } } catch(e){}
    // 循环切换影响调度基准：播放中则从当前拍重锚
    if (this.isPlaying) {
      var b = Math.max(0, this.engine.currentBeat());
      this.playheadBeat = b;
      this.engine.play(b);
      try { if (window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred() && window.EngineBridge) { try{window.EngineBridge.locate(b);}catch(e){} } } catch(e){}
    }
    this.syncTransportUI();
    this.renderRuler();
    this.showHUD(this.loop.on ? "循环选区: 开启 (L)" : "循环选区: 关闭 (L)");
    this.scheduleSave();
  };

  Arrange.prototype.toggleMetro = function (                       ) {
    this.metronome = !this.metronome;
    this.engine.metronome = this.metronome;
    /* 关闭时清掉已展开的 click 队列：触发窗内（≤0.45s）已入队的事件否则
       会继续响，关掉开关还能听到半秒节拍 */
    if (!this.metronome) this.engine.clearPendingClicks && this.engine.clearPendingClicks();
    this.syncTransportUI();
    this.showHUD(this.metronome ? "节拍器: 开启" : "节拍器: 关闭");
  };

  /** 走带条上的「暂停后光标回起点」开关：就地切换并持久化。
      钢琴窗共用同一份 UI.transportPrefs 缓存，两窗口行为即时一致 */
  Arrange.prototype.toggleResumeOnPause = function (                       ) {
    var prefs = UI.transportPrefs ? UI.transportPrefs() : { resumeOnPause: false };
    var next = !prefs.resumeOnPause;
    UI.setTransportPref("resumeOnPause", next);
    this.showHUD(next ? "⏪ 已开启：暂停后光标回到本次播放起点"
                      : "已关闭：暂停后光标停在当前位置");
    var self = this;
    UI.postJSON("/api/transport/prefs", { resume_on_pause: next }).then(function (r     ) {
      if (!r || !r.ok) throw new Error((r && r.message) || "保存失败");
    }).catch(function (err) {
      if (UI.toast) UI.toast ("⚠ 走带偏好保存失败：" + err.message, "warn");
      self._resumeSaveFailed = true;   /* 重开应用时会重新读取旧值 */
    });
  };

  /** 按钮高亮与设置值同步（init 订阅初值/后续变化，切换时也会调用） */
  Arrange.prototype.syncResumeUI = function (                         prefs     ) {
    if (!this.el.arrResumeBtn) return;
    this.el.arrResumeBtn.classList.toggle("active",
      !!(prefs && prefs.resumeOnPause));
  };

  Arrange.prototype.setBpm = function (                         val     ) {
    val = clamp(Math.round(val), 30, 300);
    if (val === this.bpm) return;
    if (this.isPlaying) {
      // 变速后重新锚定：必须先用旧 BPM 采样当前拍位——此前先改
      // engine.bpm 再采样，已过时长被新秒/拍换算，播放头跳位
      var b = Math.max(0, this.engine.currentBeat());
      this.playheadBeat = b;
      this.bpm = val;
      this.engine.bpm = val;
      this.engine.play(b);
      try { if (window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred() && window.EngineBridge) { try{window.EngineBridge.setTempo(val);}catch(e){} try{window.EngineBridge.locate(b);}catch(e){} } } catch(e){}
      // 原生模式：已调度音频剪辑的位置是旧 BPM 烘焙的采样数，setTempo
      // 只重算走带位置不重排调度表——不重建会与 MIDI 轨渐进失同步
      this.rescheduleSamplesDebounced();
    } else {
      this.bpm = val;
      this.engine.bpm = val;
      try { if (window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred() && window.EngineBridge) { try{window.EngineBridge.setTempo(val);}catch(e){} } } catch(e){}
    }
    this.syncTransportUI();
    this.renderAllThumbsSoon();
    this.showHUD("BPM: " + val);
    this.scheduleSave();
    /* 同步项目全局 BPM：AI 生成新 MIDI、保存音符表等所有链路跟随此值 */
    if (this.projectId && window.UI && UI.postJSON) {
      UI.postJSON("/api/projects/" + encodeURIComponent(this.projectId) + "/bpm", { bpm: val })
        .catch(function (e) {
          if (UI.toast) UI.toast ("⚠ 全局 BPM 保存失败: " + ((e && e.message) || ""), "warn");
        });
    }
  };

  Arrange.prototype.seekTo = function (                         beat     , restartIfPlaying     ) {
    this.playheadBeat = Math.max(0, beat);
    this.updatePlayline();
    this.updatePosDisplay();
    if (restartIfPlaying && this.isPlaying) {
      this.engine.play(this.playheadBeat);
      try { if (window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred() && window.EngineBridge) { try{window.EngineBridge.locate(this.playheadBeat);}catch(e){} } } catch(e){}
    }
  };

  Arrange.prototype.exportWav = function (                       ) {
    var self = this;
    // 渲染期间防重复点击（离线渲染耗时数秒，连点会排队多个渲染任务）
    if (this._exporting) {
      if (window.UI && window.UI.toast) window.UI.toast ("⏳ 正在导出中，请等待完成", "warn");
      return;
    }
    // 仅 AUTO+JUCE就绪时可离线导出（保证按全局采样率+尾音不截断）
    var useNative = false;
    try { useNative = window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred(); } catch(e) {}
    if (!useNative) {
      /* 导出只有引擎离线渲染一条实现（JUCE bounce：按全局采样率渲染 +
        尾音不截断）。非引擎模式不再承诺"走浏览器导出"——那条路从未存在，
         用户点了导出却什么都没发生 */
      if (window.UI && window.UI.toast) {
        UI.toast ("✗ WAV 导出需要音频引擎（桌面版）；浏览器模式不支持导出", "err");
      }
      return;
    }
    if (!this.tracks || !this.tracks.length) {
      if (window.UI && window.UI.toast) window.UI.toast ("⚠ 无轨道可导出", "warn");
      return;
    }
    this._exporting = true;
    var btn = document.getElementById("arrExportWavBtn");
    var origLabel = btn ? btn.textContent : "";
    if (btn) { btn.disabled = true; btn.textContent = "渲染中…"; }
    var finish = function () {
      self._exporting = false;
      if (btn) { btn.disabled = false; btn.textContent = origLabel; }
    };
    this.showHUD("⏳ 正在离线渲染 WAV（尾音到静默）…");
    var payload = { bpm: this.bpm, tracks: this.tracks };
    // 优先走 Wails 直通（低延迟），失败回退 HTTP
    var p = null;
    if (window.EngineBridge && window.EngineBridge.bounce) {
      try { p = window.EngineBridge.bounce(payload); } catch(e) { p = null; }
    }
    if (!p || !p.then) {
      p = fetch("/api/audio/bounce", { method:"POST", headers:{"Content-Type":"application/json"}, body: JSON.stringify(payload) }).then(function(r){ return r.json(); });
    }
    Promise.resolve(p).then(function(res){
      var url = null;
      if (typeof res === "string") {
        url = "/api/audio/bounce/file?path=" + encodeURIComponent(res);
      } else {
        url = res && (res.url || res.path);
        if (url && url.indexOf("/api/") !== 0 && res.path) url = "/api/audio/bounce/file?path=" + encodeURIComponent(res.path);
      }
      if (!url) throw new Error("无下载链接");
      self.showHUD("✅ 渲染完成，开始下载…");
      return UI.download(url);
    }).catch(function(err){
      self.showHUD("✗ 导出失败（可重试；持续失败请检查磁盘空间或引擎状态）");
      if (window.UI && window.UI.toast) window.UI.toast ("✗ 导出失败: " + (err && err.message || err), "err");
    }).finally(finish);
  };

  /* ═══════════ 缩放 ═══════════ */

  Arrange.prototype.zoomStep = function (                         dir     ) {
    this.setPpb(this.ppb + dir * 4);
  };

  /** 横向缩放：以视口 40% 处的拍位为锚点，缩放后回滚到原锚点 */
  /* 总输出音量（0-1）：走带条滑块入口，作用于引擎 masterGain。
     引擎模式（无 WebAudio ctx）时 masterGain 为空，静默跳过 */
  Arrange.prototype.applyMasterVolume = function (                         v        ) {
    var g = this.engine && this.engine.masterGain;
    if (!g) return;
    var ctx = window.SharedAudio && window.SharedAudio.get();
    if (!ctx) return;
    try { g.gain.setTargetAtTime(Math.max(0, Math.min(1, v)), ctx.currentTime, 0.01); } catch (e) {}
  };

  Arrange.prototype.setPpb = function (                         val     ) {
    val = clamp(Math.round(val), 8, 80);
    if (val === this.ppb) return;
    var scroller = this.el.arrTracksScroll;
    var centerBeat = 0;
    if (scroller) {
      centerBeat = (scroller.scrollLeft + scroller.clientWidth * 0.4) / this.ppb;
    }
    this.ppb = val;
    this.renderAll();
    this.renderAllThumbsSoon();
    if (scroller) {
      scroller.scrollLeft = Math.max(0, centerBeat * this.ppb - scroller.clientWidth * 0.4);
    }
    this.showHUD("缩放: " + val + " px/拍");
    this.scheduleSave();
  };

  /* ═══════════ 吸附下拉 ═══════════ */

  Arrange.prototype.bindSnapDropdown = function (                       ) {
    var self = this;
    var wrap = this.el.arrSnapDropdown;
    var btn = this.el.arrSnapBtn;
    var menu = this.el.arrSnapMenu;
    if (!wrap || !btn || !menu) return;

    function renderMenu() {
      menu .innerHTML = "";
      SNAPS.forEach(function (s) {
        var opt = document.createElement("div");
        opt.className = "select-option" + (self.snap === s.v ? " selected" : "");
        opt.textContent = s.label;
        opt.addEventListener("click", function () {
          self.snap = s.v;
          menu .hidden = true;
          wrap .classList.remove("open");
          self.syncTransportUI();
          self.showHUD("吸附: " + s.label);
          self.scheduleSave();
        });
        menu .appendChild(opt);
      });
    }

    btn.addEventListener("click", function (e     ) {
      e.stopPropagation();
      var show = menu .hidden;
      if (show) {
        renderMenu();
        menu .classList.add("menu-in");
        menu .hidden = false;
        wrap .classList.add("open");
      } else {
        menu .hidden = true;
        wrap .classList.remove("open");
      }
      btn .blur();
    });
    document.addEventListener("mousedown", function (e) {
      if (!menu .hidden && !wrap .contains(e.target        )) {
        menu .hidden = true;
        wrap .classList.remove("open");
      }
    });
  };

  /* ═══════════ 轨道区事件绑定（委托） ═══════════ */

  Arrange.prototype.bindTracksArea = function (                       ) {
    var self = this;
    var inner = this.el.arrInner;

    // ＋新建轨道按钮
    if (this.el.arrAddTrackBtn) {
      this.el.arrAddTrackBtn.addEventListener("click", function () {
        self.addTrack();
      });
    }

    if (!inner) return;

    // 左键：轨道头选中 / Clip 手势起点 / 空白处取消选择
    inner.addEventListener("mousedown", function (e     ) {
      if (e.button !== 0) return;
      var headEl = e.target.closest (".arr-track-head");
      if (headEl) {
        self.selectTrack(Number(headEl.dataset.trackIdx));
        return;
      }
      var handleEl = e.target.closest (".arr-clip-handle");
      var edgeEl = e.target.closest (".arr-clip-edge");
      var clipEl = e.target.closest (".arr-clip");
      if (clipEl && (handleEl || edgeEl)) {
        var mode = handleEl
          ? (handleEl.classList.contains("left") ? "fadeL" : "fadeR")
          : (edgeEl.classList.contains("left") ? "trimL" : "trimR");
        e.preventDefault();
        self.beginClipGesture(e, clipEl, mode);
        return;
      }
      if (clipEl) {
        e.preventDefault();
        self.beginClipGesture(e, clipEl, "move");
        return;
      }
      if (!e.shiftKey) self.clearSelection();
    });

    // 双击：MIDI 片段在钢琴卷帘打开源文件；音频片段试听
    inner.addEventListener("dblclick", function (e     ) {
      var clipEl = e.target.closest (".arr-clip");
      /* 兜底：target 因任何原因落在轨道容器上时，按坐标重新命中 */
      if (!clipEl && typeof document.elementFromPoint === "function") {
        var hit = document.elementFromPoint(e.clientX, e.clientY);
        if (hit && hit.closest) clipEl = hit.closest (".arr-clip");
      }
      if (!clipEl) return;
      var found = self.locateClip(clipEl);
      if (!found) return;
      if (found.clip.type === "midi" && found.clip.fullName && window.PianoRoll) {
        window.PianoRoll.openFile(self.projectId       , found.clip.fullName, "");
      } else if (found.clip.type === "audio" && found.clip.src) {
        self.engine.resume();
        self.engine.previewSample(found.clip.src.p).catch(function () {
          if (UI.toast) UI.toast ("✗ 音频试听失败（文件不存在或格式不支持）", "err");
        });
      }
    });

    // 右键菜单：轨道头 / Clip / 空白处（含表头空白列）
    inner.addEventListener("contextmenu", function (e     ) {
      e.preventDefault();
      var headEl = e.target.closest (".arr-track-head");
      if (headEl) {
        var idx = Number(headEl.dataset.trackIdx);
        self.selectTrack(idx);
        self.openTrackMenu(e, idx);
        return;
      }
      var clipEl = e.target.closest (".arr-clip");
      if (clipEl) {
        var found = self.locateClip(clipEl);
        if (found && self.selectedClips.indexOf(found.clip.id) === -1) {
          self.selectOnly(found.clip.id);
        }
        self.openClipMenu(e, clipEl);
        return;
      }
      self.openBlankMenu(e);
    });

    // 拖放入轨：dragover 高亮 + drop 放置
    var scroller = this.el.arrTracksScroll;
    if (scroller) {
      scroller.addEventListener("dragover", function (e     ) {
        if (!self.isArrangeDrag(e)) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
        if (self.isSourceDrag(e)) {
          /* 音源拖拽 = 替换轨道音源：整行高亮示意，不放 clip 剪影
             （剪影会让人误以为要创建 MIDI Clip） */
          self.hideDropGhost();
          self.updateSourceHighlight(e);
          return;
        }
        self.updateDropHighlight(e);
        /* 剪影预览：与 drop 用同一份 resolveDropTarget 计算，
           拖动所见位置 = 松手放置位置 */
        self.updateDropGhost(self.resolveDropTarget(e));
      });
      scroller.addEventListener("dragleave", function (e     ) {
        self.clearDropHighlight();
        self.clearSourceHighlight();
      });
      scroller.addEventListener("drop", function (e     ) {
        if (!self.isArrangeDrag(e)) return;
        e.preventDefault();
        self.clearDropHighlight();
        self.clearSourceHighlight();
        self.handleDrop(e);
      });
    }

    // 窗口级鼠标拖拽收尾（手势与标尺拖拽共用）
    window.addEventListener("mousemove", function (e) {
      if (self.dragState) self.updateClipGesture(e);
      if (self.rulerDrag) self.updateRulerDrag(e);
    });
    window.addEventListener("mouseup", function () {
      if (self.dragState) self.finishClipGesture();
      if (self.rulerDrag) self.finishRulerDrag();
    });
  };

  /* ═══════════ 坐标换算 ═══════════ */

  /** 客户端 X → 时间线拍（基于滚动容器，含滚动偏移与轨道头宽度补偿） */
  Arrange.prototype.clientXToBeat = function (                         clientX     ) {
    var scroller = this.el.arrTracksScroll;
    if (!scroller) return 0;
    var r = scroller.getBoundingClientRect();
    return (clientX - r.left + scroller.scrollLeft - HEADER_W) / this.ppb;
  };

  Arrange.prototype.snapBeat = function (                         beat     ) {
    if (!this.snap) return Math.round(beat * 1000) / 1000;
    return Math.round(beat / this.snap) * this.snap;
  };

  /** 循环区间端点按小节吸附 */
  Arrange.prototype.barSnap = function (                         beat     ) {
    return Math.round(beat / BAR_BEATS) * BAR_BEATS;
  };

  Arrange.prototype.contentEndBeat = function (                       ) {
    var end = BAR_BEATS;
    this.tracks.forEach(function (t) {
      t.clips.forEach(function (c) { end = Math.max(end, c.start + c.length); });
    });
    return end;
  };

  /* ═══════════ 标尺交互（下半播放头 / 上半循环区间） ═══════════ */

  Arrange.prototype.bindRulerEvents = function (                       ) {
    // 已并入 bindTracksArea 的窗口级 mousemove/up；标尺自身事件在 initRulerPointer 绑定
  };

  Arrange.prototype.initRulerPointer = function (                       ) {
    if (this._rulerPointerBound) return; // 只绑定一次
    var self = this;
    var canvas = this.el.arrRulerCanvas;
    if (!canvas) return;
    this._rulerPointerBound = true;

    canvas.addEventListener("mousedown", function (e     ) {
      if (e.button !== 0) return;
      e.preventDefault();
      var rect = canvas .getBoundingClientRect();
      var upper = (e.clientY - rect.top) < RULER_H / 2;
      var beat = clamp(self.clientXToBeat(e.clientX), 0, 1e6);
      if (upper) {
        self.rulerDrag = { mode: "loop", anchor: self.barSnap(beat) };
        self.loop.on = true;
        self.loop.start = self.loop.anchor = self.rulerDrag.anchor ;
        self.loop.end = self.rulerDrag.anchor  + BAR_BEATS;
        /* 拖拽期间给引擎一份快照而非实时引用：否则引擎心跳每 25ms 读到
           正在变化的边界，回绕点随拖拽漂移（播放头/调度映射抖动）。
           finishRulerDrag 恢复实时引用 */
        self.engine.loop = { on: true, start: self.loop.start, end: self.loop.end };
        self.syncTransportUI();
        self.renderRuler();
      } else {
        var wasPlaying = self.isPlaying;
        if (wasPlaying) self.pausePlayback(true);
        self.seekTo(self.snapBeat(Math.max(0, beat)), false);
        self.rulerDrag = { mode: "seek", wasPlaying: wasPlaying };
      }
    });

    canvas.addEventListener("dblclick", function (e     ) {
      var rect = canvas .getBoundingClientRect();
      if ((e.clientY - rect.top) < RULER_H / 2 && self.loop.on) {
        self.loop.on = false;
        self.engine.loop = self.loop;
        self.syncTransportUI();
        self.renderRuler();
        self.showHUD("循环区间已清除");
        self.scheduleSave();
      }
    });
  };

  Arrange.prototype.updateRulerDrag = function (                         e     ) {
    if (!this.rulerDrag) return;
    var beat = clamp(this.clientXToBeat(e.clientX), 0, 1e6);
    if (this.rulerDrag.mode === "seek") {
      this.seekTo(Math.max(0, this.snapBeat(beat)), false);
    } else {
      var cur = this.barSnap(beat);
      this.loop.start = Math.min(this.rulerDrag.anchor , cur);
      this.loop.end = Math.max(this.rulerDrag.anchor  + BAR_BEATS, cur === this.rulerDrag.anchor ? cur + BAR_BEATS : cur);
      this.renderRuler();
    }
  };

  Arrange.prototype.finishRulerDrag = function (                       ) {
    if (!this.rulerDrag) return;
    var d = this.rulerDrag;
    this.rulerDrag = null;
    if (d.mode === "loop") {
      delete this.loop.anchor;
      this.engine.loop = this.loop;
      this.showHUD("循环区间: " + (this.loop.start / BAR_BEATS + 1) + " – " + (this.loop.end / BAR_BEATS + 1) + " 小节");
      this.renderRuler();
      this.scheduleSave();
    } else if (d.mode === "seek") {
      if (d.wasPlaying) this.startPlayback();
    }
  };

  /* ═══════════ Clip 手势（移动 / 裁剪 / 渐变） ═══════════ */

  Arrange.prototype.locateClip = function (                         clipEl     ) {
    var row = clipEl.closest (".arr-lane-row");
    if (!row) return null;
    var trackIdx = Number(row.dataset.trackIdx);
    var track = this.tracks[trackIdx];
    if (!track) return null;
    var clipId = clipEl.dataset.clipId;
    for (var i = 0; i < track.clips.length; i++) {
      if (track.clips[i].id === clipId) return { track: track, trackIdx: trackIdx, clip: track.clips[i], clipEl: clipEl };
    }
    return null;
  };

  Arrange.prototype.beginClipGesture = function (                         e     , clipEl     , mode     ) {
    var found = this.locateClip(clipEl);
    if (!found) return;

    // Shift 点击多选；普通点击单选
    if (e.shiftKey) {
      this.toggleClipSelect(found.clip.id);
    } else if (this.selectedClips.indexOf(found.clip.id) === -1) {
      this.selectOnly(found.clip.id);
    }

    var spb = this.secondsPerBeat();
    var grabOffsetPx = 0;
    var clipRect = clipEl.getBoundingClientRect();
    grabOffsetPx = e.clientX - clipRect.left;

    /* Ctrl+拖动 = 克隆（FL Playlist 惯例；Alt 为旧版肌肉记忆兼容）。
       延迟到位移超阈值才真正克隆：Ctrl+点击（不拖动）不产生副本 */
    var wantClone = mode === "move" && (e.ctrlKey || e.metaKey || e.altKey);

    /* 手势起点快照（移动/裁剪/渐变共用）——必须在克隆等一切变异
       之前抓取，否则快照里已包含克隆，撤销后克隆残留。
       只暂存不压栈：真正产生了变更（finish 判定）才入撤销栈，
       避免"点一下没动"也污染撤销记录 */
    this.dragState = {
      mode: mode,
      clipEl: clipEl,
      found: found,
      startX: e.clientX,
      startY: e.clientY,
      grabOffsetPx: grabOffsetPx,
      origStart: found.clip.start,
      origLength: found.clip.length,
      origFadeIn: found.clip.fadeIn || 0,
      origFadeOut: found.clip.fadeOut || 0,
      spb: spb,
      lastThumbDraw: 0,
      changed: false,
      movedToTrackIdx: found.trackIdx,
      clonePending: wantClone,
      peOff: false,
      pendingHistory: this.snapshot(),
      /* Alt 按下 = 临时禁用网格吸附（FL 惯例；Alt 曾被克隆占用，
         克隆已改回 Ctrl，与 FL Playlist 一致） */
      freeSnap: e.altKey && !wantClone
    };
    /* pointerEvents 不在按下瞬间隐藏：click/dblclick 的 target 取
       mousedown 与 mouseup 命中点的公共祖先，提前隐藏会把双击重定向到
       轨道容器（双击打开钢琴窗/试听失灵的根因）。真正拖起来才隐藏 */
  };

  /** 手势期间吸附：Alt 按下时跳过吸附（与钢琴窗 freeSnap 同语义） */
  Arrange.prototype.gestureSnap = function (                         beat     , e     ) {
    if (e && e.altKey) return Math.max(0, Math.round(beat * 1000) / 1000);
    return this.snapBeat(beat);
  };

  /** 遍历当前选中的 clip（含所在轨道） */
  Arrange.prototype.forEachSelectedClip = function (                         fn     ) {
    var self = this;
    this.tracks.forEach(function (track) {
      track.clips.forEach(function (clip) {
        if (self.selectedClips.indexOf(clip.id) !== -1) fn(track, clip);
      });
    });
  };

  Arrange.prototype.updateClipGesture = function (                         e     ) {
    var d = this.dragState;
    if (!d || !d.found) return;
    var clip = d.found.clip;
    var el = d.clipEl;
    var now = performance.now();

    /* 位移超过阈值才算真的在拖：此时才隐藏 clip 的指针命中，
       elementFromPoint（跨轨判定）才能看到下方的轨道 */
    if (!d.peOff && (Math.abs(e.clientX - d.startX ) > 3 || Math.abs(e.clientY - d.startY ) > 3)) {
      d.peOff = true;
      if (el) el.style.pointerEvents = "none";
    }

    if (d.mode === "move") {
      /* 延迟克隆：Ctrl 按下后位移超过阈值（4px）才克隆选中 Clip 并
         改拖副本——Ctrl+点击（不拖动）不产生副本（FL Playlist 惯例） */
      if (d.clonePending) {
        if (Math.abs(e.clientX - d.startX ) + Math.abs(e.clientY - d.startY ) <= 4) return;
        var clones        = [];
        this.forEachSelectedClip(function (track     , clip     ) {
          var copy = JSON.parse(JSON.stringify(clip));
          copy.id = uid("clip");
          track.clips.push(copy);
          clones.push(copy.id);
        });
        if (clones.length) {
          this.selectedClips = clones;
          this.renderTracks();
          var newEl = this.el.arrLanes .querySelector('.arr-clip[data-clip-id="' + clones[0] + '"]');
          if (newEl) {
            if (el) el.style.pointerEvents = "";
            var rel = this.locateClip(newEl);
            if (rel) {
              d.found = rel;   // 后续移动作用于克隆体（局部 clip/el 同步换新）
              clip = rel.clip;
            }
            el = newEl;
            d.clipEl = newEl;
            el.style.pointerEvents = "none";
          }
          d.changed = true;
        }
        d.clonePending = false;
        this.showHUD("⧉ 克隆拖动");
      }

      // 横向：吸附移动（Alt 临时禁用吸附）；纵向：跨轨
      var newStart = this.gestureSnap(Math.max(0, this.clientXToBeat(e.clientX) - d.grabOffsetPx  / this.ppb), e);
      if (Math.abs(newStart - clip.start) > 1e-6) {
        var delta = newStart - clip.start;
        clip.start = newStart;
        d.changed = true;
        // 多选拖拽：其余选中剪辑跟随相同位移（此前只动被抓的，多选移动静默丢失部分剪辑）
        if (this.selectedClips.indexOf(clip.id) !== -1 && this.selectedClips.length > 1) {
          var selfM = this;
          var anchorId = clip.id;
          this.forEachSelectedClip(function (t2     , c2     ) {
            if (c2.id === anchorId) return;
            c2.start = Math.max(0, c2.start + delta);
            var el2 = selfM.el.arrLanes
              ? selfM.el.arrLanes.querySelector('.arr-clip[data-clip-id="' + c2.id + '"]')
              : null;
            if (el2) selfM.positionClipEl(el2, c2);
          });
        }
      }
      var laneEl = document.elementFromPoint(e.clientX, e.clientY);
      var contentEl = laneEl && laneEl.closest ? laneEl.closest (".arr-lane-content") : null;
      if (contentEl) {
        var rowEl = contentEl.closest (".arr-lane-row");
        var targetIdx = Number(rowEl .dataset.trackIdx);
        if (!isNaN(targetIdx) && targetIdx !== d.movedToTrackIdx && this.tracks[targetIdx]) {
          // 从原轨摘除挂到新轨（DOM 同步搬移，数据在收尾时统一落）
          var oldRow = d.found.trackIdx;
          var oldTrack = this.tracks[oldRow];
          var ci = oldTrack.clips.indexOf(clip);
          if (ci !== -1) oldTrack.clips.splice(ci, 1);
          this.tracks[targetIdx].clips.push(clip);
          contentEl.appendChild(el );
          d.found.trackIdx = targetIdx;
          d.found.track = this.tracks[targetIdx];
          d.movedToTrackIdx = targetIdx;
          d.changed = true;
        }
      }
      this.positionClipEl(el, clip);
    } else if (d.mode === "trimL") {
      var endFixed = d.origStart  + d.origLength ;
      /* 音频素材不能裁到文件头之前：可向左恢复的量 = 当前 offset（秒）
         换算成拍；越界会造成"画面帧界与实际发声内容错位" */
      var minStart = d.origStart;
      if (clip.type === "audio") {
        minStart = d.origStart  - Math.max(0, clip.offset || 0) / d.spb ;
      }
      var newStart = clamp(this.gestureSnap(this.clientXToBeat(e.clientX), e), Math.max(0, minStart ), endFixed - MIN_CLIP_LEN);
      var delta = newStart - d.origStart ;
      if (Math.abs(delta) > 1e-6) {
        clip.start = newStart;
        clip.length = endFixed - newStart;
        if (clip.type === "audio") clip.offset = Math.max(0, (clip.offset || 0) + delta * d.spb );
        d.changed = true;
      }
      this.positionClipEl(el, clip);
    } else if (d.mode === "trimR") {
      var newLen = clamp(this.gestureSnap(this.clientXToBeat(e.clientX), e) - clip.start, MIN_CLIP_LEN, 1e5);
      if (Math.abs(newLen - clip.length) > 1e-6) {
        clip.length = newLen;
        d.changed = true;
      }
      this.positionClipEl(el, clip);
    } else if (d.mode === "fadeL") {
      var sec = Math.max(0, (this.clientXToBeat(e.clientX) - clip.start) * d.spb );
      clip.fadeIn = Math.round(Math.min(sec, clip.length * d.spb  * 0.9) * 100) / 100;
      clip._rev = (clip._rev || 0) + 1; // 缩略图 key 含 rev，渐变变化需强制重绘
      d.changed = true;
    } else if (d.mode === "fadeR") {
      var endSec = (clip.start + clip.length) * d.spb ;
      var sec2 = Math.max(0, endSec - this.clientXToBeat(e.clientX) * d.spb );
      clip.fadeOut = Math.round(Math.min(sec2, clip.length * d.spb  * 0.9) * 100) / 100;
      clip._rev = (clip._rev || 0) + 1;
      d.changed = true;
    }

    // 渐变手柄实时重绘缩略（~12fps 节流）
    if ((d.mode === "fadeL" || d.mode === "fadeR" || d.mode === "trimL" || d.mode === "trimR") && now - d.lastThumbDraw  > 80) {
      d.lastThumbDraw = now;
      this.drawThumbFor(el);
    }
  };

  Arrange.prototype.finishClipGesture = function (                       ) {
    var d = this.dragState;
    this.dragState = null;
    if (!d) return;
    if (d.clipEl) d.clipEl.style.pointerEvents = "";
    if (!d.changed) return;
    /* 变更已发生 → 手势起点的暂存快照此刻才入撤销栈 */
    if (d.pendingHistory !== undefined && d.pendingHistory !== null) {
      this.undoStack.push(d.pendingHistory);
      if (this.undoStack.length > 50) this.undoStack.shift();
      this.redoStack = [];
      d.pendingHistory = null;
    }
    this.drawThumbFor(d.clipEl);
    this.applyMixSafe();
    this.showHUD(d.mode === "move" ? ("位置: " + (d.found .clip.start / BAR_BEATS + 1).toFixed(2) + " 小节")
      : d.mode === "fadeL" ? ("渐入: " + d.found .clip.fadeIn.toFixed(2) + "s")
      : d.mode === "fadeR" ? ("渐出: " + d.found .clip.fadeOut.toFixed(2) + "s")
      : "长度: " + d.found .clip.length.toFixed(2) + " 拍");
    this.updateContentWidth();
    this.scheduleSave();
  };

  Arrange.prototype.applyMixSafe = function (                       ) {
    // Web Audio 侧立即生效（本地节点操作，廉价）
    this.engine.applyMix(this.tracks);
    // 同步原生引擎混音图（M3）：轨序号即引擎轨号；浏览器模式/引擎未就绪时
    // updateTrackMix 内部自动跳过。音量滑块按 input 连续触发（每拖动帧
    // 一次 × 全轨道 IPC），故引擎同步做短防抖合并，避免 IPC 洪峰
    if (typeof this.engine.updateTrackMix === "function") {
      var self = this;
      if (this._mixSyncTimer) clearTimeout(this._mixSyncTimer);
      this._mixSyncTimer = setTimeout(function () {
        self._mixSyncTimer = null;
        var n = Math.min(self.tracks.length, 32);
        for (var i = 0; i < n; i++) {
          var t = self.tracks[i];
          /* 声像归混音台管（P4）：轨道没有 pan 字段，通道条写 mixer.channels[id].pan */
          var mixCh = (self.mixer && self.mixer.channels) ? self.mixer.channels[t.id] : null;
          self.engine.updateTrackMix(
            i,
            t.volume !== undefined ? t.volume : 0.8,
            mixCh && typeof mixCh.pan === "number" ? mixCh.pan : 0,
            !!t.mute,
            !!t.solo,
            true
          );
        }
        /* 收缩尾槽：删轨后引擎侧残留的槽位保留旧 gain/mute/solo——若被删
           的是 solo 轨，引擎 anySolo 恒真，其余全部轨被混音器丢弃（播放全
           无声）。对上次发过、本次已不存在的槽位显式发 active=false */
        var prev = self._lastMixTrackCount || 0;
        for (var j = n; j < Math.min(prev, 32); j++) {
          self.engine.updateTrackMix(j, 0, 0, false, false, false);
        }
        self._lastMixTrackCount = n;
      }, 80);
    }
    this.rescheduleSamplesDebounced();
  };

  /** 汇总当前全部音频 clip → 引擎调度表。scheduleSamples 为全量重建语义，
      SamplePool 按路径缓存已解码样本，重复下发只做表重建不重新解码。
      失败时 toast 具体原因（素材目录未挂载等此前只写日志，用户侧表现为
      "没声音"）。严格路由：引擎模式下失败即静音该素材，不回退 Web 渲染
      （_nativeSamplesFailed 仅作状态标记，不再驱动降级） */
  Arrange.prototype.sendSampleSchedule = function (                       ) {
    if (!window.EngineBridge) return null;
    var clips = [];
    for (var ti = 0; ti < this.tracks.length; ti++) {
      var tr = this.tracks[ti];
      for (var ci = 0; ci < tr.clips.length; ci++) {
        var c = tr.clips[ci];
        if (c.mute || c.type !== "audio" || !c.src || !c.src.p) continue;
        clips.push({ track: ti, path: c.src.p, start: c.start, length: c.length, offset: c.offset || 0, fadeIn: c.fadeIn || 0, fadeOut: c.fadeOut || 0, gain: c.gain !== undefined ? c.gain : 1 });
      }
    }
    var self = this;
    try {
      var p = window.EngineBridge.scheduleSamples(clips, this.bpm);
      if (p && typeof p.then === "function") {
        return p.then(function () {
          self.engine._nativeSamplesFailed = false;
        }).catch(function (err) {
          self.engine._nativeSamplesFailed = true;
          if (UI.toast) UI.toast ("⚠ 音频素材调度失败，相关剪辑静音（ENGINE 模式不降级）：" + (err && err.message || err), "warn");
        });
      }
      return p;
    } catch (e) {
      this.engine._nativeSamplesFailed = true;
      return null;
    }
  };

  /** 调度签名：坐标/长度/offset/mute/fade/BPM 任一变化都算——用于跳过
      音量拖动等不影响素材表的 applyMixSafe 调用。
      BPM 必须在签名内：scheduleSamples 把剪辑坐标按当时 BPM 烘焙成采样数，
      播放中改速若不重发，音频轨会继续按旧 BPM 的表发声，与 MIDI 轨渐进
      失同步（此前签名漏 BPM，setBpm 触发的重排被去重跳过） */
  Arrange.prototype.sampleScheduleSig = function (                       ) {
    var parts = ["bpm:" + this.bpm];
    for (var ti = 0; ti < this.tracks.length; ti++) {
      var tr = this.tracks[ti];
      for (var ci = 0; ci < tr.clips.length; ci++) {
        var c = tr.clips[ci];
        if (c.type !== "audio" || !c.src || !c.src.p) continue;
        parts.push(ti + ":" + c.start.toFixed(3) + ":" + c.length.toFixed(3) + ":" +
          Math.round(c.offset || 0) + ":" + (c.mute ? 1 : 0) + ":" +
          (c.fadeIn || 0).toFixed(2) + ":" + (c.fadeOut || 0).toFixed(2));
      }
    }
    return parts.join("|");
  };

  /** 播放中的结构编辑必须重发调度表：原生引擎的音频只在按下播放那一刻
      的快照上发声，此后移动/裁剪/删增 clip 若不重发，轨道要么在旧坐标
      出声、要么整段静音（MIDI 不受影响——音符是心跳实时注入的） */
  Arrange.prototype.rescheduleSamplesDebounced = function (                       ) {
    if (!this.isPlaying) return;
    if (!(window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred())) return;
    var self = this;
    if (this._reschedTimer) clearTimeout(this._reschedTimer);
    this._reschedTimer = setTimeout(function () {
      self._reschedTimer = null;
      if (!self.isPlaying) return;
      var sig = self.sampleScheduleSig();
      if (sig === self._lastSampleSig) return; // 表没变（音量调节等），跳过
      self._lastSampleSig = sig;
      self.sendSampleSchedule();
    }, 120);
  };

  /* ═══════════ 渲染管线 ═══════════ */

  Arrange.prototype.renderAll = function (                       ) {
    this.updateContentWidth();
    this.renderTracks();
    this.resizeRulerCanvas();
    this.renderRuler();
    this.updatePlayline();
    this.updatePosDisplay();
  };

  /** 内容宽度：最后片段末端 + 余量，最小 64 小节 */
  Arrange.prototype.updateContentWidth = function (                       ) {
    if (!this.el.arrInner) return;
    var endBeat = Math.max(MIN_BARS * BAR_BEATS, this.contentEndBeat() + BAR_BEATS * 8);
    var width = HEADER_W + endBeat * this.ppb;
    this.el.arrInner.style.width = width + "px";
    // 小节线背景间距同步到每条 lane
    var barPx = (this.ppb * BAR_BEATS) + "px";
    var lanes = this.el.arrLanes ? this.el.arrLanes.querySelectorAll(".arr-lane-content") : [];
    for (var i = 0; i < lanes.length; i++) {
      lanes[i].style.setProperty("--bar-px", barPx);
    }
  };

  Arrange.prototype.renderTracks = function (                       ) {
    var lanesEl = this.el.arrLanes;
    if (!lanesEl) return;
    var self = this;
    this.meterEls = {};
    /* 混音台通道条与轨道 1:1：增删轨道/改名/换色后增量对齐（id 相同只刷内容） */
    if (window.ArrMixer) window.ArrMixer.syncTracks(this);
    // 释放对旧剪辑元素的观察（IO 持强引用，不 unobserve 则随每次重建累积泄漏）
    if (this.thumbObserver) {
      var old = lanesEl.querySelectorAll(".arr-clip");
      for (var oi = 0; oi < old.length; oi++) this.thumbObserver.unobserve(old[oi]);
    }
    lanesEl.innerHTML = "";

    this.tracks.forEach(function (track, idx) {
      var row = document.createElement("div");
      row.className = "arr-lane-row" + (idx === self.selectedTrackIdx ? " selected-track" : "");
      row.dataset.trackIdx = idx       ;
      // 行高由 CSS .arr-lane-row 统一控制（72px），此处不再设置

      row.appendChild(self.buildTrackHead(track, idx));

      var content = document.createElement("div");
      content.className = "arr-lane-content";
      content.dataset.trackIdx = idx       ;
      content.style.setProperty("--bar-px", (self.ppb * BAR_BEATS) + "px");

      track.clips.forEach(function (clip) {
        content.appendChild(self.buildClipEl(track, clip));
      });

      row.appendChild(content);
      lanesEl .appendChild(row);
    });
  };

  Arrange.prototype.buildTrackHead = function (                         track     , idx     ) {
    var self = this;
    var head = document.createElement("div");
    head.className = "arr-track-head";
    head.dataset.trackIdx = idx;

    var color = document.createElement("div");
    color.className = "arr-th-color";
    head.appendChild(color);

    var body = document.createElement("div");
    body.className = "arr-th-body";

    var name = document.createElement("div");
    name.className = "arr-th-name";
    name.textContent = track.name;
    name.title = track.name + "（右键菜单 / F2 重命名）";
    body.appendChild(name);

    var controls = document.createElement("div");
    controls.className = "arr-th-controls";

    var muteBtn = document.createElement("button");
    muteBtn.type = "button";
    muteBtn.className = "arr-th-btn" + (track.mute ? " mute-on" : "");
    muteBtn.textContent = "M";
    muteBtn.title = "静音 (M)";
    muteBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      self.toggleMute(idx);
    });

    var soloBtn = document.createElement("button");
    soloBtn.type = "button";
    soloBtn.className = "arr-th-btn" + (track.solo ? " solo-on" : "");
    soloBtn.textContent = "S";
    soloBtn.title = "独奏 (S)";
    soloBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      self.toggleSolo(idx);
    });

    var vol = document.createElement("input");
    vol.type = "range";
    vol.className = "arr-th-vol";
    vol.min = "0"; vol.max = "1"; vol.step = "0.01";
    vol.value = String(track.volume !== undefined ? track.volume : 0.8);
    vol.title = "轨道音量";
    /* 快照在首次 input 前抓取（值尚未写入 track）——此前在 change（松手
       后）才 pushHistory，快照已是改后状态，音量撤销是空操作 */
    var volSnapshot      = null;
    vol.addEventListener("input", function () {
      if (volSnapshot === null) volSnapshot = self.snapshot();
      track.volume = Number(vol.value);
      self.applyMixSafe();
    });
    vol.addEventListener("change", function () {
      if (volSnapshot !== null) {
        self.undoStack.push(volSnapshot);
        if (self.undoStack.length > 50) self.undoStack.shift();
        self.redoStack = [];
        volSnapshot = null;
      }
      self.scheduleSave();
    });

    controls.appendChild(muteBtn);
    controls.appendChild(soloBtn);
    controls.appendChild(vol);
    body.appendChild(controls);

    var srcRow = document.createElement("div");
    srcRow.className = "arr-th-src";

    var srcBtn = document.createElement("button");
    srcBtn.type = "button";
    srcBtn.className = "arr-th-src-btn";
    srcBtn.textContent = self.sourceBadgeText(track);
    srcBtn.title = "点击加载音源（合成器 / 内置音色 / SF2 音源库）";
    srcBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var r = srcBtn.getBoundingClientRect();
      self.openSourceMenu(track, r.left, r.bottom + 4);
    });

    var meter = document.createElement("div");
    meter.className = "arr-th-meter";
    var meterBar = document.createElement("i");
    meter.appendChild(meterBar);
    this.meterEls[track.id] = meterBar;

    srcRow.appendChild(srcBtn);
    srcRow.appendChild(meter);
    body.appendChild(srcRow);

    head.appendChild(body);

    // 轨道头颜色条跟随主题色
    color.style.background = track.color;
    return head;
  };

  /** 轨道头音源标签：v2 由机架条目解析（解析不到明确提示，不显示假音色） */
  Arrange.prototype.sourceBadgeText = function (                         track     ) {
    var voice = window.ArrRack.resolveSource(track.source);
    if (!voice.ok) return "♪ ⚠ 音源缺失";
    if (voice.missing) return "♪ ⚠ " + shortLabel(voice.label);
    return "♪ " + shortLabel(voice.label);
  };

  function shortLabel(text     ) {
    var s = String(text || "");
    return s.length > 22 ? s.slice(0, 21) + "…" : s;
  }

  /* ═══════════ Clip 元素 ═══════════ */

  Arrange.prototype.buildClipEl = function (                         track     , clip     ) {
    var el = document.createElement("div");
    el.className = "arr-clip type-" + clip.type + (clip.mute ? " muted" : "")
      + (this.selectedClips.indexOf(clip.id) !== -1 ? " selected" : "");
    el.dataset.clipId = clip.id;
    el.style.setProperty("--clip-color", track.color);
    el.style.setProperty("--clip-bg", hexToRgba(track.color, 0.13));

    var head = document.createElement("div");
    head.className = "arr-clip-head";
    var nameSpan = document.createElement("span");
    nameSpan.className = "arr-clip-name";
    nameSpan.textContent = clip.name;
    head.appendChild(nameSpan);
    var tag = document.createElement("span");
    tag.className = "arr-clip-type-tag";
    tag.textContent = clip.type === "midi" ? "MIDI" : "WAVE";
    head.appendChild(tag);
    el.appendChild(head);

    var canvasEl = document.createElement("canvas");
    canvasEl.className = "arr-clip-canvas";
    el.appendChild(canvasEl);

    // 裁剪边缘热区（左右）
    var edgeL = document.createElement("div");
    edgeL.className = "arr-clip-edge left";
    el.appendChild(edgeL);
    var edgeR = document.createElement("div");
    edgeR.className = "arr-clip-edge right";
    el.appendChild(edgeR);

    // 渐变手柄仅音频片段提供（渐变作用于采样增益包络）
    if (clip.type === "audio") {
      var hL = document.createElement("div");
      hL.className = "arr-clip-handle left";
      hL.title = "拖动调整渐入时长";
      el.appendChild(hL);
      var hR = document.createElement("div");
      hR.className = "arr-clip-handle right";
      hR.title = "拖动调整渐出时长";
      el.appendChild(hR);
    }

    this.positionClipEl(el, clip);

    if (this.thumbObserver) this.thumbObserver.observe(el);
    else this.drawThumbFor(el);
    return el;
  };

  Arrange.prototype.positionClipEl = function (                         el     , clip     ) {
    el.style.width = Math.max(6, clip.length * this.ppb) + "px";
    el.style.transform = "translateX(" + (clip.start * this.ppb) + "px)";
  };

  /** 刷新单个片段元素的外观状态与几何 */
  Arrange.prototype.refreshClipEl = function (                         clipEl     , clip     ) {
    if (!clipEl) return;
    clipEl.classList.toggle("muted", !!clip.mute);
    clipEl.classList.toggle("selected", this.selectedClips.indexOf(clip.id) !== -1);
    this.positionClipEl(clipEl, clip);
    this.drawThumbFor(clipEl);
  };

  /** 缩略图绘制（IntersectionObserver 驱动；key 变化才重绘） */
  Arrange.prototype.drawThumbFor = function (                         el     ) {
    if (!el || !el.classList || !el.classList.contains("arr-clip")) return;
    var found = this.locateClip(el);
    if (!found) return;
    var track = found.track, clip = found.clip;
    var canvasEl = el.querySelector(".arr-clip-canvas");
    if (!canvasEl) return;

    var w = Math.max(2, Math.floor(el.clientWidth));
    var h = Math.max(2, Math.floor(el.clientHeight - 15));
    var key = w + "x" + h + ":r" + (clip._rev || 0) + ":" + this.bpm;
    if (el.dataset.thumbKey === key) return;
    el.dataset.thumbKey = key;

    var dpr = window.devicePixelRatio || 1;
    // 超宽片段限制画布分辨率（CSS 拉伸显示，视觉可接受且省内存）
    var cw = Math.min(w, 1600);
    canvasEl.width = cw * dpr;
    canvasEl.height = h * dpr;
    canvasEl.style.height = h + "px";
    var ctx = canvasEl.getContext("2d");
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, cw, h);

    var spb = this.secondsPerBeat();
    if (clip.type === "midi") {
      this.drawMidiThumb(ctx, track, clip, cw, h);
    } else {
      this.drawAudioThumb(ctx, track, clip, cw, h, spb);
    }
  };

  Arrange.prototype.drawMidiThumb = function (                         ctx     , track     , clip     , w     , h     ) {
    var notes = clip.notes || [];
    if (!notes.length) return;
    var lo = 127, hi = 0;
    for (var i = 0; i < notes.length; i++) {
      var p = MidiParse.noteNameToNumber(notes[i].note);
      if (p < lo) lo = p;
      if (p > hi) hi = p;
    }
    var span = Math.max(hi - lo + 1, 8);
    var len = Math.max(clip.length, 0.001);
    var rowH = h / span;
    var fill = hexToRgba(track.color, 0.9);
    for (var j = 0; j < notes.length; j++) {
      var n = notes[j];
      var pitch = MidiParse.noteNameToNumber(n.note);
      var y = h - (pitch - lo + 1) * rowH;
      var x = (n.start / len) * w;
      var nw = Math.max(1.5, ((n.end - n.start) / len) * w);
      ctx.fillStyle = hexToRgba(track.color, clamp(0.35 + (n.velocity || 100) / 200, 0.35, 0.95));
      ctx.fillRect(x, y, nw, Math.max(1.5, rowH - 1));
    }
    ctx.fillStyle = fill; // 保持引用避免优化告警
  };

  Arrange.prototype.drawAudioThumb = function (                         ctx     , track     , clip     , w     , h     , spb     ) {
    var peaks = clip._peaks;
    if (!peaks && clip.src && clip.src.p) {
      // 未解码过：异步取峰值后重绘一次
      var self = this;
      var rev = clip._rev || 0;
      this.engine.resume();
      this.engine.getSampleEntry(clip.src.p).then(function (entry     ) {
        clip._peaks = entry.peaks;
        if ((clip._rev || 0) === rev) self.invalidateThumbByClipId(clip.id);
      }).catch(function () {});
    }
    var mid = h / 2;
    ctx.strokeStyle = hexToRgba(track.color, 0.85);
    ctx.lineWidth = 1;
    ctx.beginPath();
    if (peaks && peaks.length) {
      var step = Math.max(1, Math.floor(peaks.length / w));
      for (var x = 0; x < w; x++) {
        var idx = Math.min(peaks.length - 1, Math.floor(x / w * peaks.length));
        var v = peaks[idx];
        var maxV = 0;
        for (var k = 0; k < step && idx + k < peaks.length; k++) maxV = Math.max(maxV, peaks[idx + k]);
        v = Math.max(v, maxV);
        ctx.moveTo(x + 0.5, mid - v * mid * 0.92);
        ctx.lineTo(x + 0.5, mid + v * mid * 0.92);
      }
    } else {
      ctx.moveTo(0, mid);
      ctx.lineTo(w, mid);
    }
    ctx.stroke();

    // 渐变曲线可视化（橙色斜线，随 BPM 换算像素）
    var scale = w / Math.max(6, clip.length * this.ppb);
    var fadePxIn = (clip.fadeIn || 0) / spb * this.ppb * scale;
    var fadePxOut = (clip.fadeOut || 0) / spb * this.ppb * scale;
    ctx.strokeStyle = "rgba(255,140,0,0.9)";
    ctx.beginPath();
    if (fadePxIn > 1) { ctx.moveTo(0, h); ctx.lineTo(fadePxIn, 0); }
    if (fadePxOut > 1) { ctx.moveTo(w, h); ctx.lineTo(w - fadePxOut, 0); }
    ctx.stroke();
  };

  Arrange.prototype.invalidateThumbByClipId = function (                         clipId     ) {
    var el = this.el.arrLanes
      ? this.el.arrLanes.querySelector('.arr-clip[data-clip-id="' + clipId + '"]')
      : null;
    if (!el) return;
    /* 清除缓存键再重绘：异步峰值取回时元素已带首次绘制的相同键，
       不清除会被 drawThumbFor 的键守卫拦下——从磁盘恢复的音频剪辑
       波形将永远画不出来（只见平线） */
    if (el.dataset) delete el.dataset.thumbKey;
    this.drawThumbFor(el);
  };

  Arrange.prototype.renderAllThumbsSoon = function (                       ) {
    var self = this;
    clearTimeout(this._thumbTimer);
    this._thumbTimer = setTimeout(function () {
      if (!self.el.arrLanes) return;
      var els = self.el.arrLanes.querySelectorAll(".arr-clip");
      for (var i = 0; i < els.length; i++) self.drawThumbFor(els[i]);
    }, 120);
  };

  /* ═══════════ 标尺渲染（仅可视区，避免超大画布） ═══════════ */

  Arrange.prototype.resizeRulerCanvas = function (                       ) {
    var canvas = this.el.arrRulerCanvas;
    var sticky = this.el.rulerSticky;
    if (!canvas || !sticky || !this.rulerCtx) return;
    var vw = Math.max(10, sticky.clientWidth);
    var dpr = window.devicePixelRatio || 1;
    canvas.width = Math.floor(vw * dpr);
    canvas.height = Math.floor(RULER_H * dpr);
    canvas.style.width = vw + "px";
    canvas.style.height = RULER_H + "px";
  };

  Arrange.prototype.renderRuler = function (                       ) {
    var canvas = this.el.arrRulerCanvas;
    if (!canvas || !this.rulerCtx) return;
    var ctx = this.rulerCtx;
    var dpr = window.devicePixelRatio || 1;
    var vw = canvas.width / dpr;
    var vh = canvas.height / dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, vw, vh);

    var scroller = this.el.arrTracksScroll;
    var sl = scroller ? scroller.scrollLeft : 0;
    var ppb = this.ppb;
    var barPx = ppb * BAR_BEATS;

    // 循环区间底衬
    if (this.loop.on && this.loop.end > this.loop.start) {
      var lx = this.loop.start * ppb - sl;
      var lw = (this.loop.end - this.loop.start) * ppb;
      ctx.fillStyle = "rgba(255,140,0,0.16)";
      ctx.fillRect(lx, 0, lw, vh);
      ctx.strokeStyle = "rgba(255,140,0,0.85)";
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(lx + 1, 0); ctx.lineTo(lx + 1, vh);
      ctx.moveTo(lx + lw - 1, 0); ctx.lineTo(lx + lw - 1, vh);
      ctx.stroke();
      ctx.lineWidth = 1;
    }

    // 小节线与编号（只画可视范围；主题色取一次避免循环内反复求值）
    var startBar = Math.max(0, Math.floor(sl / barPx));
    var endBar = Math.ceil((sl + vw) / barPx);
    var docStyle = getComputedStyle(document.documentElement);
    var majorColor = (docStyle.getPropertyValue("--color-border-strong") || "#888").trim();
    var minorColor = "rgba(127,127,127,0.30)";
    var labelColor = (docStyle.getPropertyValue("--color-ink-dim") || "#999").trim();
    ctx.font = '9px "Cascadia Mono", Consolas, monospace';
    ctx.textBaseline = "top";
    for (var b = startBar; b <= endBar; b++) {
      var x = Math.round(b * barPx - sl) + 0.5;
      var major = (b % 4 === 0);
      ctx.strokeStyle = major ? majorColor : minorColor;
      ctx.beginPath();
      ctx.moveTo(x, major ? 0 : 8);
      ctx.lineTo(x, vh);
      ctx.stroke();
      if (barPx > 26 || major) {
        ctx.fillStyle = labelColor;
        ctx.fillText(String(b + 1), x + 4, 3);
      }
    }

    // 拍刻度（缩放足够时）
    if (ppb >= 18) {
      ctx.strokeStyle = "rgba(127,127,127,0.22)";
      var startBeat = Math.max(0, Math.floor(sl / ppb));
      var endBeat = Math.ceil((sl + vw) / ppb);
      ctx.beginPath();
      for (var bt = startBeat; bt <= endBeat; bt++) {
        if (bt % BAR_BEATS === 0) continue;
        var bx = Math.round(bt * ppb - sl) + 0.5;
        ctx.moveTo(bx, vh - 7);
        ctx.lineTo(bx, vh);
      }
      ctx.stroke();
    }

    // 上/下半区功能分隔提示线
    ctx.strokeStyle = "rgba(127,127,127,0.15)";
    ctx.beginPath();
    ctx.moveTo(0, RULER_H / 2 + 0.5);
    ctx.lineTo(vw, RULER_H / 2 + 0.5);
    ctx.stroke();
  };

  /* ═══════════ 播放线 / 位置显示 / UI 心跳 ═══════════ */

  Arrange.prototype.updatePlayline = function (                       ) {
    if (this.el.arrPlayline) {
      this.el.arrPlayline.hidden = false;
      this.el.arrPlayline.style.transform = "translateX(" + (this.playheadBeat * this.ppb) + "px)";
    }
  };

  Arrange.prototype.fmtPos = function (                         beat     ) {
    var bar = Math.floor(beat / BAR_BEATS) + 1;
    var bt = Math.floor(beat % BAR_BEATS) + 1;
    return bar + "." + bt;
  };

  Arrange.prototype.updatePosDisplay = function (                       ) {
    if (this.el.arrPosDisplay) {
      this.el.arrPosDisplay.textContent = this.fmtPos(this.playheadBeat);
    }
  };

  Arrange.prototype.startUILoop = function (                       ) {
    if (this.rafId) return;
    var self = this;
    var step = function () {
      if (!self.isOpen) { self.rafId = null; return; }
      self.frameCount++;
      if (self.isPlaying) {
        var b = Math.max(0, self.engine.currentBeat());
        self.playheadBeat = b;
        self.updatePlayline();
        self.updatePosDisplay();
        self.followPlayhead(b);
      }
      // 电平表 ~20fps 节流
      if (self.isPlaying && self.frameCount % 3 === 0) self.updateMeters();
      else if (!self.isPlaying && self.frameCount % 60 === 0) self.decayMeters();
      self.rafId = requestAnimationFrame(step);
    };
    this.rafId = requestAnimationFrame(step);
  };

  /** 播放头自动跟随：指针越过视口右侧 90% 时把视窗推到其前方 15% 处。
      用户主动横滚（滚轮 Shift/deltaX）后挂起 2 秒不抢滚动条 */
  Arrange.prototype.followPlayhead = function (                         beat     ) {
    var sc = this.el.arrTracksScroll;
    if (!sc) return;
    var now = performance.now();
    if (this._followSuspendUntil && now < this._followSuspendUntil) return;
    var contentX = HEADER_W + beat * this.ppb;
    var rel = contentX - sc.scrollLeft;
    if (rel >= sc.clientWidth * 0.9 && contentX > sc.clientWidth * 0.15) {
      sc.scrollLeft = contentX - sc.clientWidth * 0.15;
    }
  };

  Arrange.prototype.stopUILoop = function (                       ) {
    if (this.rafId) { cancelAnimationFrame(this.rafId); this.rafId = null; }
    this.decayMeters();
  };

  Arrange.prototype.updateMeters = function (                       ) {
    for (var i = 0; i < this.tracks.length; i++) {
      var t = this.tracks[i];
      var barEl = this.meterEls[t.id];
      if (!barEl) continue;
      var level = this.engine.trackLevel(t.id);
      barEl.style.transform = "scaleX(" + clamp(level * 1.25, 0, 1) + ")";
    }
  };

  Arrange.prototype.decayMeters = function (                       ) {
    for (var id in this.meterEls) {
      this.meterEls[id] .style.transform = "scaleX(0)";
    }
  };

  /* ═══════════ HUD 提示徽章 ═══════════ */

  Arrange.prototype.showHUD = function (                         text     ) {
    var el = this.el.arrHudBadge;
    if (!el) return;
    el.textContent = text;
    el.classList.remove("hud-pop");
    void el.offsetWidth;
    el.classList.add("visible", "hud-pop");
    var self = this;
    clearTimeout(this.hudTimer);
    this.hudTimer = setTimeout(function () {
      el .classList.remove("visible", "hud-pop");
    }, 1300);
  };

  /* ═══════════ 选择状态 ═══════════ */

  Arrange.prototype.selectOnly = function (                         clipId     ) {
    this.selectedClips = [clipId];
    this.refreshSelectionClasses();
  };

  Arrange.prototype.toggleClipSelect = function (                         clipId     ) {
    var i = this.selectedClips.indexOf(clipId);
    if (i === -1) this.selectedClips.push(clipId);
    else this.selectedClips.splice(i, 1);
    this.refreshSelectionClasses();
  };

  Arrange.prototype.clearSelection = function (                       ) {
    if (!this.selectedClips.length) return;
    this.selectedClips = [];
    this.refreshSelectionClasses();
  };

  Arrange.prototype.selectAllClips = function (                       ) {
    this.selectedClips = [];
    this.tracks.forEach(function (           t) {
      t.clips.forEach(function (           c) {
        this.selectedClips.push(c.id);
      }, this);
    }, this);
    this.refreshSelectionClasses();
    this.showHUD("全选 (" + this.selectedClips.length + ")");
  };

  Arrange.prototype.refreshSelectionClasses = function (                       ) {
    if (!this.el.arrLanes) return;
    var els = this.el.arrLanes.querySelectorAll(".arr-clip");
    for (var i = 0; i < els.length; i++) {
      els[i].classList.toggle("selected", this.selectedClips.indexOf(els[i].dataset.clipId ) !== -1);
    }
  };

  /** ↑↓ 键切换选中轨道 */
  Arrange.prototype.moveTrackSelection = function (                         dir     ) {
    var idx = clamp(this.selectedTrackIdx + dir, 0, this.tracks.length - 1);
    if (idx === this.selectedTrackIdx) return;
    this.selectTrack(idx);
    this.showHUD(this.tracks[idx].name);
  };

  Arrange.prototype.selectTrack = function (                         idx     ) {
    if (isNaN(idx) || !this.tracks[idx]) return;
    this.selectedTrackIdx = idx;
    if (!this.el.arrLanes) return;
    var rows = this.el.arrLanes.querySelectorAll(".arr-lane-row");
    for (var i = 0; i < rows.length; i++) {
      rows[i].classList.toggle("selected-track", Number(rows[i].dataset.trackIdx) === idx);
    }
  };

  /* ═══════════ Clip 编辑操作 ═══════════ */

  Arrange.prototype.deleteSelected = function (                       ) {
    if (!this.selectedClips.length) return;
    /* 多选删除确认：连选多个片段时 Del 一下全删（此前无确认，习惯性
       按 Del 本意删 1 个却全没了）；单片段保持直接删 */
    var ids = this.selectedClips.slice();
    if (ids.length > 1) {
      var self = this;
      this.openConfirm("删除 " + ids.length + " 个片段？", "选中的 " + ids.length + " 个片段将被删除（可用 Ctrl+Z 撤销）", function () {
        self.deleteSelectedSilent(ids);
        self.showHUD("🗑 已删除 " + ids.length + " 个片段");
      });
      return;
    }
    this.pushHistory();
    var ids = this.selectedClips.slice();
    this.tracks.forEach(function (t) {
      t.clips = t.clips.filter(function (c) { return ids.indexOf(c.id) === -1; });
    });
    this.selectedClips = [];
    this.renderTracks();
    this.updateContentWidth();
    this.applyMixSafe();
    this.showHUD("🗑 已删除 " + ids.length + " 个片段");
    this.scheduleSave();
  };

  Arrange.prototype.copySelection = function (                         cut     ) {
    if (!this.selectedClips.length) return;
    if (cut) this.pushHistory();
    var ids = this.selectedClips.slice();
    var minStart = Infinity;
    var entries        = [];
    var self = this;
    /* 记录每个片段的源轨道：粘贴时按相对轨距落轨，跨轨信息不再丢失 */
    this.forEachSelectedClip(function (track     , clip     ) {
      var ti = self.tracks.indexOf(track);
      minStart = Math.min(minStart, clip.start);
      entries.push({ clip: JSON.parse(JSON.stringify(clip)), srcTrack: ti });
    });
    var baseSrc = Infinity;
    entries.forEach(function (en) { baseSrc = Math.min(baseSrc, en.srcTrack); });
    // 相对最早片段起点归零，粘贴时落到播放头
    entries.forEach(function (en) {
      en.clip.start -= minStart;
      delete en.clip._rev; delete en.clip._peaks;
    });
    this.clipboard = { entries: entries, baseSrc: Math.max(0, baseSrc), type: cut ? "cut" : "copy" };
    if (cut) {
      this.deleteSelectedSilent(ids);
      this.showHUD("✂ 已剪切 " + entries.length + " 个片段");
    } else {
      this.showHUD("⧉ 已复制 " + entries.length + " 个片段");
    }
  };

  Arrange.prototype.cutSelection = function (                       ) { this.copySelection(true); };

  Arrange.prototype.deleteSelectedSilent = function (                         ids     ) {
    this.tracks.forEach(function (t) {
      t.clips = t.clips.filter(function (c) { return ids.indexOf(c.id) === -1; });
    });
    this.selectedClips = [];
    this.renderTracks();
    this.updateContentWidth();
    /* 结构变化必须重发调度表：引擎模式的音频剪辑只在播放起点快照 +
       重发时生效（此前剪切/多选删除只改数据，被删的剪辑会继续按旧表发声） */
    this.applyMixSafe();
    this.scheduleSave();
  };

  Arrange.prototype.pasteClipboard = function (                       ) {
    var cb = this.clipboard;
    if (!cb || !cb.entries || !cb.entries.length) {
      this.showHUD("剪贴板为空");
      return;
    }
    this.pushHistory();
    var base = this.snapBeat(this.playheadBeat);
    var targetIdx = clamp(this.selectedTrackIdx, 0, this.tracks.length - 1);
    var self = this;
    cb.entries.forEach(function (en     ) {
      /* 按相对源轨距落轨（FL Playlist 惯例）：跨轨复制的片段回到对应轨；
         轨道不足时收敛到底部可用轨 */
      var ti = clamp(targetIdx + (en.srcTrack - cb .baseSrc), 0, self.tracks.length - 1);
      var copy = JSON.parse(JSON.stringify(en.clip));
      copy.id = uid("clip");
      copy.start = Math.max(0, base + copy.start);
      delete copy._peaks;
      self.tracks[ti].clips.push(copy);
    });
    this.renderTracks();
    this.updateContentWidth();
    this.applyMixSafe();
    this.showHUD("⎘ 已粘贴 " + cb.entries.length + " 个片段 @ " + this.fmtPos(base));
    this.scheduleSave();
  };

  Arrange.prototype.duplicateSelection = function (                       ) {
    if (!this.selectedClips.length) return;
    this.pushHistory();
    var newIds        = [];
    this.forEachSelectedClip(function (track     , clip     ) {
      var copy = JSON.parse(JSON.stringify(clip));
      copy.id = uid("clip");
      delete copy._peaks;
      track.clips.push(copy);
      newIds.push(copy.id);
    });
    this.selectedClips = newIds;
    this.renderTracks();
    this.updateContentWidth();
    this.applyMixSafe();   // 克隆的音频剪辑需重发调度表才会发声
    this.showHUD("⧉ 原地克隆 (" + newIds.length + ")");
    this.scheduleSave();
  };

  /** Ctrl+B：向右顺延复制（每个片段克隆到其原位置右侧一个长度处） */
  Arrange.prototype.repeatRight = function (                       ) {
    if (!this.selectedClips.length) return;
    this.pushHistory();
    var newIds        = [];
    this.forEachSelectedClip(function (track     , clip     ) {
      var copy = JSON.parse(JSON.stringify(clip));
      copy.id = uid("clip");
      copy.start = clip.start + clip.length;
      delete copy._peaks;
      track.clips.push(copy);
      newIds.push(copy.id);
    });
    this.selectedClips = newIds;
    this.renderTracks();
    this.updateContentWidth();
    this.applyMixSafe();   // 顺延复制的新剪辑同样需要重发才会发声
    this.showHUD("⇥ 向右顺延复制");
    this.scheduleSave();
  };

  /** 片段内相对拍 rel 是否可分割（两侧至少留 MIN_CLIP_LEN） */
  Arrange.prototype.canSplitRel = function (                         clip     , rel     ) {
    return rel > MIN_CLIP_LEN && rel < clip.length - MIN_CLIP_LEN;
  };

  /** 在绝对拍 atBeat 处把 track 上第 i 个 clip 切成两段。
      audio：右半 offset 前移；midi：跨界音符左右各截一段。
      成功返回 true（含 pushHistory 与视图/引擎同步） */
  Arrange.prototype.splitClipAtBeat = function (                         track     , clip     , atBeat     ) {
    var rel = atBeat - clip.start;
    if (!this.canSplitRel(clip, rel)) return false;

    this.pushHistory();
    var right = JSON.parse(JSON.stringify(clip));
    delete right._peaks; delete right._rev;
    right.id = uid("clip");
    right.start = clip.start + rel;
    right.length = clip.length - rel;
    right._rev = 0;
    clip.length = rel;

    if (clip.type === "midi") {
      var leftNotes        = [], rightNotes        = [];
      (clip.notes || []).forEach(function (n     ) {
        if (n.end <= rel) { leftNotes.push(n); return; }
        if (n.start >= rel) { rightNotes.push({ note: n.note, start: n.start - rel, end: n.end - rel, velocity: n.velocity }); return; }
        /* 跨界音符：左侧截尾，右侧截头 */
        leftNotes.push({ note: n.note, start: n.start, end: rel, velocity: n.velocity });
        rightNotes.push({ note: n.note, start: 0, end: n.end - rel, velocity: n.velocity });
      });
      clip.notes = leftNotes;
      right.notes = rightNotes;
    } else if (right.src) {
      right.offset = Math.max(0, (right.offset || 0) + rel * this.secondsPerBeat());
    }

    track.clips.push(right);   // 追加到同轨末尾；渲染按 start 定位
    this.renderTracks();
    this.updateContentWidth();
    this.applyMixSafe();
    this.scheduleSave();
    return true;
  };

  /** 右键菜单入口：在点击拍点拆分单个片段 */
  Arrange.prototype.splitClipAtCursor = function (                         track     , clip     , beatAtCursor     ) {
    if (!track || !clip) return false;
    /* 落点吸附到网格再尝试；太贴边时退回未吸附值 */
    var snapped = this.snapBeat(Math.max(0, beatAtCursor));
    if (this.splitClipAtBeat(track, clip, snapped)) return true;
    if (this.splitClipAtBeat(track, clip, Math.max(0, beatAtCursor))) return true;
    this.showHUD("⚠ 拆分点太靠近边缘");
    return false;
  };

  /** S 键入口：在播放头处拆分所有被选中的片段 */
  Arrange.prototype.splitSelectedAtPlayhead = function (                       ) {
    if (!this.selectedClips.length) { this.showHUD("先选中要拆分的片段"); return; }
    var playhead = this.playheadBeat;
    var hits = 0, misses = 0;
    var self = this;
    this.forEachSelectedClip(function (track     , clip     ) {
      var rel = playhead - clip.start;
      if (self.canSplitRel(clip, rel)) {
        if (self.splitClipAtBeat(track, clip, playhead)) hits++;
      } else {
        misses++;
      }
    });
    if (hits) this.showHUD("✂ 已在播放头拆分 " + hits + " 个片段" + (misses ? "（" + misses + " 个不可拆）" : ""));
    else this.showHUD("播放头不在所选片段内部，无法拆分");
  };

  /* ═══════════ 轨道操作 ═══════════ */

  Arrange.prototype.addTrack = function (                       ) {
    // 引擎模式：32 轨上限且 29-31 为试听/钢琴窗专用轨（见 engine_bridge.js
    // 轨位表），编曲轨上限 0..28；webaudio 模式无轨位预留约束（32 轨上限）
    var engMode = window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine();
    var limit = engMode ? 29 : 32;
    if (this.tracks.length >= limit) {
      this.showHUD(engMode
        ? "✗ 最多支持 29 条轨道（引擎轨位上限：29-31 为试听/钢琴窗专用）"
        : "✗ 最多支持 32 条轨道（WebAudio 模式上限）");
      return;
    }
    this.pushHistory();
    this.tracks.push(this.makeTrack(this.tracks.length + 1));
    this.applyMixSafe();
    this.renderAll();
    this.selectTrack(this.tracks.length - 1);
    this.showHUD("＋ 新建轨道: " + this.tracks[this.tracks.length - 1].name);
    this.scheduleSave();
  };

  Arrange.prototype.removeTrack = function (                         idx     ) {
    if (!this.tracks[idx] || this.tracks.length <= 1) {
      this.showHUD("至少保留一条轨道");
      return;
    }
    var self = this;
    this.openConfirm("删除轨道", "确定删除轨道「" + this.tracks[idx].name + "」及其全部剪辑？", function () {
      self.pushHistory();
      var removed = self.tracks.splice(idx, 1)[0];
      // 先停挂在该轨上的音频源（此前只断 gain，BufferSource 仍解码播放至自然结束）
      self.engine.stopTrackSources(removed.id);
      var nodes = self.engine.trackNodes[removed.id];
      if (nodes) {
        if (nodes.synth) nodes.synth.stopAll();
        if (nodes.soundfont) nodes.soundfont.stopAll();
        try { nodes.gain .disconnect(); nodes.analyser .disconnect(); } catch (e) {}
        delete self.engine.trackNodes[removed.id];
      }
      self.selectedTrackIdx = clamp(self.selectedTrackIdx, 0, self.tracks.length - 1);
      self.selectedClips = [];
      self.applyMixSafe();
      self.renderAll();
      self.showHUD("🗑 已删除轨道");
      self.scheduleSave();
    });
  };

  Arrange.prototype.duplicateTrack = function (                         idx     ) {
    var src = this.tracks[idx];
    if (!src) return;
    this.pushHistory();
    var copy = JSON.parse(JSON.stringify(src));
    copy.id = uid("tr");
    copy.name = src.name + " 副本";
    copy.clips.forEach(function (c     ) { c.id = uid("clip"); delete c._peaks; });
    this.tracks.splice(idx + 1, 0, copy);
    this.applyMixSafe();
    this.renderAll();
    this.showHUD("⧉ 已复制轨道");
    this.scheduleSave();
  };

  Arrange.prototype.moveTrack = function (                         idx     , dir     ) {
    var to = idx + dir;
    if (!this.tracks[idx] || !this.tracks[to]) return;
    this.pushHistory();
    var tmp = this.tracks[idx];
    this.tracks[idx] = this.tracks[to];
    this.tracks[to] = tmp;
    this.selectedTrackIdx = to;
    this.applyMixSafe();
    this.renderAll();
    this.scheduleSave();
  };

  Arrange.prototype.clearTrackClips = function (                         idx     ) {
    var track = this.tracks[idx];
    if (!track || !track.clips.length) return;
    var self = this;
    this.openConfirm("清空剪辑", "确定清空轨道「" + track.name + "」的全部剪辑？", function () {
      self.pushHistory();
      track.clips = [];
      self.renderTracks();
      self.updateContentWidth();
      self.applyMixSafe();   // 播放中清空：重发调度表，否则旧表继续发声
      self.scheduleSave();
    });
  };

  /** 定点刷新轨道头按钮态（mute/solo 切换不再全量重建轨道区 DOM） */
  Arrange.prototype.refreshTrackHeadStates = function (                       ) {
    if (!this.el.arrLanes) return;
    var rows = this.el.arrLanes.querySelectorAll(".arr-lane-row");
    for (var i = 0; i < rows.length && i < this.tracks.length; i++) {
      var head = rows[i].querySelector(".arr-track-head");
      if (!head) continue;
      var mBtn = head.querySelector(".arr-th-btn:nth-of-type(1)");
      var sBtn = head.querySelector(".arr-th-btn:nth-of-type(2)");
      if (mBtn) mBtn.classList.toggle("mute-on", !!this.tracks[i].mute);
      if (sBtn) sBtn.classList.toggle("solo-on", !!this.tracks[i].solo);
    }
  };

  Arrange.prototype.toggleMute = function (                         idx     ) {
    var track = this.tracks[idx];
    if (!track) return;
    track.mute = !track.mute;
    this.applyMixSafe();
    this.refreshTrackHeadStates();
    this.showHUD(track.name + ": " + (track.mute ? "静音" : "取消静音") + " (M)");
    this.scheduleSave();
  };

  Arrange.prototype.toggleSolo = function (                         idx     ) {
    var track = this.tracks[idx];
    if (!track) return;
    track.solo = !track.solo;
    this.applyMixSafe();
    this.refreshTrackHeadStates();
    this.showHUD(track.name + ": " + (track.solo ? "独奏" : "取消独奏") + " (S)");
    this.scheduleSave();
  };

  /* ═══════════ 右键菜单体系 ═══════════ */

  Arrange.prototype.closeMenu = function (                       ) {
    if (this.menuEl) {
      this.menuEl.remove();
      this.menuEl = null;
    }
    if (this.menuOutsideHandler) {
      document.removeEventListener("mousedown", this.menuOutsideHandler, true);
      this.menuOutsideHandler = null;
    }
  };

  /** 在 (x,y) 打开右键菜单；items: "-" 分隔 | {heading} | {label, danger, disabled, iconHtml, action} */
  Arrange.prototype.showMenu = function (                         items     , x     , y     ) {
    this.closeMenu();
    var self = this;
    var menu = document.createElement("div");
    menu.className = "ctx-menu menu-in";

    items.forEach(function (a     ) {
      if (a === "-") {
        var sep = document.createElement("div");
        sep.className = "ctx-sep";
        menu.appendChild(sep);
        return;
      }
      if (a.heading) {
        var h = document.createElement("div");
        h.className = "menu-heading";
        h.style.cssText = "padding:6px 14px 2px;font-size:10px;color:var(--color-ink-faint);font-family:'Cascadia Mono',Consolas,monospace";
        h.textContent = a.heading;
        menu.appendChild(h);
        return;
      }
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "ctx-item" + (a.danger ? " danger" : "");
      btn.disabled = !!a.disabled;
      if (a.disabled) btn.style.opacity = "0.45";
      if (a.iconHtml) {
        var icon = document.createElement("span");
        icon.innerHTML = a.iconHtml;
        btn.appendChild(icon);
      }
      btn.appendChild(document.createTextNode(a.label));
      if (!a.disabled) {
        btn.addEventListener("click", function () {
          self.closeMenu();
          a.action();
        });
      }
      menu.appendChild(btn);
    });

    document.body.appendChild(menu);
    this.menuEl = menu;
    var w = menu.offsetWidth, h2 = menu.offsetHeight;
    menu.style.left = Math.max(8, Math.min(x, window.innerWidth - w - 8)) + "px";
    menu.style.top = Math.max(8, Math.min(y, window.innerHeight - h2 - 8)) + "px";

    this.menuOutsideHandler = function (ev     ) {
      if (menu && !menu.contains(ev.target        )) self.closeMenu();
    };
    setTimeout(function () {
      document.addEventListener("mousedown", self.menuOutsideHandler , true);
    }, 0);
  };

  /** rackItemOfTrack 轨道音源引用的机架条目（没有则 null） */
  function rackItemOfTrack(ctrl     , track     )      {
    if (!track || !track.source || !track.source.itemId) return null;
    var items = (ctrl.rack && ctrl.rack.items) || [];
    for (var i = 0; i < items.length; i++) {
      if (items[i] && items[i].id === track.source.itemId) return items[i];
    }
    return null;
  }

  /** trackIsPluginSource 该轨音源是否为插件（VST3 / MM）——决定参数面板入口是否可用 */
  function trackIsPluginSource(ctrl     , track     )          {
    var item = rackItemOfTrack(ctrl, track);
    return !!item && (item.kind === "vst3" || item.kind === "mm");
  }

  /** 轨道头右键菜单 */
  Arrange.prototype.openTrackMenu = function (                         e     , idx     ) {
    var self = this;
    var track = this.tracks[idx];
    if (!track) return;
    var items        = [
      { label: "✎ 重命名 (F2)", action: function () { self.openRenameModal(idx); } },
      { label: "🎨 更换颜色 ▸", action: function () { self.openColorMenu(e.clientX, e.clientY, idx); } },
      { label: "🎹 加载音源 ▸", action: function () { self.openSourceMenu(track, e.clientX, e.clientY); } },
      {
        /* P5 §4.6：轨道乐器（VST3 / MM）的参数面板入口。
           非插件音色（合成波/SF2/采样）没有参数面板，菜单项置灰并说明原因。 */
        label: "🎛 插件参数面板…",
        disabled: !trackIsPluginSource(self, track),
        action: function () {
          var item = rackItemOfTrack(self, track);
          if (window.PluginPanel) {
            window.PluginPanel.open(self, {
              track: idx, slot: -1, master: false,
              ref: (item && (item.path || item.sourceId)) || "",
              sourceId: (item && item.sourceId) || "",
              format: (item && item.kind) || "",
              name: (item && item.name) || "插件乐器"
            });
          }
        }
      },
      "-",
      { label: "⧉ 复制轨道", action: function () { self.duplicateTrack(idx); } },
      { label: "↑ 上移轨道", disabled: idx === 0, action: function () { self.moveTrack(idx, -1); } },
      { label: "↓ 下移轨道", disabled: idx >= this.tracks.length - 1, action: function () { self.moveTrack(idx, 1); } },
      { label: "⌫ 清空剪辑", action: function () { self.clearTrackClips(idx); } },
      "-",
      { label: "🗑 删除轨道", danger: true, action: function () { self.removeTrack(idx); } }
    ];
    this.showMenu(items, e.clientX, e.clientY);
  };

  /** 轨道头空白处右键：新建轨道 */
  Arrange.prototype.openBlankMenu = function (                         e     ) {
    var self = this;
    var items        = [
      { label: "＋ 新建轨道", action: function () { self.addTrack(); } }
    ];
    if (this.clipboard) {
      items.push("-");
      items.push({ label: "⎘ 粘贴", action: function () { self.pasteClipboard(); } });
    }
    this.showMenu(items, e.clientX, e.clientY);
  };

  /** Clip 右键菜单 */
  Arrange.prototype.openClipMenu = function (                         e     , clipEl     ) {
    var self = this;
    var found = this.locateClip(clipEl);
    if (!found) return;
    var clip = found.clip;
    var beatAtCursor = this.clientXToBeat(e.clientX);
    var relAtCursor = beatAtCursor - clip.start;
    this.showMenu([
      { label: "✂ 在此拆分 (S)", disabled: !this.canSplitRel(clip, Math.max(0, relAtCursor)), action: function () {
        self.splitClipAtCursor(found .track, clip, beatAtCursor);
      } },
      "-",
      { label: clip.mute ? "🔇 取消片段静音" : "🔇 片段静音", action: function () {
        self.pushHistory();
        clip.mute = !clip.mute;
        self.refreshClipEl(clipEl, clip);
        self.applyMixSafe();   // 静音状态属调度表内容：不重发则音频剪辑照响
        self.scheduleSave();
      } },
      { label: "⤺ 清除渐变", disabled: clip.type !== "audio" || (!clip.fadeIn && !clip.fadeOut), action: function () {
        self.pushHistory();
        clip.fadeIn = 0; clip.fadeOut = 0;
        clip._rev = (clip._rev || 0) + 1;
        self.refreshClipEl(clipEl, clip);
        self.scheduleSave();
      } },
      "-",
      { label: "✂ 剪切 (Ctrl+X)", action: function () { self.cutSelection(); } },
      { label: "⧉ 复制 (Ctrl+C)", action: function () { self.copySelection(false); } },
      { label: "⧉ 原地克隆 (Ctrl+D)", action: function () { self.duplicateSelection(); } },
      { label: "⇥ 向右顺延 (Ctrl+B)", action: function () { self.repeatRight(); } },
      "-",
      { label: "🗑 删除 (Del)", danger: true, action: function () { self.deleteSelected(); } }
    ], e.clientX, e.clientY);
  };

  /** 轨道音源菜单（P2 起改为机架选择器）：
      轨道能选的音色只能来自项目机架（arrangement.rack.items），
      选择器三处共用（机架 ＋ / 轨道头 / 钢琴窗），实现在 rack.ts。 */
  Arrange.prototype.openSourceMenu = function (                         track     , x     , y     ) {
    var self = this;
    var idx = this.tracks.indexOf(track);
    window.ArrRack.openPicker({
      title: "轨道音源 · " + track.name,
      currentTrack: idx,
      items: function () { return self.rack.items; },
      ensureItem: function (sourceId        , presetId        , name        ) {
        return self.ensureRackItem(sourceId, presetId, name);
      },
      onPick: function (item     , presetId        ) {
        self.assignRackItem(item, idx, presetId);
      },
      onPreview: function (item     , presetId        , btn     ) {
        self.previewRackItem(item, btn, presetId);
      }
    });
    if (x !== undefined && y !== undefined) {
      /* 选择器是模态：以轨道头按钮位置为锚点做一次落位提示（不改模态居中） */
      var anchor = document.getElementById("arrRackWindow");
      if (anchor) anchor.dataset.lastAnchor = x + "," + y;
    }
  };

  Arrange.prototype._srcDot = function (                         color     ) {
    return '<span class="arr-ctx-swatch" style="background:' + color + '"></span>';
  };

  /** setTrackSource 把轨道指向一条机架条目（v2 唯一入口）。
      source 形如 {itemId, presetId}；旧格式调用（type/wave/...）会被就地迁移
      出机架条目——历史上这两条路径都由这里进，保持兼容。 */
  Arrange.prototype.setTrackSource = function (                         track     , source     ) {
    var ArrRack = window.ArrRack;
    var ref = source || {};
    if (!ref.itemId) {
      var migrated = ArrRack.migrateV1Source(ref);
      if (!migrated) {
        this.showHUD("✗ 无法识别的音源（音源只能来自机架）");
        return;
      }
      ArrRack.upsert(this.rack.items, migrated);
      ref = { itemId: migrated.id, presetId: migrated.presetId || "" };
    }
    if (!ArrRack.find(this.rack.items, ref.itemId)) {
      this.showHUD("✗ 音源不在机架中，请先从机架选择音源");
      return;
    }
    this.pushHistory();
    track.source = { itemId: String(ref.itemId), presetId: String(ref.presetId || "") };
    // 触发引擎重建该轨发声链
    var nodes = this.engine.trackNodes[track.id];
    if (nodes) nodes.sourceKey = null;
    this.engine.ensureTrack(track);
    this.applyMixSafe();
    this.renderTracks();
    this.showHUD("♪ 音源: " + this.sourceBadgeText(track).replace("♪ ", ""));
    this.scheduleSave();
  };

  /* ═══════════ 机架写操作（经 ops API，与 AI 工具共用同一实现） ═══════════
     POST /api/projects/{id}/arrangement/ops：服务端在项目级写锁内读-改-写，
     返回改动后的完整编排数据。前端只取被改动的那几块热应用（applyRemote-
     Arrangement），本地未保存的 clip 编辑不会被服务端快照覆盖。
     这些操作走服务端而不是本地改内存的原因：AI 也在改同一份机架，
     本地内存里的机架可能已经过期（AI 刚加的条目会被下一次整份 PUT 抹掉）。 */

  /** setPianoRollSource 钢琴窗音源变化（PianoRoll.setRackVoice 调用）：
      更新状态并落盘到 arrangement.pianoRoll（跨会话保持）。 */
  Arrange.prototype.setPianoRollSource = function (                         itemId     , presetId     ) {
    if (!itemId) return;
    var prev = this.pianoRollSource || { itemId: "", presetId: "" };
    var next = { itemId: String(itemId), presetId: String(presetId || "") };
    var changed = prev.itemId !== next.itemId || String(prev.presetId || "") !== next.presetId;
    this.pianoRollSource = next;
    if (!changed) return;
    var self = this, project = this.projectId, epoch = this.projectEpoch;
    this.arrangeOps([{ op: "setPianoRoll", itemId: next.itemId, presetId: next.presetId }])
      .then(function () { if (self.projectId === project && self.projectEpoch === epoch) self.scheduleSave(); })
      .catch(function (err     ) {
        if (self.projectId !== project || self.projectEpoch !== epoch) return;
        UI.toast ("✗ 钢琴窗音源未能保存: " + (err && err.message ? err.message : "未知错误"), "err");
      });
  };

  /** arrangeOps 发送定向修改；返回 Promise<{rev, summary, changed}> */
  Arrange.prototype.arrangeOps = function (                         ops       ) {
    var self = this;
    if (!this.projectId) return Promise.reject(new Error("未打开项目"));
    var project=this.projectId, epoch=this.projectEpoch;
    ops=ops.map((op)=>{var captured=window.ArrangementSync.copy(op);if(typeof captured.track==="number" && captured.track>=0 && this.tracks[captured.track])captured.track=this.tracks[captured.track].id;return captured;});
    return this.doSave().then(function flush()      {
      if(self.projectId!==project || self.projectEpoch!==epoch)throw new Error("项目已切换");
      if(self.loadedProjectId!==project)throw new Error("工程正在加载，请稍后重试");
      if(self.saveConflict)throw new Error("请先处理保存冲突");
      if(self._saves[project].lastError)throw self._saves[project].lastError;
      if(self.dirty)return self.doSave().then(flush);
      return UI.postJSON     ("/api/projects/" + encodeURIComponent(project) + "/arrangement/ops", {
      ops: ops,
      client: "ui"
    }); }).then(function (res     ) {
      if(self.projectId!==project || self.projectEpoch!==epoch)return res;
      if (res && res.arrangement) self.applyRemoteArrangement(res.arrangement, res.changed || []);
      if (res && typeof res.rev === "number") self.rev = res.rev;
      return res || {};
    });
  };

  /** ensureRackItem 把 sourceId 入架（已在架复用）并返回条目 */
  Arrange.prototype.ensureRackItem = function (                         sourceId     , presetId     , name     ) {
    var self = this;
    var project=this.projectId,epoch=this.projectEpoch;
    var local = window.ArrRack.itemFromSourceId(sourceId, presetId, name);
    return this.arrangeOps([{
      op: "addRackItem",
      sourceId: sourceId,
      presetId: presetId || "",
      name: name || ""
    }]).then(function (res     ) {
      if(self.projectId!==project || self.projectEpoch!==epoch)return null;
      if (res && res.summary && res.summary.length) self.showHUD("🎛 " + res.summary[0]);
      var item = window.ArrRack.find(self.rack.items, local ? local.id : "");
      // 服务端 id 与前端派生 id 同算法；万一漂移（老数据/改名）按 sourceId 兜底查找
      if (!item) {
        var items = self.rack.items;
        for (var i = 0; i < items.length; i++) {
          if (items[i].sourceId === sourceId &&
              String(items[i].presetId || "") === String(presetId || "")) { item = items[i]; break; }
        }
      }
      return item || local;
    }).catch(function (err     ) {
      if(self.projectId!==project || self.projectEpoch!==epoch)return null;
      UI.toast ("✗ 加入机架失败: " + (err && err.message ? err.message : "未知错误"), "err");
      return null;
    });
  };

  /** assignRackItem 把机架条目分配给某条轨道（不传 trackIdx 用当前选中轨） */
  Arrange.prototype.assignRackItem = function (                         item     , trackIdx     , presetId     ) {
    if (!item) return;
    /* 采样包/插件条目不能直接当轨道音源：说清原因而不是制造一条无声轨
       （采样包在素材库中挑文件；VST/MM 插件 P5/P6 接入） */
    var probe = window.ArrRack.resolve(this.rack.items, {
      itemId: item.id,
      presetId: presetId === undefined || presetId === null || presetId === "" ? (item.presetId || "") : presetId
    });
    if (probe.voice.type === "pack") {
      UI.toast ("⚠ 采样包不能直接分配给轨道：请在素材库中挑选采样文件（P3 接入）", "warn");
      return;
    }
    var idx = (trackIdx === undefined || trackIdx === null) ? this.selectedTrackIdx : Number(trackIdx);
    var track = this.tracks[idx];
    if (!track) {
      // 拖到空白区：新建一条轨道并挂上该音源
      this.pushHistory();
      track = this.makeTrack(this.tracks.length + 1);
      track.source = { itemId: item.id, presetId: presetId || item.presetId || "" };
      track.name = window.ArrRack.kindLabel(item.kind) + " " + (this.tracks.length + 1);
      this.tracks.push(track);
      this.engine.ensureTrack(track);
      this.applyMixSafe();
      this.renderTracks();
      this.notifyRackChanged();
      this.scheduleSave();
      this.showHUD("➕ 新建轨道并设置音源: " + (item.name || "音源"));
      return;
    }
    var preset = presetId === undefined || presetId === null || presetId === ""
      ? String(item.presetId || "")
      : String(presetId);
    this.pushHistory();
    track.source = { itemId: item.id, presetId: preset };
    var self = this;
    var project=this.projectId,epoch=this.projectEpoch;
    this.arrangeOps([{ op: "setTrackSource", track: track.id, itemId: item.id, presetId: preset }])
      .then(function (res     ) {
        if(self.projectId!==project || self.projectEpoch!==epoch)return;
        if (res && res.summary && res.summary.length) self.showHUD("🎛 " + res.summary[0]);
      })
      .catch(function (err     ) {
        if(self.projectId!==project || self.projectEpoch!==epoch)return;
        /* 服务端拒绝（条目被 AI 删了等）：本地回滚并明确提示 */
        self.undo();
        UI.toast ("✗ 分配音源失败: " + (err && err.message ? err.message : "未知错误"), "err");
        return;
      });
    var nodes = this.engine.trackNodes[track.id];
    if (nodes) nodes.sourceKey = null;
    this.engine.ensureTrack(track);
    this.applyMixSafe();
    this.renderTracks();
    this.notifyRackChanged();
    this.scheduleSave();
  };

  /** removeRackItem 移出机架（被轨道占用时服务端拒绝并给出占用清单） */
  Arrange.prototype.removeRackItem = function (                         itemId     , force      ) {
    var self = this;
    var project=this.projectId,epoch=this.projectEpoch;
    var item = window.ArrRack.find(this.rack.items, itemId);
    var name = item ? item.name : itemId;
    this.pushHistory();
    return this.arrangeOps([{ op: "removeRackItem", itemId: itemId, force: !!force }])
      .then(function (res     ) {
        if(self.projectId!==project || self.projectEpoch!==epoch)return;
        self.showHUD("🎛 已移出机架: " + name);
        self.renderTracks();
      })
      .catch(function (err     ) {
        if(self.projectId!==project || self.projectEpoch!==epoch)return;
        self.undo();   // 服务端拒绝：撤销本次快照，避免留下无用历史
        UI.toast ("✗ 移出机架失败: " + (err && err.message ? err.message : "未知错误"), "err");
      });
  };

  /** applyRemoteArrangement 把服务端返回/推送的编排数据热应用。
      只覆盖 changed 里点名的部分：本地未保存的 clip 编辑（拖动/新增音符）
      不会因为一次"AI 改音源"而丢失。 */
  Arrange.prototype.applyRemoteArrangement = function (                         data     , changed     ) {
    if (!data || typeof data !== "object") return;
    var record=this._saves[this.projectId ];
    if(record && record.base) {
      if(Number(data.rev)<Number(record.base.rev))return;
      var local=this.serialize(), base=this.saveConflict?this.saveConflict.base:record.base;
      var merged=window.ArrangementSync.merge(base,local,data);
      record.base=window.ArrangementSync.copy(data);
      this._applyMergedState(merged.value);
      this.rev=Number(data.rev)||0;
      if(merged.conflicts.length)this._pauseForConflict(base,local,data,merged.conflicts);
      else if(!window.ArrangementSync.equal(merged.value,data))this.scheduleSave();
      this.notifyRackChanged();
      if(window.PluginPanel)window.PluginPanel.restoreAll(this);
      return;
    }
    var ArrRack = window.ArrRack;
    var kinds                           = {};
    (changed || []).forEach(function (k     ) { kinds[String(k)] = true; });
    var all = !changed || !changed.length;
    var tracksTouched = false;

    if (all || kinds["rack"]) {
      if (data.rack && Array.isArray(data.rack.items)) {
        this.rack = { items: data.rack.items.map(function (it     ) { return ArrRack.cloneItem(it); }) };
      }
    }
    if (all || kinds["tracks"]) {
      var byId                        = {};
      (data.tracks || []).forEach(function (t     ) { if (t && t.id) byId[t.id] = t; });
      // 以服务端为准增删轨道（保留本地 clips：服务端的 clips 可能落后于本地编辑）
      var next        = [];
      var self0 = this;
      (data.tracks || []).forEach(function (t     , i        ) {
        var local = null;
        for (var j = 0; j < self0.tracks.length; j++) {
          if (self0.tracks[j].id === t.id) { local = self0.tracks[j]; break; }
        }
        if (local) {
          local.name = t.name !== undefined ? t.name : local.name;
          local.color = t.color || local.color;
          local.mute = !!t.mute;
          local.solo = !!t.solo;
          local.volume = t.volume !== undefined ? Number(t.volume) : local.volume;
          if (t.source) {
            local.source = {
              itemId: String(t.source.itemId || ""),
              presetId: String(t.source.presetId || "")
            };
          }
          next.push(local);
        } else {
          var fresh = self0.makeTrack(i + 1);
          fresh.id = t.id || fresh.id;
          fresh.name = t.name || fresh.name;
          fresh.color = t.color || fresh.color;
          fresh.mute = !!t.mute;
          fresh.solo = !!t.solo;
          fresh.volume = t.volume !== undefined ? Number(t.volume) : 0.8;
          fresh.source = t.source
            ? { itemId: String(t.source.itemId || ""), presetId: String(t.source.presetId || "") }
            : { itemId: "", presetId: "" };
          fresh.clips = [];
          next.push(fresh);
        }
      }, this);
      this.tracks = next;
      if (!this.tracks.length) this.tracks = [this.makeTrack(1)];
      tracksTouched = true;
    }
    if (all || kinds["pianoRoll"]) {
      if (data.pianoRoll && typeof data.pianoRoll === "object") {
        this.pianoRollSource = {
          itemId: String(data.pianoRoll.itemId || ""),
          presetId: String(data.pianoRoll.presetId || "")
        };
      }
    }
    if (all || kinds["mixer"]) {
      if (data.mixer && data.mixer.channels) this.mixer = { channels: data.mixer.channels, master: data.mixer.master };
    }
    if (all || kinds["transport"]) {
      if (data.bpm !== undefined) {
        this.bpm = clamp(Number(data.bpm) || 120, 30, 300);
        this.engine.bpm = this.bpm;
        window.EngineBridge.setTempo(this.bpm);
      }
      if (data.loop) {
        this.loop = {
          on: !!data.loop.on,
          start: Number(data.loop.start) || 0,
          end: Number(data.loop.end) || 16
        };
        this.engine.loop = this.loop;
        window.EngineBridge.setLoop(this.loop.on, this.loop.start, this.loop.end).catch(function () {});
      }
      this.syncTransportUI();
    }

    this.normalizeRack();
    if (tracksTouched) {
      var selfT = this;
      this._pruneTrackNodes();
      this.tracks.forEach(function (t     ) { selfT.engine.ensureTrack(t); });
      this.renderTracks();
      this.updateContentWidth();
      this.applyMixSafe();
    }
    this.notifyRackChanged();
    /* 落盘：把 AI 的改动并入本地状态后立即回写（若此刻有一次整份 PUT 在途，
       它写的是"改动前"的快照，这次回写保证服务端最终与界面一致） */
    this.scheduleSave();
    this.setSaveStamp("AI 已同步", false);
  };

  /** 轨道颜色菜单 */
  Arrange.prototype.openColorMenu = function (                         x     , y     , idx     ) {
    var self = this;
    var track = this.tracks[idx];
    if (!track) return;
    var items        = [{ heading: "🎨 轨道颜色 · " + track.name }];
    TRACK_COLORS.forEach(function (c) {
      items.push({
        label: c.toUpperCase(),
        iconHtml: '<span class="arr-ctx-swatch" style="background:' + c + '"></span>',
        action: function () {
          self.pushHistory();
          track.color = c;
          self.renderTracks();
          self.scheduleSave();
        }
      });
    });
    this.showMenu(items, x, y);
  };

  /* ═══════════ 拖放：来源高亮与落点处理 ═══════════ */

  Arrange.prototype.isArrangeDrag = function (                         e     ) {
    var types = e.dataTransfer && e.dataTransfer.types;
    if (!types) return false;
    for (var i = 0; i < types.length; i++) {
      if (types[i] === "text/plain" || types[i] === "application/x-arrange" || types[i] === "application/x-arrange-source") return true;
    }
    return false;
  };

  /** 本次拖拽是否为机架音源（替换轨道音源），而非 MIDI/音频素材入库 */
  Arrange.prototype.isSourceDrag = function (                         e     ) {
    var types = e.dataTransfer && e.dataTransfer.types;
    if (!types) return false;
    for (var i = 0; i < types.length; i++) {
      if (types[i] === "application/x-arrange-source") return true;
    }
    return false;
  };

  /** 音源拖拽反馈：高亮悬停轨道整行（行 = 头 + 轨道内容），示意「替换该轨音源」 */
  Arrange.prototype.updateSourceHighlight = function (                         e     ) {
    var row = null;
    if (e.target && e.target.closest) {
      var headEl = e.target.closest (".arr-track-head");
      var contentEl = headEl ? null : e.target.closest (".arr-lane-content");
      if (headEl) row = headEl.closest (".arr-lane-row");
      else if (contentEl) row = contentEl.closest (".arr-lane-row");
    }
    if (this.hoverSourceEl && this.hoverSourceEl !== row) {
      this.hoverSourceEl.classList.remove("drag-over-source");
      this.hoverSourceEl = null;
    }
    if (row && row !== this.hoverSourceEl) {
      row.classList.add("drag-over-source");
      this.hoverSourceEl = row;
    }
  };

  Arrange.prototype.clearSourceHighlight = function (                       ) {
    if (this.hoverSourceEl) {
      this.hoverSourceEl.classList.remove("drag-over-source");
      this.hoverSourceEl = null;
    }
  };

  Arrange.prototype.updateDropHighlight = function (                         e     ) {
    var target = e.target.closest
      ? (e.target.closest (".arr-track-head") || e.target.closest (".arr-lane-content"))
      : null;
    if (this.hoverDropEl && this.hoverDropEl !== target) {
      this.hoverDropEl.classList.remove("drop-target");
      this.hoverDropEl = null;
    }
    if (target && target !== this.hoverDropEl) {
      target.classList.add("drop-target");
      this.hoverDropEl = target;
    }
  };

  Arrange.prototype.clearDropHighlight = function (                       ) {
    if (this.hoverDropEl) {
      this.hoverDropEl.classList.remove("drop-target");
      this.hoverDropEl = null;
    }
    this.hideDropGhost();
  };

  /**
   * 统一落点解析（dragover 剪影与 drop 放置共用同一份计算，
   * 保证"剪影 = 真实放置位置"）：
   * - 轨道头 → 播放头位置（选中该轨道）
   * - 内容区 → 鼠标 x 的吸附位置
   * - 其他区域 → 选中轨道 + 播放头位置
   */
  Arrange.prototype.resolveDropTarget = function (                         e     ) {
    var headEl = e.target && e.target.closest ? e.target.closest (".arr-track-head") : null;
    var contentEl = e.target && e.target.closest ? e.target.closest (".arr-lane-content") : null;
    var trackIdx;
    var startBeat;
    var zone;
    if (headEl) {
      trackIdx = Number(headEl.dataset.trackIdx);
      startBeat = this.snapBeat(Math.max(0, this.playheadBeat));
      zone = "head";
    } else if (contentEl) {
      trackIdx = Number(contentEl.dataset.trackIdx);
      startBeat = this.snapBeat(Math.max(0, this.clientXToBeat(e.clientX)));
      zone = "content";
    } else {
      trackIdx = this.selectedTrackIdx;
      startBeat = this.snapBeat(Math.max(0, this.playheadBeat));
      zone = "fallback";
    }
    if (isNaN(trackIdx) || !this.tracks[trackIdx]) trackIdx = 0;
    return { trackIdx: trackIdx, startBeat: Math.max(0, startBeat), zone: zone };
  };

  /** 拖放剪影：半透明块指示 clip 即将出现的位置与轨道 */
  Arrange.prototype.updateDropGhost = function (                         target     ) {
    var content = this.el.arrLanes
      ? this.el.arrLanes.querySelector('.arr-lane-content[data-track-idx="' + target.trackIdx + '"]')
      : null;
    if (!content) { this.hideDropGhost(); return; }

    var ghost = this.dropGhostEl;
    if (!ghost || !ghost.parentNode) {
      ghost = document.createElement("div");
      ghost.className = "arr-drop-ghost";
      ghost.innerHTML = '<span class="arr-drop-ghost-label"></span>';
      this.dropGhostEl = ghost;
    }
    if (ghost.parentNode !== content) content.appendChild(ghost);
    /* 剪影宽度用 2 小节示意（真实长度 drop 后由文件内容决定）；
       轨道头落点时剪影直接跳到播放头处，所见即所得 */
    var widthBeats = BAR_BEATS * 2;
    ghost.style.width = Math.max(24, widthBeats * this.ppb) + "px";
    ghost.style.transform = "translateX(" + (target.startBeat * this.ppb) + "px)";
    var bar = Math.floor(target.startBeat / BAR_BEATS) + 1;
    var label = ghost.firstChild;
    if (label) label.textContent = "↧ 第 " + bar + " 小节" + (target.zone === "head" ? "（播放头）" : "");
    ghost.hidden = false;
  };

  Arrange.prototype.hideDropGhost = function (                       ) {
    if (this.dropGhostEl) {
      this.dropGhostEl.remove();
      this.dropGhostEl = null;
    }
  };

  Arrange.prototype.handleDrop = function (                         e     ) {
    var raw = e.dataTransfer.getData("application/x-arrange") || e.dataTransfer.getData("text/plain");
    if (!raw) return;
    var data;
    try { data = JSON.parse(raw); } catch (err) { return; }
    if (!data || (data.kind !== "arr-midi" && data.kind !== "arr-audio" && data.kind !== "arr-source")) return;

    // 落点与剪影同一份计算（resolveDropTarget），放置位置即拖动所见
    var target = this.resolveDropTarget(e);

    if (data.kind === "arr-source") {
      /* 机架条目拖拽载荷即条目引用本身（P2）；历史载荷把它包在 source 字段里 */
      this.replaceTrackSourceFromDrop(target.trackIdx, data.source || {
        itemId: data.itemId,
        presetId: data.presetId,
        label: data.label
      });
    } else if (data.kind === "arr-midi") {
      this.addMidiClipFromDrop(target.trackIdx, data.name, target.startBeat);
    } else {
      this.addAudioClipFromDrop(target.trackIdx, data.p, data.name, target.startBeat);
    }
  };

  /** 拖拽音源 → 替换指定轨道音源。
      P2 起载荷是机架条目引用 {itemId,presetId,label}（机架条目拖拽 / 选择器
      拖拽都发这个形状）；拖到空白区则新建一条轨道并挂上该音源。 */
  Arrange.prototype.replaceTrackSourceFromDrop = function (                         trackIdx     , source     ) {
    if (!source) return;
    var ArrRack = window.ArrRack;
    var self = this;
    var item = ArrRack.find(this.rack.items, source.itemId);
    if (!item && source.sourceId) {
      /* 素材库「音源」页签的拖拽：条目可能还没入架——先入架再分配
         （拖到轨道 = 分配并自动入架，用户不必先手动入架） */
      this.ensureRackItem(source.sourceId, source.presetId, source.label).then(function (created     ) {
        if (!created) {
          UI.toast ("⚠ 该音源无法加入机架", "warn");
          return;
        }
        self.assignRackItem(created, trackIdx, source.presetId);
      });
      return;
    }
    if (!item) {
      /* 历史载荷（{type,wave,...}）或条目已被删：尝试迁移，失败即提示 */
      var adopted = source.itemId ? null : ArrRack.migrateV1Source(source);
      if (adopted) {
        ArrRack.upsert(this.rack.items, adopted);
        item = adopted;
      }
    }
    if (!item) {
      UI.toast ("⚠ 该音源不在机架中（音色只能来自机架）", "warn");
      return;
    }
    this.assignRackItem(item, trackIdx === undefined || trackIdx === null ? undefined : trackIdx, source.presetId);
  };

  /** 拖入 MIDI 文件 → 下载字节 → 前端解析 → 创建 MIDI Clip */
  Arrange.prototype.addMidiClipFromDrop = function (                         trackIdx     , fileName     , startBeat     ) {
    var self = this;
    var track = this.tracks[trackIdx];
    if (!track || !fileName) return;
    fetch("/api/projects/" + encodeURIComponent(this.projectId       ) + "/download?names=" + encodeURIComponent(fileName))
      .then(function (res) {
        if (!res.ok) throw new Error("获取 MIDI 文件失败");
        return res.arrayBuffer();
      })
      .then(function (buf) {
        var notes = MidiParse.parseBytes(buf).filter(function (n) { return n.end > n.start; });
        if (!notes.length) {
          UI.toast ("⚠ 该文件未解析出音符", "warn");
          return;
        }
        var maxEnd = 0;
        notes.forEach(function (n) { maxEnd = Math.max(maxEnd, n.end); });
        var length = Math.max(BAR_BEATS, Math.ceil(maxEnd / BAR_BEATS) * BAR_BEATS);
        self.pushHistory();
        track.clips.push({
          id: uid("clip"),
          type: "midi",
          name: baseName(fileName)       ,
          fullName: fileName,
          start: startBeat,
          length: length,
          notes: notes,
          mute: false,
          fadeIn: 0,
          fadeOut: 0,
          _rev: 0
        });
        self.renderTracks();
        self.updateContentWidth();
        self.applyMixSafe();
        self.showHUD("♪ " + baseName(fileName) + " → " + track.name + " @ " + self.fmtPos(startBeat));
        self.scheduleSave();
      })
      .catch(function (err) {
        UI.toast ("✗ 拖入 MIDI 失败: " + err.message, "err");
      });
  };

  /** 拖入音频素材 → 解码取时长 → 创建音频 Clip（含波形峰值缓存） */
  Arrange.prototype.addAudioClipFromDrop = function (                         trackIdx     , absPath     , name     , startBeat     ) {
    var self = this;
    var track = this.tracks[trackIdx];
    if (!track || !absPath) return;
    this.engine.resume();
    this.engine.getSampleEntry(absPath).then(function (entry     ) {
      if (!entry || !entry.buffer) throw new Error("素材解码失败");
      var durBeats = Math.max(MIN_CLIP_LEN, Math.round((entry.buffer.duration / self.secondsPerBeat()) * 100) / 100);
      self.pushHistory();
      track.clips.push({
        id: uid("clip"),
        type: "audio",
        name: name || baseName(absPath),
        src: { p: absPath },
        start: startBeat,
        length: durBeats,
        offset: 0,
        mute: false,
        fadeIn: 0,
        fadeOut: 0,
        _peaks: entry.peaks,
        _rev: 0
      });
      self.renderTracks();
      self.updateContentWidth();
      self.applyMixSafe();
      self.showHUD("♫ " + (name || baseName(absPath)) + " → " + track.name + " @ " + self.fmtPos(startBeat));
      self.scheduleSave();
    }).catch(function (err     ) {
      UI.toast ("✗ 拖入素材失败: " + err.message, "err");
    });
  };

  /* ═══════════ 左栏：项目 MIDI 文件列表 ═══════════ */

  Arrange.prototype.renderMidiList = function (                       ) {
    var wrap = this.el.arrMidiList;
    if (!wrap) return;
    var self = this;
    wrap.innerHTML = "";
    if (this.el.arrMidiCount) {
      this.el.arrMidiCount.textContent = this.midiFiles.length + " FILES";
    }
    if (!this.midiFiles.length) {
      var tip = document.createElement("div");
      tip.className = "arr-empty-tip";
      tip.innerHTML = "项目中暂无 MIDI 文件<br>可在对话中让 AI 生成，或上传 .mid 文件";
      wrap.appendChild(tip);
      return;
    }
    var sorted = this.midiFiles.slice().sort(function (a     , b     ) {
      return String(a.name).localeCompare(String(b.name), "zh-CN");
    });
    sorted.forEach(function (f     ) {
      var item = document.createElement("div");
      item.className = "arr-midi-item";
      item.draggable = true;
      item.title = f.name + "（拖拽到轨道放置 / 双击在钢琴卷帘打开）";

      var glyph = document.createElement("span");
      glyph.className = "mi-glyph";
      glyph.textContent = "♪";
      var nameSpan = document.createElement("span");
      nameSpan.className = "mi-name";
      nameSpan.textContent = baseName(f.name)       ;
      var dirSpan = document.createElement("span");
      dirSpan.className = "mi-dir";
      var dirPart = String(f.name).indexOf("/") !== -1 ? String(f.name).slice(0, String(f.name).lastIndexOf("/")) : "";
      dirSpan.textContent = dirPart;

      item.appendChild(glyph);
      item.appendChild(nameSpan);
      if (dirPart) item.appendChild(dirSpan);

      item.addEventListener("dragstart", function (e) {
        var payload = JSON.stringify({ kind: "arr-midi", name: f.name });
        e.dataTransfer .setData("application/x-arrange", payload);
        e.dataTransfer .setData("text/plain", payload);
        e.dataTransfer .effectAllowed = "copy";
        item.classList.add("dragging");
        var clear = function () {
          item.classList.remove("dragging");
          item.removeEventListener("dragend", clear);
        };
        item.addEventListener("dragend", clear);
      });
      item.addEventListener("dblclick", function () {
        if (window.PianoRoll && self.projectId) {
          window.PianoRoll.openFile(self.projectId, f.name, "");
        }
      });

      wrap .appendChild(item);
    });
  };

  /* ═══════════ 右栏：素材库（P3 三页签；实现在 arrangement/material.ts） ═══════════
     条目渲染、波形缩略、搜索与页签都归 window.ArrMaterial；这里保留同名委托方法，
     供既有调用点（bindPanels / 项目切换 / 声音管理变更）与类型契约继续可用。 */

  Arrange.prototype.TREE_EXPANDED_KEY = "melody-mate-material-tree";

  Arrange.prototype.loadMaterialDirs = function (                       ) {
    var self = this;
    window.ArrMaterial.loadDirs(this).catch(function () {
      if (UI.toast) UI.toast ("⚠ 素材目录读取失败", "warn");
    });
  };

  Arrange.prototype.renderMaterialTree = function (                       ) {
    window.ArrMaterial.render(this);
  };

  Arrange.prototype.refreshOpenTreeNodes = function (                       ) {
    window.ArrMaterial.refreshOpenNodes(this);
  };

  Arrange.prototype.removeMaterialDir = function (                         d     ) {
    window.ArrMaterial.removeDir(this, d);
  };

  Arrange.prototype.toggleTreeNode = function (                         dir     , sub     ) {
    window.ArrMaterial.toggleNode(this, dir, sub);
  };

  Arrange.prototype.buildSampleRow = function (                         dir     , sub     , f     , depth     ) {
    return window.ArrMaterial.buildSampleRow(this, dir, sub, f, depth);
  };

  Arrange.prototype.nodeKey = function (                         dir     , sub     ) {
    return window.ArrMaterial.nodeKey(dir, sub);
  };

  Arrange.prototype.joinAbs = function (                         dir     , sub     , name     ) {
    return window.ArrMaterial.joinAbs(dir, sub, name);
  };

  Arrange.prototype.loadExpandedMap = function (                       ) {
    return window.ArrMaterial.loadExpanded();
  };

  Arrange.prototype.saveExpandedMap = function (                       ) {
    window.ArrMaterial.saveExpanded();
  };

  Arrange.prototype.bindPanels = function (                       ) {
    window.ArrMaterial.bind(this);
    // 混音台（P4）：顶栏按钮、面板内推子/声像/FX 槽交互
    window.ArrMixer.bind(this);
    // 标尺事件绑定（依赖 DOM 就绪）
    this.initRulerPointer();
  };

  /* ═══════════ 弹窗（重命名 / 确认 / 快捷键） ═══════════ */

  Arrange.prototype.showOverlay = function (                         overlay     ) {
    overlay.hidden = false;
    overlay.classList.remove("modal-in", "modal-out");
    void overlay.offsetWidth;
    overlay.classList.add("modal-in");
    var self = this;
    var token = ++this._modalToken;
    setTimeout(function () {
      if (token === self._modalToken) overlay.classList.remove("modal-in");
    }, 600);
  };

  Arrange.prototype.hideOverlay = function (                         overlay     ) {
    overlay.classList.remove("modal-in");
    overlay.classList.add("modal-out");
    var overlayRef = overlay;
    setTimeout(function () {
      overlayRef.classList.remove("modal-out");
      overlayRef.hidden = true;
    }, 220);
  };

  Arrange.prototype.bindModals = function (                       ) {
    var self = this;
    this._modalToken = 0;

    /* 早期测试版提示：sessionStorage 记忆（Esc 关闭不记，点「知道了」后本会话不再弹） */
    var betaOverlay = document.getElementById("arrBetaModal");
    var betaOk = document.getElementById("arrBetaOk");
    if (betaOverlay && betaOk) {
      betaOk.addEventListener("click", function () {
        try { sessionStorage.setItem("melodymate_arr_beta_v1", "1"); } catch (e) {}
        self.hideOverlay(betaOverlay);
      });
    }

    var renameOverlay = document.getElementById("arrRenameModal");
    var renameInput = document.getElementById("arrRenameInput");
    if (renameOverlay) {
      var doRename = function () {
        var idx = self._renameIdx;
        var track = self.tracks[idx ];
        var val = renameInput .value .trim();
        if (track && val && val !== track.name) {
          self.pushHistory();
          track.name = val;
          self.renderTracks();
          self.scheduleSave();
        }
        self.hideOverlay(renameOverlay);
      };
      document.getElementById("arrRenameOk") .addEventListener("click", doRename);
      document.getElementById("arrRenameCancel") .addEventListener("click", function () {
        self.hideOverlay(renameOverlay);
      });
      renameInput .addEventListener("keydown", function (e) {
        if (e.key === "Enter") { e.preventDefault(); doRename(); }
        if (e.key === "Escape") { e.preventDefault(); self.hideOverlay(renameOverlay); }
        e.stopPropagation();
      });
    }

    var confirmOverlay = document.getElementById("arrConfirmModal");
    if (confirmOverlay) {
      var cancelConfirm = function () {
        self.hideOverlay(confirmOverlay);
        self._confirmFn = null;
      };
      document.getElementById("arrConfirmOk") .addEventListener("click", function () {
        var fn = self._confirmFn;
        self.hideOverlay(confirmOverlay);
        self._confirmFn = null;
        if (fn) fn();
      });
      document.getElementById("arrConfirmCancel") .addEventListener("click", cancelConfirm);
      /* 点遮罩 = 取消（与快捷键弹窗/聊天确认框一致） */
      confirmOverlay.addEventListener("mousedown", function (e) {
        if (e.target === confirmOverlay) cancelConfirm();
      });
    }

  };

  Arrange.prototype.openRenameModal = function (                         idx     ) {
    var track = this.tracks[idx];
    var overlay = document.getElementById("arrRenameModal");
    var input = document.getElementById("arrRenameInput");
    if (!track || !overlay || !input) return;
    this._renameIdx = idx;
    input.value = track.name;
    this.showOverlay(overlay);
    setTimeout(function () { input .focus(); input .select (); }, 60);
  };

  /** 当前可见的最上层编排弹窗（无则 null） */
  Arrange.prototype._topOverlay = function (                       ) {
    var ids = ["arrConfirmModal", "arrRenameModal", "arrBetaModal"];
    for (var i = ids.length - 1; i >= 0; i--) {
      var m = document.getElementById(ids[i]);
      if (m && !m.hidden) return m;
    }
    return null;
  };

  Arrange.prototype.openConfirm = function (                         title     , text     , fn     ) {
    var overlay = document.getElementById("arrConfirmModal");
    if (!overlay) { if ((window       ).confirm) fn(); return; }
    document.getElementById("arrConfirmTitle") .textContent = title;
    document.getElementById("arrConfirmText") .textContent = text;
    this._confirmFn = fn;
    this.showOverlay(overlay);
  };

  /* ═══════════ 快捷键（FL Studio 风格 · 三域焦点仲裁） ═══════════ */

  /** 输入控件聚焦时不劫持按键 */
  Arrange.prototype.isEditableTarget = function (                         t     ) {
    if (!t) return false;
    var tag = t.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || t.isContentEditable;
  };

  /** 焦点位于可激活控件（按钮/链接）上：空格让位给浏览器默认点击语义 */
  Arrange.prototype.isActivatableTarget = function (                         t     ) {
    if (!t || !t.closest) return false;
    var tag = t.tagName;
    if (tag === "BUTTON" || tag === "A") return true;
    return !!t.closest ('button, a, [role="button"]');
  };

  /** 钢琴卷帘抽屉持有焦点时让位（三域仲裁：最后点击区域优先） */
  Arrange.prototype.pianoRollOwnsKeys = function (                       ) {
    var pr = window.PianoRoll;
    return !!(pr && pr.isOpen && pr.isFocused);
  };

  Arrange.prototype.bindKeyboard = function (                       ) {
    var self = this;
    document.addEventListener("keydown", function (e) {
      if (!self.isOpen) return;
      if (e.isComposing) return;                    // 中文输入法组合中
      if (self.isEditableTarget(e.target)) return;  // 输入控件聚焦
      if (self.pianoRollOwnsKeys()) return;         // 卷帘焦点优先

      /* 弹窗打开时：快捷键全部挂起，Esc 只关最上层弹窗（确认/重命名/快捷键） */
      var topModal = self._topOverlay();
      if (topModal) {
        if (String(e.key || "") === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          self.hideOverlay(topModal);
          if (topModal.id === "arrConfirmModal") self._confirmFn = null;
        }
        return;
      }

      var ctrl = e.ctrlKey || e.metaKey;
      var k = String(e.key || "");
      var K = window.Shortcuts;

      // Esc 分层：确认/重命名弹窗（上方分支）→ 输出机架窗口（系统保留，不可自定义）
      if (k === "Escape" || k === "Esc") {
        var rackWin = document.getElementById("arrRackWindow");
        if (rackWin && !rackWin.hidden) {
          rackWin.hidden = true;
          return;
        }
        /* 混音台是常驻面板：Esc 不关它（避免误关），只关浮窗类 */
      }

      // 混音台开合（F9，可在设置里改键）
      if (K && K.matches(e, "arrange.mixer")) {
        e.preventDefault();
        /* 注意：这里必须是 self —— 监听器挂在 document 上，this 是 document */
        window.ArrMixer.toggle(self);
        return;
      }

      // Space 走走带停；焦点在按钮/链接上时让位给浏览器激活语义
      // （否则机架的试听/分配等按钮无法用空格操作）
      if (K && K.matches(e, "arrange.playPause")) {
        if (self.isActivatableTarget(e.target)) return;
        e.preventDefault();
        self.togglePlay();
        return;
      }

      /* 播放中 seek 必须重锚（restartIfPlaying=true）：只改 playheadBeat
         不通知引擎，下一帧 RAF 就被引擎时钟写回原位——只有一帧闪动 */
      if (K && K.matches(e, "arrange.home")) { e.preventDefault(); self.seekTo(0, true); self.updatePlayButton(); return; }
      if (K && K.matches(e, "arrange.end")) { e.preventDefault(); self.seekTo(self.contentEndBeat(), true); return; }
      if (K && K.matches(e, "arrange.nudgeLeft")) {
        e.preventDefault();
        self.seekTo(Math.max(0, self.playheadBeat - (self.snap || 0.25)), true);
        return;
      }
      if (K && K.matches(e, "arrange.nudgeRight")) {
        e.preventDefault();
        self.seekTo(Math.max(0, self.playheadBeat + (self.snap || 0.25)), true);
        return;
      }
      if (K && K.matches(e, "arrange.nudgeBarLeft")) {
        e.preventDefault();
        self.seekTo(Math.max(0, self.playheadBeat - BAR_BEATS), false);
        return;
      }
      if (K && K.matches(e, "arrange.nudgeBarRight")) {
        e.preventDefault();
        self.seekTo(Math.max(0, self.playheadBeat + BAR_BEATS), false);
        return;
      }
      if (K && K.matches(e, "arrange.trackUp")) { e.preventDefault(); self.moveTrackSelection(-1); return; }
      if (K && K.matches(e, "arrange.trackDown")) { e.preventDefault(); self.moveTrackSelection(1); return; }
      if (K && K.matches(e, "arrange.muteTrack")) { self.toggleMute(self.selectedTrackIdx); return; }
      if (K && K.matches(e, "arrange.soloTrack")) { self.toggleSolo(self.selectedTrackIdx); return; }
      if (K && K.matches(e, "arrange.splitAtPlayhead")) { self.splitSelectedAtPlayhead(); return; }
      if (K && K.matches(e, "arrange.toggleLoop")) { self.toggleLoop(); return; }
      if (K && K.matches(e, "arrange.renameTrack")) { e.preventDefault(); self.openRenameModal(self.selectedTrackIdx); return; }

      if (K && K.matches(e, "arrange.undo")) { e.preventDefault(); self.undo(); return; }
      if (K && K.matches(e, "arrange.redo")) { e.preventDefault(); self.redo(); return; }
      if (K && K.matches(e, "arrange.copy")) { e.preventDefault(); self.copySelection(false); return; }
      if (K && K.matches(e, "arrange.cut")) { e.preventDefault(); self.cutSelection(); return; }
      if (K && K.matches(e, "arrange.paste")) { e.preventDefault(); self.pasteClipboard(); return; }
      if (K && K.matches(e, "arrange.clone")) { e.preventDefault(); self.duplicateSelection(); return; }
      if (K && K.matches(e, "arrange.duplicateNext")) { e.preventDefault(); self.repeatRight(); return; }
      if (K && K.matches(e, "arrange.selectAll")) { e.preventDefault(); self.selectAllClips(); return; }
      if (K && K.matches(e, "arrange.zoomIn")) { e.preventDefault(); self.zoomStep(1); return; }
      if (K && K.matches(e, "arrange.zoomOut")) { e.preventDefault(); self.zoomStep(-1); return; }

      if (K && K.matches(e, "arrange.delete")) { e.preventDefault(); self.deleteSelected(); return; }
      if (k === "Escape") {
        /* Esc 分层：吸附下拉 → 右键菜单 → 清选区（下拉此前只能点外部关闭，
           与弹层 Esc 分层惯例不一致） */
        var snapMenu = self.el.arrSnapMenu;
        if (snapMenu && !snapMenu.hidden) {
          snapMenu.hidden = true;
          if (self.el.arrSnapDropdown) self.el.arrSnapDropdown.classList.remove("open");
          return;
        }
        if (self.menuEl) { self.closeMenu(); return; }
        self.clearSelection();
        return;
      }
    }, true);
  };

  /* ═══════════ 焦点管理（与钢琴卷帘同模式） ═══════════ */

  Arrange.prototype.bindFocusManager = function (                       ) {
    var self = this;
    var ws = document.getElementById("arrangeWorkspace");

    document.addEventListener("mousedown", function (e) {
      var inWs = !!(ws && ws.contains(e.target       ));
      var drawer = document.getElementById("pianoDrawer");
      var inDrawer = !!(drawer && drawer.contains(e.target       ));
      if (inWs) self.setFocus(true);
      else if (!inDrawer) self.setFocus(false);
    }, true);

    var msgInput = document.getElementById("msgInput");
    if (msgInput) {
      msgInput.addEventListener("focus", function () { self.setFocus(false); });
    }
  };

  Arrange.prototype.setFocus = function (                         focused     ) {
    this.isFocused = !!(focused && this.isOpen);
  };

  /* ═══════════ 输出机架 (FL Studio 风格 Channel / Output Rack 悬浮窗口) ═══════════ */

  /* ═══════════ 输出机架（P2 起为「乐器箱」；条目模型与渲染在 rack.ts） ═══════════
     窗口形态/拖拽/折叠沿用原有实现；列表内容改为"机架条目"（arrangement.rack.items）
     加工程内派生采样，渲染交给 window.ArrRack.render（分类页签 / 搜索 / 六态 / 空态）。 */

  Arrange.prototype.bindOutputRack = function (                       ) {
    var self = this;
    var win = document.getElementById("arrRackWindow");
    var titlebar = document.getElementById("arrRackTitlebar");
    var btn = this.el.arrRackBtn || document.getElementById("arrRackBtn");
    var closeBtn = document.getElementById("arrRackCloseBtn");
    var collapseBtn = document.getElementById("arrRackCollapseBtn");
    var addBtn = document.getElementById("arrRackAddSourceBtn");
    var searchInput = document.getElementById("arrRackSearch")                           ;
    var filterBox = document.getElementById("arrRackFilters");

    var toggleFn = function (e     ) {
      if (e) e.stopPropagation();
      self.toggleOutputRack();
    };

    if (btn) {
      btn.addEventListener("click", toggleFn);
    }

    if (closeBtn && win) {
      closeBtn.addEventListener("click", function () {
        win .hidden = true;
      });
    }

    if (collapseBtn && win) {
      collapseBtn.addEventListener("click", function () {
        var body = document.getElementById("arrRackBody");
        if (body) {
          body.style.display = body.style.display === "none" ? "flex" : "none";
        }
      });
    }

    // 窗口自由拖拽移动 (按住标题栏)
    if (titlebar && win) {
      var isDragging = false;
      var startX = 0, startY = 0;
      var initLeft = 0, initTop = 0;

      titlebar.addEventListener("mousedown", function (e) {
        if (e.target .closest (".rack-win-btn")) return;
        isDragging = true;
        startX = e.clientX;
        startY = e.clientY;
        var rect = win .getBoundingClientRect();
        initLeft = rect.left;
        initTop = rect.top;
        document.body.style.userSelect = "none";
      });

      window.addEventListener("mousemove", function (e) {
        if (!isDragging) return;
        var dx = e.clientX - startX;
        var dy = e.clientY - startY;
        var newLeft = Math.max(10, Math.min(window.innerWidth - win .offsetWidth - 10, initLeft + dx));
        var newTop = Math.max(10, Math.min(window.innerHeight - win .offsetHeight - 10, initTop + dy));
        win .style.left = newLeft + "px";
        win .style.top = newTop + "px";
      });

      window.addEventListener("mouseup", function () {
        if (isDragging) {
          isDragging = false;
          document.body.style.userSelect = "";
        }
      });
    }

    // 分类页签（按 kind 过滤：全部 / 合成器 / SF2 / VST / MM / 采样）
    if (filterBox) {
      UI.qsa(".rack-filter-btn", filterBox).forEach(function (tab     ) {
        tab.addEventListener("click", function () {
          UI.qsa(".rack-filter-btn", filterBox ).forEach(function (t     ) { t.classList.remove("active"); });
          tab.classList.add("active");
          self._currentRackFilter = tab.getAttribute("data-tab") || "all";
          window.ArrRack.render(self);
        });
      });
    }

    // 机架内搜索（120ms 防抖：机架条目在几十条量级，防抖只为手感顺滑）
    if (searchInput) {
      var searchTimer      = null;
      searchInput.addEventListener("input", function () {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(function () {
          self._rackQuery = searchInput .value || "";
          window.ArrRack.render(self);
        }, 120);
      });
      searchInput.addEventListener("keydown", function (e     ) {
        if (e.key === "Escape" && searchInput .value) {
          e.stopPropagation();
          searchInput .value = "";
          self._rackQuery = "";
          window.ArrRack.render(self);
        }
      });
    }

    // ＋ 加载音源：打开选择器（只入架，不立刻分配——拖到轨道才是分配）
    if (addBtn) {
      addBtn.addEventListener("click", function () {
        window.ArrRack.openPickerFor(self, { assign: false, title: "加载音源到机架" });
      });
    }
  };

  Arrange.prototype.toggleOutputRack = function (                       ) {
    var win = document.getElementById("arrRackWindow");
    if (!win) return;
    if (win.hidden) {
      this.openOutputRack();
    } else {
      win.hidden = true;
    }
  };

  Arrange.prototype.openOutputRack = function (                       ) {
    var win = document.getElementById("arrRackWindow");
    if (!win) return;
    win.hidden = false;
    if (!this._currentRackFilter) this._currentRackFilter = "all";
    var filterBox = document.getElementById("arrRackFilters");
    if (filterBox) {
      var wanted = this._currentRackFilter;
      UI.qsa(".rack-filter-btn", filterBox).forEach(function (t     ) {
        t.classList.toggle("active", t.getAttribute("data-tab") === wanted);
      });
    }
    window.ArrRack.render(this);
  };

  /** renderOutputRack 兼容入口：渲染交给 ArrRack（机架条目 + 派生采样） */
  Arrange.prototype.renderOutputRack = function (                         filter     ) {
    if (filter) this._currentRackFilter = filter;
    window.ArrRack.setItems(this.rack.items);
    window.ArrRack.render(this);
  };

  /** previewRackItem 试听机架条目（引擎走 29 试听轨；webaudio 走共享 AudioContext）。
      btn 传入时按脉冲态反馈（复用 .playing + snd-pulse 动画）。 */
  Arrange.prototype.previewRackItem = function (                         item     , btn      , presetId      ) {
    if (!item) return;
    var voice = window.ArrRack.resolve(this.rack.items, {
      itemId: item.id,
      presetId: presetId !== undefined && presetId !== null && presetId !== "" ? presetId : (item.presetId || "")
    });
    this.previewRackSource(voice, btn);
  };

  /** 试听（点击时播放一个 C4 音符）。 */
  Arrange.prototype.previewRackSource = function (                         resolved     , btn      ) {
    var self = this;
    var previewProject=this.projectId,previewGeneration=this.projectEpoch;
    var current=function(){return self.projectId===previewProject && self.projectEpoch===previewGeneration;};
    var nativeTask=function(task                  ){return window.PluginPanel?window.PluginPanel.enqueue(self,task,current):Promise.resolve().then(function(){if(current())return task();});};
    var voice = resolved && resolved.voice ? resolved : window.ArrRack.resolve(this.rack.items, resolved);
    if (!voice || !voice.ok) {
      UI.toast ("⚠ 该音源不在机架中，无法试听", "warn");
      return;
    }
    var pulse = startPreviewPulse(btn);
    var done = function () { stopPreviewPulse(btn, pulse); };
    try {
      var engineMode = window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine();
      if (engineMode) {
        if (!(window.AudioBackend && window.AudioBackend.isEngineReady())) {
          UI.toast ("✗ 音频引擎未就绪，试听不可用（可在设置页切换 WEBAUDIO 模式）", "err");
          this.showHUD("✗ 音频引擎未就绪，试听不可用（可在设置页切换 WEBAUDIO 模式）");
          done();
          return;
        }
        var trk = (window.EngineBridge && window.EngineBridge.PREVIEW_TRACK) || 29;
        var playNativeNote = function () {
          if(!current()){done();return;}
          window.EngineBridge.noteOnTrack(trk, 60, 100);
          setTimeout(function () {
            if(current())try { window.EngineBridge.noteOffTrack(trk, 60); } catch (e) {}
            done();
          }, 600);
        };
        var v = voice.voice;
        if (v.type === "synth") {
          nativeTask(function(){return window.EngineBridge.setTrackVoice(trk, {
            wave: v.wave || "sawtooth",
            attack: 0.01, decay: 0.15, sustain: 0.6, release: 0.25,
            cutoff: 8000, resonance: 1.0, gain: 0.7
          });}).then(playNativeNote).catch(function (e     ) {
            done();
            self.showHUD("✗ 试听失败: " + (e && e.message ? e.message : "引擎不可用"));
          });
        } else if (v.type === "sf2") {
          var path = v.path || "";
          if (!path) throw new Error("音源文件缺失（可在设置页「声音管理」重新扫描）");
          nativeTask(function(){return window.EngineBridge.loadSoundFont(path, trk).then(function () {
            if(!current())return;
            var p = window.SoundRegistry
              ? window.SoundRegistry.parsePresetId(v.presetId || "0:0")
              : { bank: 0, program: 0 };
            return window.EngineBridge.setTrackPreset(trk, p.bank, p.program).catch(function () {
              return window.EngineBridge.setTrackPreset(trk, 0, 0).then(function () {
                if (self.showHUD) self.showHUD("⚠ 预设不存在，已用该音色库首个预设");
              });
            });
          });}).then(playNativeNote).catch(function (err     ) {
            done();
            if (self.showHUD) self.showHUD("✗ 试听失败: " + (err && err.message ? err.message : "引擎不可用"));
          });
        } else if (v.type === "builtin") {
          // 内置 GeneralUser GS：走随包音色库（/api/audio/status 的 default_soundfont）
          nativeTask(function(){return fetch("/api/audio/status").then(function (r) { return r.json(); }).then(function (j) {
            if(!current())return;
            var path = j && j.default_soundfont;
            if (!path) throw new Error("缺少内置音色库（引擎模式）");
            var program = v.tone === "strings" ? 48 : 0;
            return window.EngineBridge.loadSoundFont(path, trk)
              .then(function () { if(current())return window.EngineBridge.setTrackPreset(trk, 0, program); });
          });}).then(playNativeNote).catch(function (err     ) {
            done();
            if (self.showHUD) self.showHUD("✗ 试听失败: " + (err && err.message ? err.message : "引擎不可用"));
          });
        } else if (v.type === "sample") {
          done();
          this.previewRackSample(v.path || "");
        } else if (v.type === "plugin") {
          if (v.sourceId) {
            nativeTask(function () {
              return UI.getJSON     ("/api/sound/plugin?id=" + encodeURIComponent(v.sourceId)).then(function (resolvedPlugin     ) {
                if (!current()) return;
                if (resolvedPlugin && resolvedPlugin.category === "effect") {
                  throw new Error("效果器插件请先在混音台 FX 槽装载，再播放试听");
                }
                var kind = v.pluginKind || (resolvedPlugin && resolvedPlugin.format) || "vst3";
                return UI.postJSON("/api/engine/plugins/load", {
                  track: trk,
                  id: kind === "mm" ? v.sourceId : resolvedPlugin.id,
                  format: kind,
                  path: kind === "mm" ? resolvedPlugin.path : ""
                });
              });
            }).then(playNativeNote).catch(function (err     ) {
              done();
              UI.toast ((err && err.message) || "插件不支持此处试听：请先在混音台 FX 槽装载，再播放试听", "warn");
            });
          } else {
            done();
            UI.toast ("插件不支持此处试听：请先在混音台 FX 槽装载，再播放试听", "warn");
          }
        } else if (v.type === "pack") {
          done();
          UI.toast ("采样包在素材库中挑选采样（P3 里程碑接入）", "warn");
        } else {
          done();
          UI.toast ("⚠ 该音源暂不支持试听", "warn");
        }
        return;
      }

      /* ── 浏览器模式（WebAudio）── */
      this.engine.resume();
      var ctx = this.engine.ctx;
      if (!ctx) { done(); return; }
      var gain = ctx.createGain();
      gain.gain.value = 0.8;
      gain.connect(ctx.destination);
      var cleanup = function () {
        setTimeout(function () {
          try { gain.disconnect(); } catch (e) {}
          done();
        }, 600);
      };
      var playNote = function (player     ) {
        if(!current()){cleanup();return;}
        player.noteOn(60, 100);
        setTimeout(function () {
          try { player.noteOff(60); } catch (e) {}
        }, 600);
        cleanup();
      };
      var wv = voice.voice;
      if (wv.type === "synth") {
        var synth = new window.SynthEngine(ctx, gain);
        synth.init();
        synth.setWaveform(wv.wave || "sawtooth");
        playNote(synth);
      } else if (wv.type === "sf2") {
        this.loadSf2BufferForWebAudio(voice).then(function (rec     ) {
          if (!rec || !rec.data) throw new Error("音源数据不存在");
          var player = new window.SoundFontPlayer(ctx, gain);
          player.init();
          player.parseSF2(rec.data);
          if (wv.presetId) player.setPreset(wv.presetId);
          playNote(player);
        }).catch(function (err     ) {
          cleanup();
          if (self.showHUD) self.showHUD("✗ 试听失败: " + err.message);
        });
      } else if (wv.type === "builtin") {
        var player = new window.SoundFontPlayer(ctx, gain);
        player.init();
        player.setPreset(wv.tone || "piano");
        playNote(player);
      } else if (wv.type === "sample") {
        done();
        this.previewRackSample(wv.path || "");
      } else {
        done();
        UI.toast ("⚠ 该音源暂不支持试听", "warn");
      }
    } catch (err     ) {
      done();
      console.warn("试听音源失败:", err);
      this.showHUD("✗ 试听失败: " + (err.message || err));
    }
  };

  /* 试听脉冲态：按钮进入 .playing（snd-pulse 呼吸），1s 后自动复位。
     重复点击共享同一状态，不做引用计数（试听是即发即忘的短音）。 */
  function startPreviewPulse(btn     ) {
    if (!btn) return null;
    btn.classList.add("playing");
    return setTimeout(function () { btn.classList.remove("playing"); }, 1000);
  }

  function stopPreviewPulse(btn     , timer     ) {
    if (!btn) return;
    clearTimeout(timer);
    setTimeout(function () { btn.classList.remove("playing"); }, 400);
  }

  /** 解析 SF2 音源对应的磁盘绝对路径。注册表条目优先——引擎模式发声
      只需要它，不再依赖浏览器端 IndexedDB 记录；未入册的历史工程回退到
      IndexedDB 记录的 diskPath。 */
  Arrange.prototype.resolveSf2DiskPath = function (                         source     ) {
    var reg = window.SoundRegistry;
    var voice = source && source.voice ? source : window.ArrRack.resolve(this.rack.items, source || {});
    if (voice && voice.voice && voice.voice.path) return Promise.resolve(voice.voice.path);
    var sourceId = (voice && voice.voice && voice.voice.sourceId) || (source && (source.sourceId || source.itemId)) || "";
    if (reg && sourceId) {
      var s = reg.get(sourceId);
      var p = s ? reg.diskPathOf(s) : "";
      if (p) return Promise.resolve(p);
    }
    if (!source || !source.libId || !window.SoundLibrary) return Promise.resolve("");
    return window.SoundLibrary.getSoundFont(source.libId).then(function (rec     ) {
      if (!rec) return "";
      if (rec.diskPath) return rec.diskPath          ;
      var safe = (rec.name || "soundfont").replace(/[^\p{L}\p{N}_\-\.]/gu, "_").replace(/\.[^/.]+$/, "");
      return "Library/soundfonts/" + (safe || "soundfont") + ".sf2";
    }).catch(function () { return ""; });
  };

  /** 浏览器模式（WebAudio）下取 SF2 的采样数据：优先按注册表 id 反查
      本地解码缓存，回退历史 IndexedDB 记录（libId）。 */
  Arrange.prototype.loadSf2BufferForWebAudio = function (                         source     ) {
    var reg = window.SoundRegistry;
    var voice = source && source.voice ? source : window.ArrRack.resolve(this.rack.items, source || {});
    var sourceId = (voice && voice.voice && voice.voice.sourceId) || (source && (source.sourceId || source.itemId)) || "";
    if (reg && sourceId) {
      var s = reg.get(sourceId) || { id: sourceId, name: "" };
      return reg.loadSf2(sourceId,source && source.libId);
    }
    if (source && source.libId && window.SoundLibrary) {
      return window.SoundLibrary.getSoundFont(source.libId);
    }
    return Promise.resolve(null);
  };

  /** 预览采样音频（与文件树双击试听同一链路 /api/arrangement/audio） */
  Arrange.prototype.previewRackSample = function (                         path     ) {
    if (!path) return;
    this.engine.resume();
    this.engine.previewSample(path).catch(function () {
      if (window.Arrange && window.Arrange.showHUD) {
        window.Arrange.showHUD("✗ 音频试听失败（文件不存在或格式不支持）");
      }
    });
  };

  /* ═══════════ 启动 ═══════════ */

  window.Arrange = new (Arrange       )();
  document.addEventListener("DOMContentLoaded", function () {
    window.Arrange.init();
  });
  /* 页面卸载：停走带 + 落盘。
     引擎模式必须显式 stop/clearSamples——引擎走带与采样表由后端进程持有，
     页面销毁不会让它们停下（此前导航到设置页后工程音频继续播放，且前端
     noteOff timer 已随页面销毁 → 挂音）。sendBeacon 不阻塞卸载 */
  function onPageUnload() {
    var a = window.Arrange;
    if (a) {
      try {
        if (window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine()) {
          if (a.engine && a.engine.stopSchedule) a.engine.stopSchedule();
          if (window.EngineBridge) {
            if (window.EngineBridge.stop) { try { window.EngineBridge.stop(); } catch (e) {} }
            if (window.EngineBridge.clearSamples) { try { window.EngineBridge.clearSamples(); } catch (e) {} }
          }
        }
      } catch (e) {}
    }
    if (!a || !a.projectId) return;
    /* 关窗兜底：把插件状态也尽力保存一份（不依赖工程 dirty 标记）。
       fire-and-forget，不阻塞卸载；浏览器不保证这些请求在页面销毁前
       发出（sendBeacon 才有该保证），故仅是尽力而为的兜底。 */
    try {
      if (window.PluginPanel && window.PluginPanel.saveAll) window.PluginPanel.saveAll(a);
    } catch (e) {}
    if (!a.dirty && !a.saveInFlight) return;
    a._stashDraft();
    if(a.saveConflict || a.saveInFlight)return;
    if (navigator.sendBeacon) {
      navigator.sendBeacon(
        "/api/projects/" + encodeURIComponent(a.projectId) + "/arrangement",
        new Blob([JSON.stringify(a.serialize())], { type: "application/json" })
      );
    }
  }
  window.addEventListener("beforeunload", onPageUnload);

})(window);
