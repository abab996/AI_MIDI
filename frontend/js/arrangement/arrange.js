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

  function uid(prefix) {
    return prefix + "_" + Date.now().toString(36) + "_" + Math.random().toString(36).slice(2, 7);
  }

  function hexToRgba(hex, alpha) {
    var m = /^#([0-9a-f]{6})$/i.exec(hex || "");
    if (!m) return "rgba(0,184,204," + alpha + ")";
    var n = parseInt(m[1], 16);
    return "rgba(" + ((n >> 16) & 255) + "," + ((n >> 8) & 255) + "," + (n & 255) + "," + alpha + ")";
  }

  function clamp(v, lo, hi) { return Math.max(lo, Math.min(hi, v)); }

  function baseName(path) {
    return String(path || "").split(/[\\/]/).pop();
  }

  /* ═══════════ 控制器状态 ═══════════ */

  function Arrange() {
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
    this.selectedTrackIdx = 0;
    this.selectedClips = [];
    this.clipboard = null;

    this.midiFiles = [];
    this.dirs = [];               // 已注册素材根目录（settings.material_dirs 镜像）
    this._treeCache = {};         // 素材树各层目录内容缓存
    this.expandedMap = null;      // 素材树展开态（懒加载自 localStorage）

    this.playheadBeat = 0;
    this.playbackOriginBeat = 0;  // 本次播放的起始位置（暂停回退目标）
    this.isPlaying = false;

    this.engine = new window.ArrangeEngine();
    var self = this;
    this.engine.getTracks = function () { return self.tracks; };
    this.engine.onSoundFontLoaded = function () { self.showHUD("✓ 轨道音源已加载"); };
    this.engine.onSoundFontError = function (tid, err) {
      UI.toast("✗ 轨道音源加载失败: " + (err && err.message ? err.message : "未知错误"), "err");
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

  Arrange.prototype.init = function () {
    if (this.initialized) return;
    this.initialized = true;

    var ids = ["arrStage", "chatFoldPane", "arrangeToggleBtn",
      "arrHomeBtn", "arrPlayBtn", "arrPlayIconPath", "arrPlayLabel", "arrStopBtn", "arrLoopBtn", "arrMetroBtn", "arrResumeBtn",
      "arrBpmInput", "arrPosDisplay", "arrSnapDropdown", "arrSnapBtn", "arrSnapMenu",
      "arrZoomOutBtn", "arrZoomRange", "arrZoomInBtn", "arrUndoBtn", "arrRedoBtn",
      "arrSaveStamp", "arrShortcutsBtn", "arrRackBtn", "arrHudBadge",
      "arrMidiCount", "arrMidiList",
      "arrTracksScroll", "arrInner", "arrRulerCanvas", "arrLanes", "arrAddTrackRow", "arrAddTrackBtn", "arrPlayline",
      "arrFileTree", "arrAddDirBtn"];
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

  Arrange.prototype.$ = function (id) { return this.el[id] || document.getElementById(id); };

  /* ═══════════ 折叠展开 ═══════════ */

  Arrange.prototype.bindToggle = function () {
    var self = this;
    if (this.el.arrangeToggleBtn) {
      this.el.arrangeToggleBtn.addEventListener("click", function () {
        self.toggle();
      });
    }
  };

  Arrange.prototype.toggle = function () {
    if (this.isOpen) this.close(); else this.open();
  };

  Arrange.prototype.open = function () {
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
        if (sessionStorage.getItem("aimidi_arr_beta_v1") !== "1") {
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

  Arrange.prototype.close = function () {
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
  Arrange.prototype.setProject = function (projectId) {
    if (this.projectId === projectId) return;
    this.doSave();   // 切走前冲刷：此后 projectId 被清空，挂起的保存定时器会静默空转
    this.stopPlayback();
    this.undoStack = [];
    this.redoStack = [];
    this.selectedClips = [];
    this.selectedTrackIdx = 0;
    this.playheadBeat = 0;
    this.projectId = projectId || null;
    this.loadedProjectId = null;

    if (!this.projectId) {
      this.tracks = [];
      if (this.el.arrLanes) this.el.arrLanes.innerHTML = "";
      if (this.isOpen) this.close();
      return;
    }

    var self = this;
    /* 全局 BPM 与编排数据并行加载：全局 BPM 是唯一事实，
       覆盖编排快照里的本地 bpm（旧版数据迁移兼容） */
    var bpmReq = UI.getJSON("/api/projects/" + encodeURIComponent(this.projectId) + "/bpm")
      .catch(function () { return null; });
    Promise.all([
      UI.getJSON("/api/projects/" + encodeURIComponent(this.projectId) + "/arrangement"),
      bpmReq
    ]).then(function (results) {
        var data = results[0];
        var bpmData = results[1];
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
        self.setSaveStamp("SAVED", false);
        self.syncTransportUI();
        if (self.isOpen) self.renderAll();
      })
      .catch(function (e) {
        /* 读取失败不再静默回退空白（用户会以为编排数据丢了） */
        if (UI.toast) UI.toast("⚠ 编排数据读取失败，已载入空白编排: " + ((e && e.message) || ""), "err");
        self.defaultState();
        self.loadedProjectId = self.projectId;
        if (self.isOpen) self.renderAll();
      });
  };

  /** chat.js 文件清单变化时同步左栏 MIDI 列表 */
  Arrange.prototype.refreshMidiList = function (files) {
    this.midiFiles = files || [];
    this.renderMidiList();
  };

  Arrange.prototype.defaultState = function () {
    this.bpm = 120;
    this.loop = { on: false, start: 0, end: 16 };
    this.tracks = [this.makeTrack(1)];
    this.syncTransportUI();
  };

  Arrange.prototype.makeTrack = function (index) {
    return {
      id: uid("tr"),
      name: "Track " + index,
      color: TRACK_COLORS[(index - 1) % TRACK_COLORS.length],
      mute: false,
      solo: false,
      volume: 0.8,
      source: { type: "synth", wave: "sawtooth", label: "合成器 · 锯齿波" },
      clips: []
    };
  };

  /** 干净的持久化结构：剥离运行时字段（_rev/_peaks 等），供保存与撤销快照共用 */
  Arrange.prototype.cleanState = function () {
    return {
      version: 1,
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
            var clip = {
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
      })
    };
  };

  Arrange.prototype.serialize = function () {
    return this.cleanState();
  };

  Arrange.prototype.deserialize = function (data) {
    this.bpm = clamp(Number(data.bpm) || 120, 30, 300);
    this.snap = Number(data.snap) >= 0 ? Number(data.snap) : 0.25;
    this.ppb = clamp(Number(data.ppb) || 26, 8, 80);
    this.loop = {
      on: !!(data.loop && data.loop.on),
      start: Number(data.loop && data.loop.start) || 0,
      end: Number(data.loop && data.loop.end) || 16
    };
    var self = this;
    this.tracks = (Array.isArray(data.tracks) ? data.tracks : []).map(function (t, i) {
      var track = self.makeTrack(i + 1);
      if (t.id) track.id = String(t.id);
      if (t.name) track.name = t.name;
      if (t.color) track.color = t.color;
      track.mute = !!t.mute;
      track.solo = !!t.solo;
      track.volume = clamp(t.volume !== undefined ? Number(t.volume) : 0.8, 0, 1);
      if (t.source && typeof t.source === "object") track.source = t.source;
      track.clips = (Array.isArray(t.clips) ? t.clips : []).map(function (c) {
        var clip = {
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
    if (!this.tracks.length) this.tracks = [this.makeTrack(1)];
    this._pruneTrackNodes();
    this.engine.bpm = this.bpm;
    this.engine.loop = this.loop;   // 播放中载入新数据也保持循环边界同步
    this.syncTransportUI();
  };

  Arrange.prototype.scheduleSave = function () {
    if (!this.projectId) return;
    this.dirty = true;
    this.setSaveStamp("EDIT…", true);
    var self = this;
    clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(function () { self.doSave(); }, 800);
  };

  Arrange.prototype.doSave = function () {
    var self = this;
    if (!this.projectId || !this.dirty) return;
    this.setSaveStamp("SAVING", false);
    UI.putJSON("/api/projects/" + encodeURIComponent(this.projectId) + "/arrangement", this.serialize())
      .then(function () {
        self.dirty = false;
        self.setSaveStamp("SAVED", false);
      })
      .catch(function (e) {
        self.setSaveStamp("SAVE ERR", true);
        if (UI.toast) UI.toast("✗ 编排保存失败: " + e.message, "err");
      });
  };

  Arrange.prototype.setSaveStamp = function (text, warn) {
    var el = this.el.arrSaveStamp;
    if (!el) return;
    el.textContent = text;
    el.className = warn ? "stamp danger" : "stamp ok";
  };

  /* ═══════════ 撤销 / 重做 ═══════════ */

  Arrange.prototype.snapshot = function () {
    return JSON.stringify(this.cleanState());
  };

  /** 清理已不存在轨道的发声链（整体替换 tracks 的路径：载入/撤销/重做） */
  Arrange.prototype._pruneTrackNodes = function () {
    var live = {};
    this.tracks.forEach(function (t) { live[t.id] = true; });
    for (var id in this.engine.trackNodes) {
      if (!live[id]) {
        this.engine.stopTrackSources(id);
        var nodes = this.engine.trackNodes[id];
        if (nodes.synth) nodes.synth.stopAll();
        if (nodes.soundfont) nodes.soundfont.stopAll();
        try { nodes.gain.disconnect(); nodes.analyser.disconnect(); } catch (e) {}
        delete this.engine.trackNodes[id];
      }
    }
  };

  Arrange.prototype.restoreSnapshot = function (json) {
    var s = JSON.parse(json);
    this.bpm = s.bpm || 120;
    this.loop = s.loop || { on: false, start: 0, end: 16 };
    this.tracks = s.tracks || [];
    if (!this.tracks.length) this.tracks = [this.makeTrack(1)];
    this._pruneTrackNodes();
    this.engine.applyMix(this.tracks);
    this.engine.bpm = this.bpm;
    this.engine.loop = this.loop;   // 撤销/重做同步循环边界（此前播放中撤销循环改动仍按旧边界跑）
    this.syncTransportUI();
    this.renderAll();
    this.rescheduleSamplesDebounced();   // 撤销/重做同样要把音频调度表拉回当前快照
  };

  Arrange.prototype.pushHistory = function () {
    this.undoStack.push(this.snapshot());
    if (this.undoStack.length > 50) this.undoStack.shift();
    this.redoStack = [];
  };

  Arrange.prototype.undo = function () {
    if (!this.undoStack.length) { this.showHUD("没有更多可撤销的操作"); return; }
    this.redoStack.push(this.snapshot());
    this.restoreSnapshot(this.undoStack.pop());
    this.showHUD("↩ 撤销 (Ctrl+Z)");
    this.scheduleSave();
  };

  Arrange.prototype.redo = function () {
    if (!this.redoStack.length) { this.showHUD("没有更多可重做的操作"); return; }
    this.undoStack.push(this.snapshot());
    this.restoreSnapshot(this.redoStack.pop());
    this.showHUD("↪ 重做 (Ctrl+Y)");
    this.scheduleSave();
  };

  /* ═══════════ 走带控制 ═══════════ */

  Arrange.prototype.secondsPerBeat = function () {
    return 60 / (this.bpm || 120);
  };

  Arrange.prototype.bindTransport = function () {
    var self = this;
    var on = function (id, fn) {
      var b = this.$(id);
      if (b) b.addEventListener("click", function () { fn(); b.blur(); });
    }.bind(this);

    on("arrHomeBtn", function () { self.seekTo(0, false); self.showHUD("⏮ 回到开头 (Home)"); });
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
      this.el.arrBpmInput.addEventListener("change", function () {
        self.setBpm(Number(this.value) || 120);
      });
    }
    if (this.el.arrZoomRange) {
      this.el.arrZoomRange.addEventListener("input", function () {
        self.setPpb(Number(this.value));
      });
    }

    // Ctrl+滚轮缩放 / Shift+滚轮或触摸板横滚：横向滚动时挂起播放头跟随 2s
    if (this.el.arrTracksScroll) {
      this.el.arrTracksScroll.addEventListener("wheel", function (e) {
        if (e.ctrlKey) {
          e.preventDefault();
          self.setPpb(self.ppb + (e.deltaY < 0 ? 3 : -3));
        } else if (e.shiftKey || e.deltaX) {
          self._followSuspendUntil = performance.now() + 2000;
        }
      }, { passive: false });
    }
  };

  Arrange.prototype.syncTransportUI = function () {
    if (this.el.arrBpmInput) this.el.arrBpmInput.value = this.bpm;
    if (this.el.arrZoomRange) this.el.arrZoomRange.value = this.ppb;
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

  Arrange.prototype.togglePlay = function () {
    if (this.isPlaying) this.pausePlayback();
    else this.startPlayback();
  };

  Arrange.prototype.startPlayback = function () {
    this.engine.resume();
    this.engine.metronome = this.metronome;
    this.engine.loop = this.loop;
    this.engine.bpm = this.bpm;
    // 记录本次播放的起始位置（「暂停后恢复光标位置」回退目标）
    this.playbackOriginBeat = Math.max(0, this.playheadBeat);
    this.isPlaying = true;
    this.engine.play(this.playheadBeat);
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
  Arrange.prototype.pausePlayback = function (tempSuspend) {
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

  Arrange.prototype.stopPlayback = function (resetHead) {
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

  Arrange.prototype.stopEngineClock = function () {
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

  Arrange.prototype.updatePlayButton = function () {
    if (!this.el.arrPlayLabel) return;
    this.el.arrPlayLabel.textContent = this.isPlaying ? "暂停" : "播放";
    if (this.el.arrPlayIconPath) {
      this.el.arrPlayIconPath.setAttribute("d", this.isPlaying
        ? "M5 3.5h2.25v9H5zM8.75 3.5H11v9H8.75z"
        : "m11.596 8.697-6.363 3.692c-.54.313-1.233-.066-1.233-.697V4.308c0-.63.692-1.01 1.233-.696l6.363 3.692a.802.802 0 0 1 0 1.393z");
    }
  };

  Arrange.prototype.toggleLoop = function () {
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

  Arrange.prototype.toggleMetro = function () {
    this.metronome = !this.metronome;
    this.engine.metronome = this.metronome;
    this.syncTransportUI();
    this.showHUD(this.metronome ? "节拍器: 开启" : "节拍器: 关闭");
  };

  /** 走带条上的「暂停后光标回起点」开关：就地切换并持久化。
      钢琴窗共用同一份 UI.transportPrefs 缓存，两窗口行为即时一致 */
  Arrange.prototype.toggleResumeOnPause = function () {
    var prefs = UI.transportPrefs ? UI.transportPrefs() : { resumeOnPause: false };
    var next = !prefs.resumeOnPause;
    UI.setTransportPref("resumeOnPause", next);
    this.showHUD(next ? "⏪ 已开启：暂停后光标回到本次播放起点"
                      : "已关闭：暂停后光标停在当前位置");
    var self = this;
    UI.postJSON("/api/transport/prefs", { resume_on_pause: next }).then(function (r) {
      if (!r || !r.ok) throw new Error((r && r.message) || "保存失败");
    }).catch(function (err) {
      if (UI.toast) UI.toast("⚠ 走带偏好保存失败：" + err.message, "warn");
      self._resumeSaveFailed = true;   /* 重开应用时会重新读取旧值 */
    });
  };

  /** 按钮高亮与设置值同步（init 订阅初值/后续变化，切换时也会调用） */
  Arrange.prototype.syncResumeUI = function (prefs) {
    if (!this.el.arrResumeBtn) return;
    this.el.arrResumeBtn.classList.toggle("active",
      !!(prefs && prefs.resumeOnPause));
  };

  Arrange.prototype.setBpm = function (val) {
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
          if (UI.toast) UI.toast("⚠ 全局 BPM 保存失败: " + ((e && e.message) || ""), "warn");
        });
    }
  };

  Arrange.prototype.seekTo = function (beat, restartIfPlaying) {
    this.playheadBeat = Math.max(0, beat);
    this.updatePlayline();
    this.updatePosDisplay();
    if (restartIfPlaying && this.isPlaying) {
      this.engine.play(this.playheadBeat);
      try { if (window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred() && window.EngineBridge) { try{window.EngineBridge.locate(this.playheadBeat);}catch(e){} } } catch(e){}
    }
  };

  Arrange.prototype.exportWav = function () {
    var self = this;
    // 渲染期间防重复点击（离线渲染耗时数秒，连点会排队多个渲染任务）
    if (this._exporting) {
      if (window.UI && window.UI.toast) window.UI.toast("⏳ 正在导出中，请等待完成", "warn");
      return;
    }
    // 仅 AUTO+JUCE就绪时可离线导出（保证按全局采样率+尾音不截断）
    var useNative = false;
    try { useNative = window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred(); } catch(e) {}
    if (!useNative) {
      if (window.UI && window.UI.toast) window.UI.toast("⚠ 当前为 WEBAUDIO 模式，导出走浏览器（无离线尾音保障）。请切回 AUTO 后重试", "warn");
      return;
    }
    if (!this.tracks || !this.tracks.length) {
      if (window.UI && window.UI.toast) window.UI.toast("⚠ 无轨道可导出", "warn");
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
      var a = document.createElement("a");
      a.href = url;
      a.download = "";
      document.body.appendChild(a);
      a.click();
      setTimeout(function(){ try{ a.remove(); }catch(e){} }, 1000);
    }).catch(function(err){
      self.showHUD("✗ 导出失败");
      if (window.UI && window.UI.toast) window.UI.toast("✗ 导出失败: " + (err && err.message || err), "err");
    }).finally(finish);
  };

  /* ═══════════ 缩放 ═══════════ */

  Arrange.prototype.zoomStep = function (dir) {
    this.setPpb(this.ppb + dir * 4);
  };

  /** 横向缩放：以视口 40% 处的拍位为锚点，缩放后回滚到原锚点 */
  Arrange.prototype.setPpb = function (val) {
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

  Arrange.prototype.bindSnapDropdown = function () {
    var self = this;
    var wrap = this.el.arrSnapDropdown;
    var btn = this.el.arrSnapBtn;
    var menu = this.el.arrSnapMenu;
    if (!wrap || !btn || !menu) return;

    function renderMenu() {
      menu.innerHTML = "";
      SNAPS.forEach(function (s) {
        var opt = document.createElement("div");
        opt.className = "select-option" + (self.snap === s.v ? " active" : "");
        opt.textContent = s.label;
        opt.addEventListener("click", function () {
          self.snap = s.v;
          menu.hidden = true;
          wrap.classList.remove("open");
          self.syncTransportUI();
          self.showHUD("吸附: " + s.label);
          self.scheduleSave();
        });
        menu.appendChild(opt);
      });
    }

    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      var show = menu.hidden;
      if (show) {
        renderMenu();
        menu.classList.add("menu-in");
        menu.hidden = false;
        wrap.classList.add("open");
      } else {
        menu.hidden = true;
        wrap.classList.remove("open");
      }
      btn.blur();
    });
    document.addEventListener("mousedown", function (e) {
      if (!menu.hidden && !wrap.contains(e.target)) {
        menu.hidden = true;
        wrap.classList.remove("open");
      }
    });
  };

  /* ═══════════ 轨道区事件绑定（委托） ═══════════ */

  Arrange.prototype.bindTracksArea = function () {
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
    inner.addEventListener("mousedown", function (e) {
      if (e.button !== 0) return;
      var headEl = e.target.closest(".arr-track-head");
      if (headEl) {
        self.selectTrack(Number(headEl.dataset.trackIdx));
        return;
      }
      var handleEl = e.target.closest(".arr-clip-handle");
      var edgeEl = e.target.closest(".arr-clip-edge");
      var clipEl = e.target.closest(".arr-clip");
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
    inner.addEventListener("dblclick", function (e) {
      var clipEl = e.target.closest(".arr-clip");
      /* 兜底：target 因任何原因落在轨道容器上时，按坐标重新命中 */
      if (!clipEl && typeof document.elementFromPoint === "function") {
        var hit = document.elementFromPoint(e.clientX, e.clientY);
        if (hit && hit.closest) clipEl = hit.closest(".arr-clip");
      }
      if (!clipEl) return;
      var found = self.locateClip(clipEl);
      if (!found) return;
      if (found.clip.type === "midi" && found.clip.fullName && window.PianoRoll) {
        window.PianoRoll.openFile(self.projectId, found.clip.fullName, "");
      } else if (found.clip.type === "audio" && found.clip.src) {
        self.engine.resume();
        self.engine.previewSample(found.clip.src.p).catch(function () {
          if (UI.toast) UI.toast("✗ 音频试听失败（文件不存在或格式不支持）", "err");
        });
      }
    });

    // 右键菜单：轨道头 / Clip / 空白处（含表头空白列）
    inner.addEventListener("contextmenu", function (e) {
      e.preventDefault();
      var headEl = e.target.closest(".arr-track-head");
      if (headEl) {
        var idx = Number(headEl.dataset.trackIdx);
        self.selectTrack(idx);
        self.openTrackMenu(e, idx);
        return;
      }
      var clipEl = e.target.closest(".arr-clip");
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
      scroller.addEventListener("dragover", function (e) {
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
      scroller.addEventListener("dragleave", function (e) {
        self.clearDropHighlight();
        self.clearSourceHighlight();
      });
      scroller.addEventListener("drop", function (e) {
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
  Arrange.prototype.clientXToBeat = function (clientX) {
    var scroller = this.el.arrTracksScroll;
    if (!scroller) return 0;
    var r = scroller.getBoundingClientRect();
    return (clientX - r.left + scroller.scrollLeft - HEADER_W) / this.ppb;
  };

  Arrange.prototype.snapBeat = function (beat) {
    if (!this.snap) return Math.round(beat * 1000) / 1000;
    return Math.round(beat / this.snap) * this.snap;
  };

  /** 循环区间端点按小节吸附 */
  Arrange.prototype.barSnap = function (beat) {
    return Math.round(beat / BAR_BEATS) * BAR_BEATS;
  };

  Arrange.prototype.contentEndBeat = function () {
    var end = BAR_BEATS;
    this.tracks.forEach(function (t) {
      t.clips.forEach(function (c) { end = Math.max(end, c.start + c.length); });
    });
    return end;
  };

  /* ═══════════ 标尺交互（下半播放头 / 上半循环区间） ═══════════ */

  Arrange.prototype.bindRulerEvents = function () {
    // 已并入 bindTracksArea 的窗口级 mousemove/up；标尺自身事件在 initRulerPointer 绑定
  };

  Arrange.prototype.initRulerPointer = function () {
    if (this._rulerPointerBound) return; // 只绑定一次
    var self = this;
    var canvas = this.el.arrRulerCanvas;
    if (!canvas) return;
    this._rulerPointerBound = true;

    canvas.addEventListener("mousedown", function (e) {
      if (e.button !== 0) return;
      e.preventDefault();
      var rect = canvas.getBoundingClientRect();
      var upper = (e.clientY - rect.top) < RULER_H / 2;
      var beat = clamp(self.clientXToBeat(e.clientX), 0, 1e6);
      if (upper) {
        self.rulerDrag = { mode: "loop", anchor: self.barSnap(beat) };
        self.loop.on = true;
        self.loop.start = self.loop.anchor = self.rulerDrag.anchor;
        self.loop.end = self.rulerDrag.anchor + BAR_BEATS;
        self.engine.loop = self.loop;
        self.syncTransportUI();
        self.renderRuler();
      } else {
        var wasPlaying = self.isPlaying;
        if (wasPlaying) self.pausePlayback(true);
        self.seekTo(self.snapBeat(Math.max(0, beat)), false);
        self.rulerDrag = { mode: "seek", wasPlaying: wasPlaying };
      }
    });

    canvas.addEventListener("dblclick", function (e) {
      var rect = canvas.getBoundingClientRect();
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

  Arrange.prototype.updateRulerDrag = function (e) {
    if (!this.rulerDrag) return;
    var beat = clamp(this.clientXToBeat(e.clientX), 0, 1e6);
    if (this.rulerDrag.mode === "seek") {
      this.seekTo(Math.max(0, this.snapBeat(beat)), false);
    } else {
      var cur = this.barSnap(beat);
      this.loop.start = Math.min(this.rulerDrag.anchor, cur);
      this.loop.end = Math.max(this.rulerDrag.anchor + BAR_BEATS, cur === this.rulerDrag.anchor ? cur + BAR_BEATS : cur);
      this.renderRuler();
    }
  };

  Arrange.prototype.finishRulerDrag = function () {
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

  Arrange.prototype.locateClip = function (clipEl) {
    var row = clipEl.closest(".arr-lane-row");
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

  Arrange.prototype.beginClipGesture = function (e, clipEl, mode) {
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
  Arrange.prototype.gestureSnap = function (beat, e) {
    if (e && e.altKey) return Math.max(0, Math.round(beat * 1000) / 1000);
    return this.snapBeat(beat);
  };

  /** 遍历当前选中的 clip（含所在轨道） */
  Arrange.prototype.forEachSelectedClip = function (fn) {
    var self = this;
    this.tracks.forEach(function (track) {
      track.clips.forEach(function (clip) {
        if (self.selectedClips.indexOf(clip.id) !== -1) fn(track, clip);
      });
    });
  };

  Arrange.prototype.updateClipGesture = function (e) {
    var d = this.dragState;
    if (!d || !d.found) return;
    var clip = d.found.clip;
    var el = d.clipEl;
    var now = performance.now();

    /* 位移超过阈值才算真的在拖：此时才隐藏 clip 的指针命中，
       elementFromPoint（跨轨判定）才能看到下方的轨道 */
    if (!d.peOff && (Math.abs(e.clientX - d.startX) > 3 || Math.abs(e.clientY - d.startY) > 3)) {
      d.peOff = true;
      if (el) el.style.pointerEvents = "none";
    }

    if (d.mode === "move") {
      /* 延迟克隆：Ctrl 按下后位移超过阈值（4px）才克隆选中 Clip 并
         改拖副本——Ctrl+点击（不拖动）不产生副本（FL Playlist 惯例） */
      if (d.clonePending) {
        if (Math.abs(e.clientX - d.startX) + Math.abs(e.clientY - d.startY) <= 4) return;
        var clones = [];
        this.forEachSelectedClip(function (track, clip) {
          var copy = JSON.parse(JSON.stringify(clip));
          copy.id = uid("clip");
          track.clips.push(copy);
          clones.push(copy.id);
        });
        if (clones.length) {
          this.selectedClips = clones;
          this.renderTracks();
          var newEl = this.el.arrLanes.querySelector('.arr-clip[data-clip-id="' + clones[0] + '"]');
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
      var newStart = this.gestureSnap(Math.max(0, this.clientXToBeat(e.clientX) - d.grabOffsetPx / this.ppb), e);
      if (Math.abs(newStart - clip.start) > 1e-6) {
        var delta = newStart - clip.start;
        clip.start = newStart;
        d.changed = true;
        // 多选拖拽：其余选中剪辑跟随相同位移（此前只动被抓的，多选移动静默丢失部分剪辑）
        if (this.selectedClips.indexOf(clip.id) !== -1 && this.selectedClips.length > 1) {
          var selfM = this;
          var anchorId = clip.id;
          this.forEachSelectedClip(function (t2, c2) {
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
      var contentEl = laneEl && laneEl.closest ? laneEl.closest(".arr-lane-content") : null;
      if (contentEl) {
        var rowEl = contentEl.closest(".arr-lane-row");
        var targetIdx = Number(rowEl.dataset.trackIdx);
        if (!isNaN(targetIdx) && targetIdx !== d.movedToTrackIdx && this.tracks[targetIdx]) {
          // 从原轨摘除挂到新轨（DOM 同步搬移，数据在收尾时统一落）
          var oldRow = d.found.trackIdx;
          var oldTrack = this.tracks[oldRow];
          var ci = oldTrack.clips.indexOf(clip);
          if (ci !== -1) oldTrack.clips.splice(ci, 1);
          this.tracks[targetIdx].clips.push(clip);
          contentEl.appendChild(el);
          d.found.trackIdx = targetIdx;
          d.found.track = this.tracks[targetIdx];
          d.movedToTrackIdx = targetIdx;
          d.changed = true;
        }
      }
      this.positionClipEl(el, clip);
    } else if (d.mode === "trimL") {
      var endFixed = d.origStart + d.origLength;
      /* 音频素材不能裁到文件头之前：可向左恢复的量 = 当前 offset（秒）
         换算成拍；越界会造成"画面帧界与实际发声内容错位" */
      var minStart = d.origStart;
      if (clip.type === "audio") {
        minStart = d.origStart - Math.max(0, clip.offset || 0) / d.spb;
      }
      var newStart = clamp(this.gestureSnap(this.clientXToBeat(e.clientX), e), Math.max(0, minStart), endFixed - MIN_CLIP_LEN);
      var delta = newStart - d.origStart;
      if (Math.abs(delta) > 1e-6) {
        clip.start = newStart;
        clip.length = endFixed - newStart;
        if (clip.type === "audio") clip.offset = Math.max(0, (clip.offset || 0) + delta * d.spb);
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
      var sec = Math.max(0, (this.clientXToBeat(e.clientX) - clip.start) * d.spb);
      clip.fadeIn = Math.round(Math.min(sec, clip.length * d.spb * 0.9) * 100) / 100;
      clip._rev = (clip._rev || 0) + 1; // 缩略图 key 含 rev，渐变变化需强制重绘
      d.changed = true;
    } else if (d.mode === "fadeR") {
      var endSec = (clip.start + clip.length) * d.spb;
      var sec2 = Math.max(0, endSec - this.clientXToBeat(e.clientX) * d.spb);
      clip.fadeOut = Math.round(Math.min(sec2, clip.length * d.spb * 0.9) * 100) / 100;
      clip._rev = (clip._rev || 0) + 1;
      d.changed = true;
    }

    // 渐变手柄实时重绘缩略（~12fps 节流）
    if ((d.mode === "fadeL" || d.mode === "fadeR" || d.mode === "trimL" || d.mode === "trimR") && now - d.lastThumbDraw > 80) {
      d.lastThumbDraw = now;
      this.drawThumbFor(el);
    }
  };

  Arrange.prototype.finishClipGesture = function () {
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
    this.showHUD(d.mode === "move" ? ("位置: " + (d.found.clip.start / BAR_BEATS + 1).toFixed(2) + " 小节")
      : d.mode === "fadeL" ? ("渐入: " + d.found.clip.fadeIn.toFixed(2) + "s")
      : d.mode === "fadeR" ? ("渐出: " + d.found.clip.fadeOut.toFixed(2) + "s")
      : "长度: " + d.found.clip.length.toFixed(2) + " 拍");
    this.updateContentWidth();
    this.scheduleSave();
  };

  Arrange.prototype.applyMixSafe = function () {
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
        for (var i = 0; i < self.tracks.length && i < 32; i++) {
          var t = self.tracks[i];
          self.engine.updateTrackMix(
            i,
            t.volume !== undefined ? t.volume : 0.8,
            0,
            !!t.mute,
            !!t.solo,
            true
          );
        }
      }, 80);
    }
    this.rescheduleSamplesDebounced();
  };

  /** 汇总当前全部音频 clip → 引擎调度表。scheduleSamples 为全量重建语义，
      SamplePool 按路径缓存已解码样本，重复下发只做表重建不重新解码 */
  Arrange.prototype.sendSampleSchedule = function () {
    if (!window.EngineBridge) return;
    var clips = [];
    for (var ti = 0; ti < this.tracks.length; ti++) {
      var tr = this.tracks[ti];
      for (var ci = 0; ci < tr.clips.length; ci++) {
        var c = tr.clips[ci];
        if (c.mute || c.type !== "audio" || !c.src || !c.src.p) continue;
        clips.push({ track: ti, path: c.src.p, start: c.start, length: c.length, offset: c.offset || 0, fadeIn: c.fadeIn || 0, fadeOut: c.fadeOut || 0, gain: c.gain !== undefined ? c.gain : 1 });
      }
    }
    try {
      var p = window.EngineBridge.scheduleSamples(clips, this.bpm);
      if (p && typeof p.catch === "function") {
        p.catch(function (err) {
          if (UI.toast) UI.toast("⚠ 音频素材调度失败：" + (err && err.message || err), "warn");
        });
      }
    } catch (e) {}
  };

  /** 调度签名：坐标/长度/offset/mute/fade 任一变化都算——用于跳过
      音量拖动等不影响素材表的 applyMixSafe 调用 */
  Arrange.prototype.sampleScheduleSig = function () {
    var parts = [];
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
  Arrange.prototype.rescheduleSamplesDebounced = function () {
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

  Arrange.prototype.renderAll = function () {
    this.updateContentWidth();
    this.renderTracks();
    this.resizeRulerCanvas();
    this.renderRuler();
    this.updatePlayline();
    this.updatePosDisplay();
  };

  /** 内容宽度：最后片段末端 + 余量，最小 64 小节 */
  Arrange.prototype.updateContentWidth = function () {
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

  Arrange.prototype.renderTracks = function () {
    var lanesEl = this.el.arrLanes;
    if (!lanesEl) return;
    var self = this;
    this.meterEls = {};
    // 释放对旧剪辑元素的观察（IO 持强引用，不 unobserve 则随每次重建累积泄漏）
    if (this.thumbObserver) {
      var old = lanesEl.querySelectorAll(".arr-clip");
      for (var oi = 0; oi < old.length; oi++) this.thumbObserver.unobserve(old[oi]);
    }
    lanesEl.innerHTML = "";

    this.tracks.forEach(function (track, idx) {
      var row = document.createElement("div");
      row.className = "arr-lane-row" + (idx === self.selectedTrackIdx ? " selected-track" : "");
      row.dataset.trackIdx = idx;
      // 行高由 CSS .arr-lane-row 统一控制（72px），此处不再设置

      row.appendChild(self.buildTrackHead(track, idx));

      var content = document.createElement("div");
      content.className = "arr-lane-content";
      content.dataset.trackIdx = idx;
      content.style.setProperty("--bar-px", (self.ppb * BAR_BEATS) + "px");

      track.clips.forEach(function (clip) {
        content.appendChild(self.buildClipEl(track, clip));
      });

      row.appendChild(content);
      lanesEl.appendChild(row);
    });
  };

  Arrange.prototype.buildTrackHead = function (track, idx) {
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
    var volSnapshot = null;
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

  Arrange.prototype.sourceBadgeText = function (track) {
    var s = track.source || {};
    if (s.label) return "♪ " + s.label;
    if (s.type === "sf2") return "♪ SF2 音源";
    if (s.type === "builtin") return s.tone === "strings" ? "♪ 内置 · 弦乐群" : "♪ 内置 · 温暖钢琴";
    var waves = { sawtooth: "锯齿波", square: "方波", triangle: "三角波", sine: "正弦波" };
    return "♪ 合成器 · " + (waves[s.wave] || s.wave || "锯齿波");
  };

  /* ═══════════ Clip 元素 ═══════════ */

  Arrange.prototype.buildClipEl = function (track, clip) {
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

  Arrange.prototype.positionClipEl = function (el, clip) {
    el.style.width = Math.max(6, clip.length * this.ppb) + "px";
    el.style.transform = "translateX(" + (clip.start * this.ppb) + "px)";
  };

  /** 刷新单个片段元素的外观状态与几何 */
  Arrange.prototype.refreshClipEl = function (clipEl, clip) {
    if (!clipEl) return;
    clipEl.classList.toggle("muted", !!clip.mute);
    clipEl.classList.toggle("selected", this.selectedClips.indexOf(clip.id) !== -1);
    this.positionClipEl(clipEl, clip);
    this.drawThumbFor(clipEl);
  };

  /** 缩略图绘制（IntersectionObserver 驱动；key 变化才重绘） */
  Arrange.prototype.drawThumbFor = function (el) {
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

  Arrange.prototype.drawMidiThumb = function (ctx, track, clip, w, h) {
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

  Arrange.prototype.drawAudioThumb = function (ctx, track, clip, w, h, spb) {
    var peaks = clip._peaks;
    if (!peaks && clip.src && clip.src.p) {
      // 未解码过：异步取峰值后重绘一次
      var self = this;
      var rev = clip._rev || 0;
      this.engine.resume();
      this.engine.getSampleEntry(clip.src.p).then(function (entry) {
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

  Arrange.prototype.invalidateThumbByClipId = function (clipId) {
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

  Arrange.prototype.renderAllThumbsSoon = function () {
    var self = this;
    clearTimeout(this._thumbTimer);
    this._thumbTimer = setTimeout(function () {
      if (!self.el.arrLanes) return;
      var els = self.el.arrLanes.querySelectorAll(".arr-clip");
      for (var i = 0; i < els.length; i++) self.drawThumbFor(els[i]);
    }, 120);
  };

  /* ═══════════ 标尺渲染（仅可视区，避免超大画布） ═══════════ */

  Arrange.prototype.resizeRulerCanvas = function () {
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

  Arrange.prototype.renderRuler = function () {
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

  Arrange.prototype.updatePlayline = function () {
    if (this.el.arrPlayline) {
      this.el.arrPlayline.hidden = false;
      this.el.arrPlayline.style.transform = "translateX(" + (this.playheadBeat * this.ppb) + "px)";
    }
  };

  Arrange.prototype.fmtPos = function (beat) {
    var bar = Math.floor(beat / BAR_BEATS) + 1;
    var bt = Math.floor(beat % BAR_BEATS) + 1;
    return bar + "." + bt;
  };

  Arrange.prototype.updatePosDisplay = function () {
    if (this.el.arrPosDisplay) {
      this.el.arrPosDisplay.textContent = this.fmtPos(this.playheadBeat);
    }
  };

  Arrange.prototype.startUILoop = function () {
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
  Arrange.prototype.followPlayhead = function (beat) {
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

  Arrange.prototype.stopUILoop = function () {
    if (this.rafId) { cancelAnimationFrame(this.rafId); this.rafId = null; }
    this.decayMeters();
  };

  Arrange.prototype.updateMeters = function () {
    for (var i = 0; i < this.tracks.length; i++) {
      var t = this.tracks[i];
      var barEl = this.meterEls[t.id];
      if (!barEl) continue;
      var level = this.engine.trackLevel(t.id);
      barEl.style.transform = "scaleX(" + clamp(level * 1.25, 0, 1) + ")";
    }
  };

  Arrange.prototype.decayMeters = function () {
    for (var id in this.meterEls) {
      this.meterEls[id].style.transform = "scaleX(0)";
    }
  };

  /* ═══════════ HUD 提示徽章 ═══════════ */

  Arrange.prototype.showHUD = function (text) {
    var el = this.el.arrHudBadge;
    if (!el) return;
    el.textContent = text;
    el.classList.remove("hud-pop");
    void el.offsetWidth;
    el.classList.add("visible", "hud-pop");
    var self = this;
    clearTimeout(this.hudTimer);
    this.hudTimer = setTimeout(function () {
      el.classList.remove("visible", "hud-pop");
    }, 1300);
  };

  /* ═══════════ 选择状态 ═══════════ */

  Arrange.prototype.selectOnly = function (clipId) {
    this.selectedClips = [clipId];
    this.refreshSelectionClasses();
  };

  Arrange.prototype.toggleClipSelect = function (clipId) {
    var i = this.selectedClips.indexOf(clipId);
    if (i === -1) this.selectedClips.push(clipId);
    else this.selectedClips.splice(i, 1);
    this.refreshSelectionClasses();
  };

  Arrange.prototype.clearSelection = function () {
    if (!this.selectedClips.length) return;
    this.selectedClips = [];
    this.refreshSelectionClasses();
  };

  Arrange.prototype.selectAllClips = function () {
    this.selectedClips = [];
    this.tracks.forEach(function (t) {
      t.clips.forEach(function (c) {
        this.selectedClips.push(c.id);
      }, this);
    }, this);
    this.refreshSelectionClasses();
    this.showHUD("全选 (" + this.selectedClips.length + ")");
  };

  Arrange.prototype.refreshSelectionClasses = function () {
    if (!this.el.arrLanes) return;
    var els = this.el.arrLanes.querySelectorAll(".arr-clip");
    for (var i = 0; i < els.length; i++) {
      els[i].classList.toggle("selected", this.selectedClips.indexOf(els[i].dataset.clipId) !== -1);
    }
  };

  /** ↑↓ 键切换选中轨道 */
  Arrange.prototype.moveTrackSelection = function (dir) {
    var idx = clamp(this.selectedTrackIdx + dir, 0, this.tracks.length - 1);
    if (idx === this.selectedTrackIdx) return;
    this.selectTrack(idx);
    this.showHUD(this.tracks[idx].name);
  };

  Arrange.prototype.selectTrack = function (idx) {
    if (isNaN(idx) || !this.tracks[idx]) return;
    this.selectedTrackIdx = idx;
    if (!this.el.arrLanes) return;
    var rows = this.el.arrLanes.querySelectorAll(".arr-lane-row");
    for (var i = 0; i < rows.length; i++) {
      rows[i].classList.toggle("selected-track", Number(rows[i].dataset.trackIdx) === idx);
    }
  };

  /* ═══════════ Clip 编辑操作 ═══════════ */

  Arrange.prototype.deleteSelected = function () {
    if (!this.selectedClips.length) return;
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

  Arrange.prototype.copySelection = function (cut) {
    if (!this.selectedClips.length) return;
    if (cut) this.pushHistory();
    var ids = this.selectedClips.slice();
    var minStart = Infinity;
    var entries = [];
    var self = this;
    /* 记录每个片段的源轨道：粘贴时按相对轨距落轨，跨轨信息不再丢失 */
    this.forEachSelectedClip(function (track, clip) {
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

  Arrange.prototype.cutSelection = function () { this.copySelection(true); };

  Arrange.prototype.deleteSelectedSilent = function (ids) {
    this.tracks.forEach(function (t) {
      t.clips = t.clips.filter(function (c) { return ids.indexOf(c.id) === -1; });
    });
    this.selectedClips = [];
    this.renderTracks();
    this.updateContentWidth();
    this.scheduleSave();
  };

  Arrange.prototype.pasteClipboard = function () {
    var cb = this.clipboard;
    if (!cb || !cb.entries || !cb.entries.length) {
      this.showHUD("剪贴板为空");
      return;
    }
    this.pushHistory();
    var base = this.snapBeat(this.playheadBeat);
    var targetIdx = clamp(this.selectedTrackIdx, 0, this.tracks.length - 1);
    var self = this;
    cb.entries.forEach(function (en) {
      /* 按相对源轨距落轨（FL Playlist 惯例）：跨轨复制的片段回到对应轨；
         轨道不足时收敛到底部可用轨 */
      var ti = clamp(targetIdx + (en.srcTrack - cb.baseSrc), 0, self.tracks.length - 1);
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

  Arrange.prototype.duplicateSelection = function () {
    if (!this.selectedClips.length) return;
    this.pushHistory();
    var newIds = [];
    this.forEachSelectedClip(function (track, clip) {
      var copy = JSON.parse(JSON.stringify(clip));
      copy.id = uid("clip");
      delete copy._peaks;
      track.clips.push(copy);
      newIds.push(copy.id);
    });
    this.selectedClips = newIds;
    this.renderTracks();
    this.updateContentWidth();
    this.showHUD("⧉ 原地克隆 (" + newIds.length + ")");
    this.scheduleSave();
  };

  /** Ctrl+B：向右顺延复制（每个片段克隆到其原位置右侧一个长度处） */
  Arrange.prototype.repeatRight = function () {
    if (!this.selectedClips.length) return;
    this.pushHistory();
    var newIds = [];
    this.forEachSelectedClip(function (track, clip) {
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
    this.showHUD("⇥ 向右顺延复制");
    this.scheduleSave();
  };

  /** 片段内相对拍 rel 是否可分割（两侧至少留 MIN_CLIP_LEN） */
  Arrange.prototype.canSplitRel = function (clip, rel) {
    return rel > MIN_CLIP_LEN && rel < clip.length - MIN_CLIP_LEN;
  };

  /** 在绝对拍 atBeat 处把 track 上第 i 个 clip 切成两段。
      audio：右半 offset 前移；midi：跨界音符左右各截一段。
      成功返回 true（含 pushHistory 与视图/引擎同步） */
  Arrange.prototype.splitClipAtBeat = function (track, clip, atBeat) {
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
      var leftNotes = [], rightNotes = [];
      (clip.notes || []).forEach(function (n) {
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
  Arrange.prototype.splitClipAtCursor = function (track, clip, beatAtCursor) {
    if (!track || !clip) return false;
    /* 落点吸附到网格再尝试；太贴边时退回未吸附值 */
    var snapped = this.snapBeat(Math.max(0, beatAtCursor));
    if (this.splitClipAtBeat(track, clip, snapped)) return true;
    if (this.splitClipAtBeat(track, clip, Math.max(0, beatAtCursor))) return true;
    this.showHUD("⚠ 拆分点太靠近边缘");
    return false;
  };

  /** S 键入口：在播放头处拆分所有被选中的片段 */
  Arrange.prototype.splitSelectedAtPlayhead = function () {
    if (!this.selectedClips.length) { this.showHUD("先选中要拆分的片段"); return; }
    var playhead = this.playheadBeat;
    var hits = 0, misses = 0;
    var self = this;
    this.forEachSelectedClip(function (track, clip) {
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

  Arrange.prototype.addTrack = function () {
    this.pushHistory();
    this.tracks.push(this.makeTrack(this.tracks.length + 1));
    this.applyMixSafe();
    this.renderAll();
    this.selectTrack(this.tracks.length - 1);
    this.showHUD("＋ 新建轨道: " + this.tracks[this.tracks.length - 1].name);
    this.scheduleSave();
  };

  Arrange.prototype.removeTrack = function (idx) {
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
        try { nodes.gain.disconnect(); nodes.analyser.disconnect(); } catch (e) {}
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

  Arrange.prototype.duplicateTrack = function (idx) {
    var src = this.tracks[idx];
    if (!src) return;
    this.pushHistory();
    var copy = JSON.parse(JSON.stringify(src));
    copy.id = uid("tr");
    copy.name = src.name + " 副本";
    copy.clips.forEach(function (c) { c.id = uid("clip"); delete c._peaks; });
    this.tracks.splice(idx + 1, 0, copy);
    this.applyMixSafe();
    this.renderAll();
    this.showHUD("⧉ 已复制轨道");
    this.scheduleSave();
  };

  Arrange.prototype.moveTrack = function (idx, dir) {
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

  Arrange.prototype.clearTrackClips = function (idx) {
    var track = this.tracks[idx];
    if (!track || !track.clips.length) return;
    var self = this;
    this.openConfirm("清空剪辑", "确定清空轨道「" + track.name + "」的全部剪辑？", function () {
      self.pushHistory();
      track.clips = [];
      self.renderTracks();
      self.updateContentWidth();
      self.scheduleSave();
    });
  };

  /** 定点刷新轨道头按钮态（mute/solo 切换不再全量重建轨道区 DOM） */
  Arrange.prototype.refreshTrackHeadStates = function () {
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

  Arrange.prototype.toggleMute = function (idx) {
    var track = this.tracks[idx];
    if (!track) return;
    track.mute = !track.mute;
    this.applyMixSafe();
    this.refreshTrackHeadStates();
    this.showHUD(track.name + ": " + (track.mute ? "静音" : "取消静音") + " (M)");
    this.scheduleSave();
  };

  Arrange.prototype.toggleSolo = function (idx) {
    var track = this.tracks[idx];
    if (!track) return;
    track.solo = !track.solo;
    this.applyMixSafe();
    this.refreshTrackHeadStates();
    this.showHUD(track.name + ": " + (track.solo ? "独奏" : "取消独奏") + " (S)");
    this.scheduleSave();
  };

  /* ═══════════ 右键菜单体系 ═══════════ */

  Arrange.prototype.closeMenu = function () {
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
  Arrange.prototype.showMenu = function (items, x, y) {
    this.closeMenu();
    var self = this;
    var menu = document.createElement("div");
    menu.className = "ctx-menu menu-in";

    items.forEach(function (a) {
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

    this.menuOutsideHandler = function (ev) {
      if (menu && !menu.contains(ev.target)) self.closeMenu();
    };
    setTimeout(function () {
      document.addEventListener("mousedown", self.menuOutsideHandler, true);
    }, 0);
  };

  /** 轨道头右键菜单 */
  Arrange.prototype.openTrackMenu = function (e, idx) {
    var self = this;
    var track = this.tracks[idx];
    if (!track) return;
    var items = [
      { label: "✎ 重命名 (F2)", action: function () { self.openRenameModal(idx); } },
      { label: "🎨 更换颜色 ▸", action: function () { self.openColorMenu(e.clientX, e.clientY, idx); } },
      { label: "🎹 加载音源 ▸", action: function () { self.openSourceMenu(track, e.clientX, e.clientY); } },
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
  Arrange.prototype.openBlankMenu = function (e) {
    var self = this;
    var items = [
      { label: "＋ 新建轨道", action: function () { self.addTrack(); } }
    ];
    if (this.clipboard) {
      items.push("-");
      items.push({ label: "⎘ 粘贴", action: function () { self.pasteClipboard(); } });
    }
    this.showMenu(items, e.clientX, e.clientY);
  };

  /** Clip 右键菜单 */
  Arrange.prototype.openClipMenu = function (e, clipEl) {
    var self = this;
    var found = this.locateClip(clipEl);
    if (!found) return;
    var clip = found.clip;
    var beatAtCursor = this.clientXToBeat(e.clientX);
    var relAtCursor = beatAtCursor - clip.start;
    this.showMenu([
      { label: "✂ 在此拆分 (S)", disabled: !this.canSplitRel(clip, Math.max(0, relAtCursor)), action: function () {
        self.splitClipAtCursor(found.track, clip, beatAtCursor);
      } },
      "-",
      { label: clip.mute ? "🔇 取消片段静音" : "🔇 片段静音", action: function () {
        self.pushHistory();
        clip.mute = !clip.mute;
        self.refreshClipEl(clipEl, clip);
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

  /** 轨道音源菜单：内置合成器 + 内置音色 + SF2 音源库（优先使用同步缓存，消除闪烁） */
  Arrange.prototype.openSourceMenu = function (track, x, y) {
    var self = this;
    var baseItems = [
      { heading: "🎹 音源 · " + track.name },
      { label: "合成器 · 锯齿波", iconHtml: this._srcDot("#00B8CC"), action: function () { self.setTrackSource(track, { type: "synth", wave: "sawtooth", label: "合成器 · 锯齿波" }); } },
      { label: "合成器 · 方波", iconHtml: this._srcDot("#5B8DEF"), action: function () { self.setTrackSource(track, { type: "synth", wave: "square", label: "合成器 · 方波" }); } },
      { label: "合成器 · 三角波", iconHtml: this._srcDot("#06D6A0"), action: function () { self.setTrackSource(track, { type: "synth", wave: "triangle", label: "合成器 · 三角波" }); } },
      { label: "合成器 · 正弦波", iconHtml: this._srcDot("#FFD166"), action: function () { self.setTrackSource(track, { type: "synth", wave: "sine", label: "合成器 · 正弦波" }); } },
      "-",
      { label: "内置音色 · 温暖钢琴", action: function () { self.setTrackSource(track, { type: "builtin", tone: "piano", label: "内置 · 温暖钢琴" }); } },
      { label: "内置音色 · 弦乐群", action: function () { self.setTrackSource(track, { type: "builtin", tone: "strings", label: "内置 · 弦乐群" }); } }
    ];

    function buildExtraItems(fonts) {
      if (!fonts || !fonts.length) return [];
      var extra = [{ heading: "SF2 音源库（在钢琴卷帘音源库中管理）" }];
      fonts.forEach(function (f) {
        (f.presets || []).slice(0, 24).forEach(function (p) {
          extra.push({
            label: (f.name + " › " + (p.name || "Preset")).slice(0, 46),
            action: function () {
              self.setTrackSource(track, { type: "sf2", libId: f.id, presetId: p.id, label: (p.name || f.name).slice(0, 24) });
            }
          });
        });
      });
      return extra;
    }

    // 优先读取缓存，一次性完整渲染，避免先弹出一半又闪烁重绘
    var cachedFonts = window.SoundLibrary && window.SoundLibrary.getCachedSoundFonts ? window.SoundLibrary.getCachedSoundFonts() : [];
    var initialItems = baseItems.concat(buildExtraItems(cachedFonts));
    this.showMenu(initialItems, x, y);

    // 后台静默校验，仅在列表有变化且菜单仍打开时平滑更新
    if (window.SoundLibrary) {
      window.SoundLibrary.listSoundFonts().then(function (fonts) {
        if (!self.menuEl) return;
        if (JSON.stringify(fonts) !== JSON.stringify(cachedFonts)) {
          var updatedItems = baseItems.concat(buildExtraItems(fonts));
          var rect = self.menuEl.getBoundingClientRect();
          self.showMenu(updatedItems, rect.left, rect.top);
        }
      }).catch(function () {});
    }
  };

  Arrange.prototype._srcDot = function (color) {
    return '<span class="arr-ctx-swatch" style="background:' + color + '"></span>';
  };

  Arrange.prototype.setTrackSource = function (track, source) {
    this.pushHistory();
    track.source = source;
    // 触发引擎重建该轨发声链
    var nodes = this.engine.trackNodes[track.id];
    if (nodes) nodes.sourceKey = null;
    this.engine.ensureTrack(track);
    this.applyMixSafe();
    this.renderTracks();
    this.showHUD("♪ 音源: " + this.sourceBadgeText(track).replace("♪ ", ""));
    this.scheduleSave();
  };

  /** 轨道颜色菜单 */
  Arrange.prototype.openColorMenu = function (x, y, idx) {
    var self = this;
    var track = this.tracks[idx];
    if (!track) return;
    var items = [{ heading: "🎨 轨道颜色 · " + track.name }];
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

  Arrange.prototype.isArrangeDrag = function (e) {
    var types = e.dataTransfer && e.dataTransfer.types;
    if (!types) return false;
    for (var i = 0; i < types.length; i++) {
      if (types[i] === "text/plain" || types[i] === "application/x-arrange" || types[i] === "application/x-arrange-source") return true;
    }
    return false;
  };

  /** 本次拖拽是否为机架音源（替换轨道音源），而非 MIDI/音频素材入库 */
  Arrange.prototype.isSourceDrag = function (e) {
    var types = e.dataTransfer && e.dataTransfer.types;
    if (!types) return false;
    for (var i = 0; i < types.length; i++) {
      if (types[i] === "application/x-arrange-source") return true;
    }
    return false;
  };

  /** 音源拖拽反馈：高亮悬停轨道整行（行 = 头 + 轨道内容），示意「替换该轨音源」 */
  Arrange.prototype.updateSourceHighlight = function (e) {
    var row = null;
    if (e.target && e.target.closest) {
      var headEl = e.target.closest(".arr-track-head");
      var contentEl = headEl ? null : e.target.closest(".arr-lane-content");
      if (headEl) row = headEl.closest(".arr-lane-row");
      else if (contentEl) row = contentEl.closest(".arr-lane-row");
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

  Arrange.prototype.clearSourceHighlight = function () {
    if (this.hoverSourceEl) {
      this.hoverSourceEl.classList.remove("drag-over-source");
      this.hoverSourceEl = null;
    }
  };

  Arrange.prototype.updateDropHighlight = function (e) {
    var target = e.target.closest
      ? (e.target.closest(".arr-track-head") || e.target.closest(".arr-lane-content"))
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

  Arrange.prototype.clearDropHighlight = function () {
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
  Arrange.prototype.resolveDropTarget = function (e) {
    var headEl = e.target && e.target.closest ? e.target.closest(".arr-track-head") : null;
    var contentEl = e.target && e.target.closest ? e.target.closest(".arr-lane-content") : null;
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
  Arrange.prototype.updateDropGhost = function (target) {
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

  Arrange.prototype.hideDropGhost = function () {
    if (this.dropGhostEl) {
      this.dropGhostEl.remove();
      this.dropGhostEl = null;
    }
  };

  Arrange.prototype.handleDrop = function (e) {
    var raw = e.dataTransfer.getData("application/x-arrange") || e.dataTransfer.getData("text/plain");
    if (!raw) return;
    var data;
    try { data = JSON.parse(raw); } catch (err) { return; }
    if (!data || (data.kind !== "arr-midi" && data.kind !== "arr-audio" && data.kind !== "arr-source")) return;

    // 落点与剪影同一份计算（resolveDropTarget），放置位置即拖动所见
    var target = this.resolveDropTarget(e);

    if (data.kind === "arr-source") {
      this.replaceTrackSourceFromDrop(target.trackIdx, data.source);
    } else if (data.kind === "arr-midi") {
      this.addMidiClipFromDrop(target.trackIdx, data.name, target.startBeat);
    } else {
      this.addAudioClipFromDrop(target.trackIdx, data.p, data.name, target.startBeat);
    }
  };

  /** 拖拽音源 → 替换指定轨道音源 */
  Arrange.prototype.replaceTrackSourceFromDrop = function (trackIdx, source) {
    if (!source) return;
    var track = this.tracks[trackIdx];
    if (!track) {
      // 拖到空白区域则新增一条轨道
      track = this.makeTrack(this.tracks.length + 1);
      track.source = source;
      track.name = source.label ? source.label.replace(/^.*?·\s*/, "") : "Track " + this.tracks.length;
      this.pushHistory();
      this.tracks.push(track);
      this.engine.ensureTrack(track);
      this.applyMixSafe();
      this.renderTracks();
      this.scheduleSave();
      this.showHUD("➕ 新建轨道并设置音源: " + (source.label || "音源"));
      return;
    }
    this.pushHistory();
    track.source = source;
    // 与 setTrackSource 相同的引擎重建：置空 sourceKey 让 ensureTrack 重建该轨发声链
    var nodes = this.engine.trackNodes[track.id];
    if (nodes) nodes.sourceKey = null;
    this.engine.ensureTrack(track);
    this.applyMixSafe();
    this.renderTracks();
    this.scheduleSave();
    this.showHUD("🎛 轨道 [" + track.name + "] 音源已替换为: " + (source.label || "音源"));
  };

  /** 拖入 MIDI 文件 → 下载字节 → 前端解析 → 创建 MIDI Clip */
  Arrange.prototype.addMidiClipFromDrop = function (trackIdx, fileName, startBeat) {
    var self = this;
    var track = this.tracks[trackIdx];
    if (!track || !fileName) return;
    fetch("/api/projects/" + encodeURIComponent(this.projectId) + "/download?names=" + encodeURIComponent(fileName))
      .then(function (res) {
        if (!res.ok) throw new Error("获取 MIDI 文件失败");
        return res.arrayBuffer();
      })
      .then(function (buf) {
        var notes = MidiParse.parseBytes(buf).filter(function (n) { return n.end > n.start; });
        if (!notes.length) {
          UI.toast("⚠ 该文件未解析出音符", "warn");
          return;
        }
        var maxEnd = 0;
        notes.forEach(function (n) { maxEnd = Math.max(maxEnd, n.end); });
        var length = Math.max(BAR_BEATS, Math.ceil(maxEnd / BAR_BEATS) * BAR_BEATS);
        self.pushHistory();
        track.clips.push({
          id: uid("clip"),
          type: "midi",
          name: baseName(fileName),
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
        UI.toast("✗ 拖入 MIDI 失败: " + err.message, "err");
      });
  };

  /** 拖入音频素材 → 解码取时长 → 创建音频 Clip（含波形峰值缓存） */
  Arrange.prototype.addAudioClipFromDrop = function (trackIdx, absPath, name, startBeat) {
    var self = this;
    var track = this.tracks[trackIdx];
    if (!track || !absPath) return;
    this.engine.resume();
    this.engine.getSampleEntry(absPath).then(function (entry) {
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
    }).catch(function (err) {
      UI.toast("✗ 拖入素材失败: " + err.message, "err");
    });
  };

  /* ═══════════ 左栏：项目 MIDI 文件列表 ═══════════ */

  Arrange.prototype.renderMidiList = function () {
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
    var sorted = this.midiFiles.slice().sort(function (a, b) {
      return String(a.name).localeCompare(String(b.name), "zh-CN");
    });
    sorted.forEach(function (f) {
      var item = document.createElement("div");
      item.className = "arr-midi-item";
      item.draggable = true;
      item.title = f.name + "（拖拽到轨道放置 / 双击在钢琴卷帘打开）";

      var glyph = document.createElement("span");
      glyph.className = "mi-glyph";
      glyph.textContent = "♪";
      var nameSpan = document.createElement("span");
      nameSpan.className = "mi-name";
      nameSpan.textContent = baseName(f.name);
      var dirSpan = document.createElement("span");
      dirSpan.className = "mi-dir";
      var dirPart = String(f.name).indexOf("/") !== -1 ? String(f.name).slice(0, String(f.name).lastIndexOf("/")) : "";
      dirSpan.textContent = dirPart;

      item.appendChild(glyph);
      item.appendChild(nameSpan);
      if (dirPart) item.appendChild(dirSpan);

      item.addEventListener("dragstart", function (e) {
        var payload = JSON.stringify({ kind: "arr-midi", name: f.name });
        e.dataTransfer.setData("application/x-arrange", payload);
        e.dataTransfer.setData("text/plain", payload);
        e.dataTransfer.effectAllowed = "copy";
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

      wrap.appendChild(item);
    });
  };

  /* ═══════════ 右栏：素材库（树状浏览器，FL Browser 式多根惰性展开） ═══════════ */

  Arrange.prototype.TREE_EXPANDED_KEY = "arr-tree-expanded";

  Arrange.prototype.nodeKey = function (dir, sub) {
    return sub ? dir + "/" + sub : dir;
  };

  Arrange.prototype.loadExpandedMap = function () {
    try { return JSON.parse(localStorage.getItem(this.TREE_EXPANDED_KEY) || "{}") || {}; }
    catch (e) { return {}; }
  };

  Arrange.prototype.saveExpandedMap = function () {
    try { localStorage.setItem(this.TREE_EXPANDED_KEY, JSON.stringify(this.expandedMap || {})); } catch (e) {}
  };

  /** 单层目录内容拉取（Go API 即单层语义），成功后写入节点缓存 */
  Arrange.prototype.fetchTreeNode = function (dir, sub, cb) {
    var self = this;
    var url = "/api/arrangement/files?dir=" + encodeURIComponent(dir);
    if (sub) url += "&sub=" + encodeURIComponent(sub);
    UI.getJSON(url).then(function (r) {
      self._treeCache[self.nodeKey(dir, sub)] = { dirs: r.dirs || [], files: r.files || [] };
      cb(null);
    }).catch(function (err) {
      cb(err);
    });
  };

  Arrange.prototype.loadMaterialDirs = function () {
    var self = this;
    UI.getJSON("/api/arrangement/dirs").then(function (r) {
      self.dirs = r.dirs || [];
      self.renderMaterialTree();
      self.refreshOpenTreeNodes();
    }).catch(function () {});
  };

  /** 上次会话记住了「已展开」但缺缓存的根目录 → 静默补拉并重绘 */
  Arrange.prototype.refreshOpenTreeNodes = function () {
    var self = this;
    var touched = false;
    for (var i = 0; i < this.dirs.length; i++) {
      var d = this.dirs[i];
      if (this.expandedMap && this.expandedMap[d] && !this._treeCache[d]) {
        touched = true;
        this._treeCache[d] = { dirs: [], files: [], _loading: true };
        /* 捕获当前根，回调里只清理自己的占位 */
        (function (root) {
          self.fetchTreeNode(root, "", function () { });
        })(d);
      }
    }
    if (touched) this.renderMaterialTree();
  };

  Arrange.prototype.removeMaterialDir = function (d) {
    var self = this;
    UI.delJSON("/api/arrangement/dirs", { path: d }).then(function (r) {
      self.dirs = r.dirs || [];
      [self._treeCache, self.expandedMap].forEach(function (m) {
        Object.keys(m || {}).forEach(function (k) {
          if (k === d || k.indexOf(d + "/") === 0) delete m[k];
        });
      });
      self.saveExpandedMap();
      self.renderMaterialTree();
    }).catch(function (err) {
      UI.toast("✗ 移除失败: " + err.message, "err");
    });
  };

  Arrange.prototype.toggleTreeNode = function (dir, sub) {
    var self = this;
    if (!this.expandedMap) this.expandedMap = this.loadExpandedMap();
    var key = this.nodeKey(dir, sub);
    if (this.expandedMap[key]) {
      delete this.expandedMap[key];
      this.saveExpandedMap();
      this.renderMaterialTree();
      return;
    }
    this.expandedMap[key] = true;
    this.saveExpandedMap();
    if (this._treeCache[key]) {
      this.renderMaterialTree();
      return;
    }
    /* 占位缓存让箭头先转为"加载中"，取回后统一重绘 */
    this._treeCache[key] = { dirs: [], files: [], _loading: true };
    this.renderMaterialTree();
    this.fetchTreeNode(dir, sub, function (err) {
      if (err) {
        delete self._treeCache[key];
        delete self.expandedMap[key];
        self.saveExpandedMap();
        UI.toast("✗ 读取目录失败", "err");
      }
      self.renderMaterialTree();
    });
  };

  Arrange.prototype.renderMaterialTree = function () {
    var tree = this.el.arrFileTree;
    if (!tree) return;
    tree.innerHTML = "";
    if (!this.expandedMap) this.expandedMap = this.loadExpandedMap();

    if (!this.dirs.length) {
      var tip = document.createElement("div");
      tip.className = "arr-empty-tip";
      tip.innerHTML = "点击「＋ 文件夹」添加本地<br>采样包目录（wav / mp3 / ogg / flac）";
      tree.appendChild(tip);
      return;
    }

    for (var i = 0; i < this.dirs.length; i++) {
      this.appendTreeNode(tree, this.dirs[i], "", 0);
    }
  };

  /** 一个目录节点行；展开且缓存就绪时在下方递归挂载子节点 */
  Arrange.prototype.appendTreeNode = function (container, dir, sub, depth) {
    var self = this;
    var key = this.nodeKey(dir, sub);
    var expanded = !!this.expandedMap[key];
    var cached = this._treeCache[key];

    var row = document.createElement("div");
    row.className = "arr-file-item dir" + (expanded ? " open" : "");
    row.style.paddingLeft = (8 + depth * 14) + "px";
    row.title = key;

    var arrow = document.createElement("span");
    arrow.className = "fi-glyph";
    arrow.textContent = cached && cached._loading ? "…" : (expanded ? "▾" : "▸");
    row.appendChild(arrow);

    var nameSpan = document.createElement("span");
    nameSpan.className = "fi-name";
    nameSpan.textContent = baseName(sub || dir) || dir;
    row.appendChild(nameSpan);

    row.addEventListener("click", function () {
      self.toggleTreeNode(dir, sub);
    });

    if (depth === 0) {
      var del = document.createElement("span");
      del.className = "chip-del";
      del.textContent = "✕";
      del.title = "从素材库移除该目录（不删除磁盘文件）";
      del.addEventListener("click", function (e) {
        e.stopPropagation();
        self.removeMaterialDir(dir);
      });
      row.appendChild(del);
    }

    container.appendChild(row);
    if (!expanded || !cached || cached._loading) return;

    var childWrap = document.createElement("div");
    childWrap.className = "arr-tree-children";
    container.appendChild(childWrap);

    if (!cached.dirs.length && !cached.files.length) {
      var empty = document.createElement("div");
      empty.className = "arr-empty-tip";
      empty.style.paddingLeft = (8 + (depth + 1) * 14) + "px";
      empty.textContent = "空文件夹";
      childWrap.appendChild(empty);
    }
    for (var i = 0; i < cached.dirs.length; i++) {
      this.appendTreeNode(childWrap, dir, sub ? sub + "/" + cached.dirs[i].name : cached.dirs[i].name, depth + 1);
    }
    for (var j = 0; j < cached.files.length; j++) {
      childWrap.appendChild(this.buildSampleRow(dir, sub, cached.files[j], depth + 1));
    }
  };

  /** 音频叶子行：拖拽入轨 payload 与旧行为完全一致 */
  Arrange.prototype.buildSampleRow = function (dir, sub, f, depth) {
    var self = this;
    var absPath = this.joinAbs(dir, sub, f.name);
    var item = document.createElement("div");
    item.className = "arr-file-item sample";
    item.draggable = true;
    item.style.paddingLeft = (8 + depth * 14) + "px";
    item.title = f.name + "（拖到轨道创建音频 Clip，双击试听）";

    var glyph = document.createElement("span");
    glyph.className = "fi-glyph";
    glyph.textContent = "♫";
    var nameSpan = document.createElement("span");
    nameSpan.className = "fi-name";
    nameSpan.textContent = f.name;
    var sizeSpan = document.createElement("span");
    sizeSpan.className = "fi-size";
    sizeSpan.textContent = UI.fmtSize(f.size || 0);

    item.appendChild(glyph);
    item.appendChild(nameSpan);
    item.appendChild(sizeSpan);

    item.addEventListener("dragstart", function (e) {
      var payload = JSON.stringify({ kind: "arr-audio", p: absPath, name: f.name });
      e.dataTransfer.setData("application/x-arrange", payload);
      e.dataTransfer.setData("text/plain", payload);
      e.dataTransfer.effectAllowed = "copy";
      item.classList.add("dragging");
      var clear = function () {
        item.classList.remove("dragging");
        item.removeEventListener("dragend", clear);
      };
      item.addEventListener("dragend", clear);
    });
    item.addEventListener("dblclick", function () {
      self.engine.resume();
      self.engine.previewSample(absPath).catch(function () {
        if (UI.toast) UI.toast("✗ 音频试听失败（文件不存在或格式不支持）", "err");
      });
    });
    return item;
  };

  Arrange.prototype.bindPanels = function () {
    var self = this;
    if (this.el.arrAddDirBtn) {
      this.el.arrAddDirBtn.addEventListener("click", function () {
        UI.postJSON("/api/arrangement/pick-folder", {}).then(function (r) {
          if (!r.path) return; // 用户取消
          UI.postJSON("/api/arrangement/dirs", { path: r.path }).then(function (r2) {
            self.dirs = r2.dirs || [];
            if (!self.expandedMap) self.expandedMap = self.loadExpandedMap();
            self.expandedMap[r.path] = true;
            self.saveExpandedMap();
            self.renderMaterialTree();
            UI.toast("✓ 已添加素材目录", "ok");
          }).catch(function (err) {
            UI.toast("✗ 添加目录失败: " + err.message, "err");
          });
        }).catch(function (err) {
          UI.toast("✗ 打开文件夹选择器失败: " + err.message, "err");
        });
      });
    }
    // 标尺事件绑定（依赖 DOM 就绪）
    this.initRulerPointer();
  };

  Arrange.prototype.joinAbs = function (dir, sub, name) {
    var parts = [dir];
    if (sub) parts.push(sub);
    parts.push(name);
    return parts.filter(Boolean).join("/");
  };

  /* ═══════════ 弹窗（重命名 / 确认 / 快捷键） ═══════════ */

  Arrange.prototype.showOverlay = function (overlay) {
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

  Arrange.prototype.hideOverlay = function (overlay) {
    overlay.classList.remove("modal-in");
    overlay.classList.add("modal-out");
    var overlayRef = overlay;
    setTimeout(function () {
      overlayRef.classList.remove("modal-out");
      overlayRef.hidden = true;
    }, 220);
  };

  Arrange.prototype.bindModals = function () {
    var self = this;
    this._modalToken = 0;

    /* 早期测试版提示：sessionStorage 记忆（Esc 关闭不记，点「知道了」后本会话不再弹） */
    var betaOverlay = document.getElementById("arrBetaModal");
    var betaOk = document.getElementById("arrBetaOk");
    if (betaOverlay && betaOk) {
      betaOk.addEventListener("click", function () {
        try { sessionStorage.setItem("aimidi_arr_beta_v1", "1"); } catch (e) {}
        self.hideOverlay(betaOverlay);
      });
    }

    var renameOverlay = document.getElementById("arrRenameModal");
    var renameInput = document.getElementById("arrRenameInput");
    if (renameOverlay) {
      var doRename = function () {
        var idx = self._renameIdx;
        var track = self.tracks[idx];
        var val = renameInput.value.trim();
        if (track && val && val !== track.name) {
          self.pushHistory();
          track.name = val;
          self.renderTracks();
          self.scheduleSave();
        }
        self.hideOverlay(renameOverlay);
      };
      document.getElementById("arrRenameOk").addEventListener("click", doRename);
      document.getElementById("arrRenameCancel").addEventListener("click", function () {
        self.hideOverlay(renameOverlay);
      });
      renameInput.addEventListener("keydown", function (e) {
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
      document.getElementById("arrConfirmOk").addEventListener("click", function () {
        var fn = self._confirmFn;
        self.hideOverlay(confirmOverlay);
        self._confirmFn = null;
        if (fn) fn();
      });
      document.getElementById("arrConfirmCancel").addEventListener("click", cancelConfirm);
      /* 点遮罩 = 取消（与快捷键弹窗/聊天确认框一致） */
      confirmOverlay.addEventListener("mousedown", function (e) {
        if (e.target === confirmOverlay) cancelConfirm();
      });
    }

    var shortcutsOverlay = document.getElementById("arrShortcutsModal");
    if (shortcutsOverlay) {
      this.$("arrShortcutsBtn").addEventListener("click", function () {
        self.showOverlay(shortcutsOverlay);
        self.$("arrShortcutsBtn").blur();
      });
      document.getElementById("arrShortcutsClose").addEventListener("click", function () {
        self.hideOverlay(shortcutsOverlay);
      });
      shortcutsOverlay.addEventListener("mousedown", function (e) {
        if (e.target === shortcutsOverlay) self.hideOverlay(shortcutsOverlay);
      });
    }
  };

  Arrange.prototype.openRenameModal = function (idx) {
    var track = this.tracks[idx];
    var overlay = document.getElementById("arrRenameModal");
    var input = document.getElementById("arrRenameInput");
    if (!track || !overlay || !input) return;
    this._renameIdx = idx;
    input.value = track.name;
    this.showOverlay(overlay);
    setTimeout(function () { input.focus(); input.select(); }, 60);
  };

  /** 当前可见的最上层编排弹窗（无则 null） */
  Arrange.prototype._topOverlay = function () {
    var ids = ["arrConfirmModal", "arrRenameModal", "arrShortcutsModal", "arrBetaModal"];
    for (var i = ids.length - 1; i >= 0; i--) {
      var m = document.getElementById(ids[i]);
      if (m && !m.hidden) return m;
    }
    return null;
  };

  Arrange.prototype.openConfirm = function (title, text, fn) {
    var overlay = document.getElementById("arrConfirmModal");
    if (!overlay) { if (window.confirm) fn(); return; }
    document.getElementById("arrConfirmTitle").textContent = title;
    document.getElementById("arrConfirmText").textContent = text;
    this._confirmFn = fn;
    this.showOverlay(overlay);
  };

  /* ═══════════ 快捷键（FL Studio 风格 · 三域焦点仲裁） ═══════════ */

  /** 输入控件聚焦时不劫持按键 */
  Arrange.prototype.isEditableTarget = function (t) {
    if (!t) return false;
    var tag = t.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || t.isContentEditable;
  };

  /** 焦点位于可激活控件（按钮/链接）上：空格让位给浏览器默认点击语义 */
  Arrange.prototype.isActivatableTarget = function (t) {
    if (!t || !t.closest) return false;
    var tag = t.tagName;
    if (tag === "BUTTON" || tag === "A") return true;
    return !!t.closest('button, a, [role="button"]');
  };

  /** 钢琴卷帘抽屉持有焦点时让位（三域仲裁：最后点击区域优先） */
  Arrange.prototype.pianoRollOwnsKeys = function () {
    var pr = window.PianoRoll;
    return !!(pr && pr.isOpen && pr.isFocused);
  };

  Arrange.prototype.bindKeyboard = function () {
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

      // Esc 分层：确认/重命名弹窗（上方分支）→ 输出机架窗口
      if (k === "Escape" || k === "Esc") {
        var rackWin = document.getElementById("arrRackWindow");
        if (rackWin && !rackWin.hidden) {
          rackWin.hidden = true;
          return;
        }
      }

      // Space 走走带停；焦点在按钮/链接上时让位给浏览器激活语义
      // （否则机架的试听/分配等按钮无法用空格操作）
      if (e.code === "Space") {
        if (self.isActivatableTarget(e.target)) return;
        e.preventDefault();
        self.togglePlay();
        return;
      }

      if (k === "Home") { e.preventDefault(); self.seekTo(0, false); self.updatePlayButton(); return; }
      if (k === "End") { e.preventDefault(); self.seekTo(self.contentEndBeat(), false); return; }
      if (k === "ArrowLeft" || k === "ArrowRight") {
        e.preventDefault();
        var dir = k === "ArrowLeft" ? -1 : 1;
        var step = e.shiftKey ? BAR_BEATS : (self.snap || 0.25);
        self.seekTo(Math.max(0, self.playheadBeat + dir * step), false);
        return;
      }
      if (k === "ArrowUp" || k === "ArrowDown") {
        e.preventDefault();
        self.moveTrackSelection(k === "ArrowUp" ? -1 : 1);
        return;
      }
      if (!ctrl && (k === "m" || k === "M")) { self.toggleMute(self.selectedTrackIdx); return; }
      if (!ctrl && e.shiftKey && (k === "s" || k === "S")) { self.toggleSolo(self.selectedTrackIdx); return; }
      if (!ctrl && !e.shiftKey && (k === "s" || k === "S")) { self.splitSelectedAtPlayhead(); return; }
      if (!ctrl && (k === "l" || k === "L")) { self.toggleLoop(); return; }
      if (k === "F2") { e.preventDefault(); self.openRenameModal(self.selectedTrackIdx); return; }

      if (ctrl) {
        switch (k.toLowerCase()) {
          case "z":
            e.preventDefault();
            if (e.shiftKey) self.redo(); else self.undo();
            return;
          case "y": e.preventDefault(); self.redo(); return;
          case "c": e.preventDefault(); self.copySelection(false); return;
          case "x": e.preventDefault(); self.cutSelection(); return;
          case "v": e.preventDefault(); self.pasteClipboard(); return;
          case "d": e.preventDefault(); self.duplicateSelection(); return;
          case "b": e.preventDefault(); self.repeatRight(); return;
          case "a": e.preventDefault(); self.selectAllClips(); return;
          case "=": case "+": e.preventDefault(); self.zoomStep(1); return;
          case "-": e.preventDefault(); self.zoomStep(-1); return;
        }
        return;
      }

      if (k === "Delete" || k === "Backspace") { e.preventDefault(); self.deleteSelected(); return; }
      if (k === "+" || k === "=") { self.zoomStep(1); return; }
      if (k === "-" || k === "_") { self.zoomStep(-1); return; }
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

  Arrange.prototype.bindFocusManager = function () {
    var self = this;
    var ws = document.getElementById("arrangeWorkspace");

    document.addEventListener("mousedown", function (e) {
      var inWs = !!(ws && ws.contains(e.target));
      var drawer = document.getElementById("pianoDrawer");
      var inDrawer = !!(drawer && drawer.contains(e.target));
      if (inWs) self.setFocus(true);
      else if (!inDrawer) self.setFocus(false);
    }, true);

    var msgInput = document.getElementById("msgInput");
    if (msgInput) {
      msgInput.addEventListener("focus", function () { self.setFocus(false); });
    }
  };

  Arrange.prototype.setFocus = function (focused) {
    this.isFocused = !!(focused && this.isOpen);
  };

  /* ═══════════ 输出机架 (FL Studio 风格 Channel / Output Rack 悬浮窗口) ═══════════ */

  Arrange.prototype.bindOutputRack = function () {
    var self = this;
    var win = document.getElementById("arrRackWindow");
    var titlebar = document.getElementById("arrRackTitlebar");
    var btn = this.el.arrRackBtn || document.getElementById("arrRackBtn");
    var closeBtn = document.getElementById("arrRackCloseBtn");
    var collapseBtn = document.getElementById("arrRackCollapseBtn");
    var addSf2Btn = document.getElementById("arrRackAddSf2Btn");

    var toggleFn = function (e) {
      if (e) e.stopPropagation();
      self.toggleOutputRack();
    };

    if (btn) {
      btn.addEventListener("click", toggleFn);
    }

    if (closeBtn && win) {
      closeBtn.addEventListener("click", function () {
        win.hidden = true;
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
        if (e.target.closest(".rack-win-btn")) return;
        isDragging = true;
        startX = e.clientX;
        startY = e.clientY;
        var rect = win.getBoundingClientRect();
        initLeft = rect.left;
        initTop = rect.top;
        document.body.style.userSelect = "none";
      });

      window.addEventListener("mousemove", function (e) {
        if (!isDragging) return;
        var dx = e.clientX - startX;
        var dy = e.clientY - startY;
        var newLeft = Math.max(10, Math.min(window.innerWidth - win.offsetWidth - 10, initLeft + dx));
        var newTop = Math.max(10, Math.min(window.innerHeight - win.offsetHeight - 10, initTop + dy));
        win.style.left = newLeft + "px";
        win.style.top = newTop + "px";
      });

      window.addEventListener("mouseup", function () {
        if (isDragging) {
          isDragging = false;
          document.body.style.userSelect = "";
        }
      });
    }

    // 标签页过滤切换
    var filterBtns = document.querySelectorAll(".rack-filter-btn");
    filterBtns.forEach(function (tab) {
      tab.addEventListener("click", function () {
        filterBtns.forEach(function (t) { t.classList.remove("active"); });
        tab.classList.add("active");
        var filter = tab.getAttribute("data-tab") || "all";
        self.renderOutputRack(filter);
      });
    });

    if (addSf2Btn) {
      addSf2Btn.addEventListener("click", function () {
        var refreshRack = function () {
          self.showHUD("✅ 已导入音色库，可在 SF2 分类中查看");
          self.renderOutputRack(self._currentRackFilter || "all");
        };
        var fail = function (err) {
          self.showHUD("✗ 导入音色库失败: " + (err && err.message ? err.message : "存储空间不足或文件损坏"));
          if (window.UI && UI.toast) UI.toast("✗ 导入音色库失败: " + (err && err.message ? err.message : "未知错误"), "err");
        };
        if (window.SoundLibrary && window.SoundLibrary.showImportDialog) {
          window.SoundLibrary.showImportDialog(refreshRack);
        } else {
          var input = document.createElement("input");
          input.type = "file";
          input.accept = ".sf2";
          input.onchange = function (e) {
            var file = e.target.files && e.target.files[0];
            if (!file) return;
            if (!window.SoundLibrary) return;
            var reader = new FileReader();
            reader.onload = function () {
              var buf = reader.result;
              var presets = [];
              try {
                // 解析 presets（与聊天页音源库上传一致），失败不阻断保存
                if (window.PianoRoll && window.PianoRoll.soundfont && window.PianoRoll.soundfont.parseSF2) {
                  presets = window.PianoRoll.soundfont.parseSF2(buf).presets || [];
                }
              } catch (err) {
                // 解析失败仍按无预设保存，保证基础导入可用
                presets = [];
              }
              // 大文件写入 IndexedDB 可能触发配额错误：禁用按钮 + 补 catch，
              // 失败必须给出可见反馈（此前静默吞掉，HUD 却显示成功）
              var origLabel = addSf2Btn.textContent;
              addSf2Btn.disabled = true;
              addSf2Btn.textContent = "导入中…";
              window.SoundLibrary.saveSoundFont(file.name, buf, presets)
                .then(refreshRack)
                .catch(fail)
                .finally(function () {
                  addSf2Btn.disabled = false;
                  addSf2Btn.textContent = origLabel;
                });
            };
            reader.readAsArrayBuffer(file);
          };
          input.click();
        }
      });
    }
  };

  Arrange.prototype.toggleOutputRack = function () {
    var win = document.getElementById("arrRackWindow");
    if (!win) return;
    if (win.hidden) {
      this.openOutputRack();
    } else {
      win.hidden = true;
    }
  };

  Arrange.prototype.openOutputRack = function () {
    var win = document.getElementById("arrRackWindow");
    if (!win) return;
    win.hidden = false;
    this._currentRackFilter = "all";
    var filterBtns = document.querySelectorAll(".rack-filter-btn");
    filterBtns.forEach(function (t) {
      if (t.getAttribute("data-tab") === "all") t.classList.add("active");
      else t.classList.remove("active");
    });
    this.renderOutputRack("all");
  };

  Arrange.prototype.renderOutputRack = function (filter) {
    var self = this;
    this._currentRackFilter = filter || "all";
    var listEl = document.getElementById("arrRackList");
    var statusEl = document.getElementById("arrRackStatusText");
    if (!listEl) return;
    listEl.innerHTML = "";

    var items = [];

    // 1. 虚拟合成器
    if (filter === "all" || filter === "synth") {
      items.push({
        type: "source",
        subType: "synth",
        id: "source-synth",
        badge: "SYNTH",
        badgeClass: "arr-rack-badge-synth",
        title: "内置多波形合成器 · Synth",
        desc: "支持正弦/锯齿/方波通用合成器音色",
        source: { type: "synth", wave: "sawtooth", label: "合成器 · 锯齿波" }
      });
    }

    // 2. SoundFont 预设与扩展
    if (filter === "all" || filter === "sf2") {
      items.push({
        type: "source",
        subType: "sf2",
        id: "source-piano",
        badge: "SF2 PIANO",
        badgeClass: "arr-rack-badge-sf2",
        title: "预设钢琴 · Warm Piano",
        desc: "原声大三角钢琴 SoundFont 音色库",
        source: { type: "builtin", tone: "piano", label: "内置 · 温暖钢琴" }
      });
      items.push({
        type: "source",
        subType: "sf2",
        id: "source-strings",
        badge: "SF2 STRINGS",
        badgeClass: "arr-rack-badge-sf2",
        title: "预设弦乐 · String Ensemble",
        desc: "管弦乐合奏组 SoundFont 音色库",
        source: { type: "builtin", tone: "strings", label: "内置 · 弦乐群" }
      });

      var cachedFonts = window.SoundLibrary ? window.SoundLibrary.getCachedSoundFonts() : [];
      cachedFonts.forEach(function (sf) {
        var firstPreset = (sf.presets && sf.presets.length) ? sf.presets[0].id : null;
        items.push({
          type: "source",
          subType: "sf2",
          id: "sf2-" + sf.id,
          badge: "SF2 扩展",
          badgeClass: "arr-rack-badge-sf2",
          title: sf.name || "自定义音色库",
          desc: firstPreset ? "预设: " + ((sf.presets[0].name || "Preset")) : "已挂载自定义 SoundFont 音色",
          source: { type: "sf2", libId: sf.id, presetId: firstPreset, label: (sf.name || "自定义音色").slice(0, 24) }
        });
      });
    }

    // 3. 采样素材
    if (filter === "all" || filter === "samples") {
      var sampleMap = {};
      for (var t = 0; t < self.tracks.length; t++) {
        var trk = self.tracks[t];
        for (var c = 0; c < trk.clips.length; c++) {
          var clip = trk.clips[c];
          if (clip.type === "audio" && clip.src && clip.src.p) {
            var path = clip.src.p;
            if (!sampleMap[path]) {
              sampleMap[path] = {
                type: "sample",
                subType: "audio",
                id: "sample-" + path,
                badge: "SAMPLE",
                badgeClass: "arr-rack-badge-sample",
                title: clip.name || path.split("/").pop(),
                desc: "音频采样: " + path,
                path: path,
                name: clip.name || path.split("/").pop()
              };
            }
          }
        }
      }
      for (var p in sampleMap) {
        items.push(sampleMap[p]);
      }
    }

    if (statusEl) {
      statusEl.textContent = "已挂载 " + items.length + " 个通道 (" + (filter === "all" ? "全部" : filter.toUpperCase()) + ")";
    }

    if (items.length === 0) {
      listEl.innerHTML = '<div style="text-align:center; padding:24px 10px; color:var(--color-ink-dim); font-size:12px">当前分类下暂无已挂载通道条目</div>';
      return;
    }

    // 渲染 FL Studio 通道条目卡片
    items.forEach(function (item) {
      var card = document.createElement("div");
      card.className = "arr-rack-item";
      card.draggable = true;

      // 拖拽把手图标
      var handle = document.createElement("span");
      handle.className = "arr-rack-item-drag-handle";
      handle.innerHTML = "⋮⋮";
      handle.title = "按住拖拽至轨道";
      card.appendChild(handle);

      // 类型徽标
      var badge = document.createElement("span");
      badge.className = "arr-rack-item-badge " + (item.badgeClass || "");
      badge.textContent = item.badge;
      card.appendChild(badge);

      // 信息区
      var info = document.createElement("div");
      info.className = "arr-rack-item-info";

      var nameEl = document.createElement("div");
      nameEl.className = "arr-rack-item-name";
      nameEl.textContent = item.title;
      info.appendChild(nameEl);

      var descEl = document.createElement("div");
      descEl.className = "arr-rack-item-desc";
      descEl.textContent = item.desc;
      info.appendChild(descEl);

      card.appendChild(info);

      // 操作按钮区 (全自定义 CSS 按钮，拒绝原生)
      var actions = document.createElement("div");
      actions.className = "arr-rack-item-actions";

      var previewBtn = document.createElement("button");
      previewBtn.type = "button";
      previewBtn.className = "arr-rack-preview-btn";
      previewBtn.innerHTML = "▶ 试听";
      previewBtn.title = "预览试听发声";
      previewBtn.addEventListener("click", function (e) {
        e.stopPropagation();
        if (item.type === "source") {
          self.previewRackSource(item.source);
        } else {
          self.previewRackSample(item.path);
        }
      });
      actions.appendChild(previewBtn);

      var assignBtn = document.createElement("button");
      assignBtn.type = "button";
      assignBtn.className = "arr-rack-assign-btn";
      assignBtn.innerHTML = "分配";
      assignBtn.title = "分配到当前选中的轨道";
      assignBtn.addEventListener("click", function (e) {
        e.stopPropagation();
        if (item.type === "source") {
          self.replaceTrackSourceFromDrop(self.selectedTrackIdx, item.source);
        } else {
          self.addAudioClipFromDrop(self.selectedTrackIdx, item.path, item.name, self.playheadBeat);
        }
      });
      actions.appendChild(assignBtn);

      card.appendChild(actions);

      // 拖拽数据绑定 (application/x-arrange)
      card.addEventListener("dragstart", function (e) {
        card.classList.add("dragging");
        var dragData;
        if (item.type === "source") {
          dragData = { kind: "arr-source", source: item.source };
        } else {
          dragData = { kind: "arr-audio", p: item.path, name: item.name };
        }
        e.dataTransfer.setData("application/x-arrange", JSON.stringify(dragData));
        e.dataTransfer.setData("text/plain", JSON.stringify(dragData));
        // 独立标记类型：dragover 阶段读不到 data 内容（浏览器安全限制），
        // 只能凭 types 区分「替换音源」与「放入素材」，给出各自的视觉反馈
        if (item.type === "source") {
          e.dataTransfer.setData("application/x-arrange-source", "1");
        }
        e.dataTransfer.effectAllowed = "copyMove";
      });

      card.addEventListener("dragend", function () {
        card.classList.remove("dragging");
      });

      listEl.appendChild(card);
    });
  };

  /** 预览机架音源 (在编曲引擎的 AudioContext 上播放一个 C4 音符) */
  Arrange.prototype.previewRackSource = function (source) {
    var self = this;
    if (!source) return;
    try {
      this.engine.resume();
      var ctx = this.engine.ctx;
      if (!ctx) return;
      var gain = ctx.createGain();
      gain.gain.value = 0.8;
      gain.connect(ctx.destination);
      var cleanup = function () {
        setTimeout(function () {
          try { gain.disconnect(); } catch (e) {}
        }, 600);
      };
      var playNote = function (player) {
        player.noteOn(60, 100);
        setTimeout(function () {
          try { player.noteOff(60); } catch (e) {}
        }, 600);
        cleanup();
      };
      if (source.type === "synth") {
        var synth = new window.SynthEngine(ctx, gain);
        synth.init();
        synth.setWaveform(source.wave || "sawtooth");
        synth._forceWebAudio = true; // 试听强制走 Web Audio，避免直发原生引擎轨道
        playNote(synth);
      } else if (source.type === "sf2" && source.libId && window.SoundLibrary) {
        window.SoundLibrary.getSoundFont(source.libId).then(function (rec) {
          if (!rec || !rec.data) throw new Error("音源数据不存在");
          var player = new window.SoundFontPlayer(ctx, gain);
          player.init();
          player.parseSF2(rec.data);
          if (source.presetId) player.setPreset(source.presetId);
          playNote(player);
        }).catch(function (err) {
          cleanup();
          if (self.showHUD) self.showHUD("✗ 试听失败: " + err.message);
        });
      } else {
        // builtin 预设音色
        var player = new window.SoundFontPlayer(ctx, gain);
        player.init();
        player.setPreset(source.tone || "piano");
        playNote(player);
      }
    } catch (err) {
      console.warn("试听音源失败:", err);
      this.showHUD("✗ 试听失败: " + (err.message || err));
    }
  };

  /** 预览机架采样音频（与文件树双击试听同一链路 /api/arrangement/audio） */
  Arrange.prototype.previewRackSample = function (path) {
    if (!path) return;
    this.engine.resume();
    this.engine.previewSample(path).catch(function () {
      if (window.Arrange && window.Arrange.showHUD) {
        window.Arrange.showHUD("✗ 音频试听失败（文件不存在或格式不支持）");
      }
    });
  };

  /* ═══════════ 启动 ═══════════ */

  window.Arrange = new Arrange();
  document.addEventListener("DOMContentLoaded", function () {
    window.Arrange.init();
  });
  /* 应用退出兜底：800ms 防抖窗口内的编排改动用 sendBeacon 落盘
     （后端 /arrangement 已同时接受 POST），不阻塞卸载 */
  window.addEventListener("beforeunload", function () {
    var a = window.Arrange;
    if (!a || !a.projectId || !a.dirty) return;
    if (navigator.sendBeacon) {
      navigator.sendBeacon(
        "/api/projects/" + encodeURIComponent(a.projectId) + "/arrangement",
        new Blob([JSON.stringify(a.serialize())], { type: "application/json" })
      );
    }
  });

})(window);
