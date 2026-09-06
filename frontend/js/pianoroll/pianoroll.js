/* Studio One 风格抽屉与 FL Studio 风格专业钢琴卷帘核心控制器 (深层对齐版) */
(function (window) {
  "use strict";

  var UI = window.UI;
  var Tools = window.PianoRollTools;

  function PianoRoll() {
    this.drawerEl = null;
    this.isOpen = false;
    this.isMaximized = false;
    this.isFocused = false; // 焦点状态
    this.drawerHeight = parseInt(localStorage.getItem("prDrawerHeight"), 10) || 440;
    this.minHeight = 160;
    this.closeThreshold = 110;

    // 多标签页数据管理
    this.tabs = []; // [{ id, name, projectId, path, notes, originalNotes, dirty, undoStack, redoStack, bpm }]
    this.activeTabId = null;

    // 画布与视口坐标
    this.keysCanvas = null;
    this.gridCanvas = null;
    this.velocityCanvas = null;
    this.keysCtx = null;
    this.gridCtx = null;
    this.velocityCtx = null;
    this.hudBadge = null;

    this.keysWidth = 64;
    this.rulerHeight = 28;
    this.velocityHeight = 90;

    // 视口缩放与平移 (Beat & Pitch)
    this.pixelsPerBeat = 64;
    this.noteRowHeight = 16;
    this.scrollX = 0;
    this.scrollY = 60 * 16;

    // FL Studio 工具与状态
    this.currentTool = "draw"; // "draw", "paint", "erase", "slice", "select", "mute", "zoom"
    this.snapGrid = 0.25; // 1/16 拍
    this.selectedScale = "none";
    this.selectedRootPitch = 60; // C4
    this.chordStamp = "none";
    this.ghostTrackId = null;

    // 选中与多选
    this.selectedNotes = [];
    this.clipboard = [];

    // 音频与回放（共享单例 AudioContext：统一全应用音频时钟基准）
    this.synth = new window.SynthEngine(window.SharedAudio && window.SharedAudio.get());
    this.soundfont = new window.SoundFontPlayer(window.SharedAudio && window.SharedAudio.get());
    this.soundSource = "synth_sawtooth";

    this.isPlaying = false;
    this.playheadBeat = 0;
    this.bpm = 120;
    this.isLooping = true;
    this.loopStart = 0;
    this.loopEnd = 16;
    this.isMetronome = false;
    this.animFrameId = null;

    // MIDI 录制与预备拍系统
    this.isRecording = false;
    this.activeRecordNotes = {}; // { [pitch]: { noteObj, startBeat, pressTime } }
    this.recordConfig = {
      countIn: 0, // 0: 无, 4: 1小节, 8: 2小节
      replaceMode: false, // false: 叠加混录 (Overdub), true: 覆盖录制 (Replace)
      quantizeOnRecord: true, // 录制时实时吸附到网格
      metronomeOnRecord: true // 录制时自动开启节拍器
    };
    this.isCountIn = false;
    this.countInRemaining = 0;

    // FL Studio 经典音符边缘放大渐隐粒子效果池
    this.deleteEffects = [];
    this.effectRafId = null;

    // 自定义下拉菜单控制器
    this.snapDropdown = null;
    this.scaleDropdown = null;
    this.chordDropdown = null;
    this.ghostDropdown = null;
    this.soundDropdown = null;

    // 交互拖拽状态
    this.dragState = null;
    this.saveDebounceTimer = null;
    this.hudTimer = null;
    this.editingNote = null; // 当前双击编辑的音符
  }

  /* ═══════════ 音符删除渐隐特效驱动 ═══════════ */

  PianoRoll.prototype.addDeleteEffect = function (note) {
    if (!note) return;
    if (!this.deleteEffects) this.deleteEffects = [];
    var p = typeof note.note === "number" ? note.note : Tools.noteNameToNumber(note.note);
    this.deleteEffects.push({
      start: note.start,
      end: note.end,
      pitch: p,
      startTime: performance.now(),
      duration: 220
    });
    this.requestEffectFrame();
  };

  PianoRoll.prototype.requestEffectFrame = function () {
    if (this.effectRafId) return;
    var self = this;
    this.effectRafId = requestAnimationFrame(function () {
      self.effectRafId = null;
      self.render();
    });
  };

  /* ═══════════ 初始化与 DOM 挂载 ═══════════ */

  PianoRoll.prototype.init = function () {
    this.drawerEl = document.getElementById("pianoDrawer");
    if (!this.drawerEl) return;

    this.keysCanvas = document.getElementById("prKeysCanvas");
    this.gridCanvas = document.getElementById("prGridCanvas");
    this.velocityCanvas = document.getElementById("prVelocityCanvas");
    this.hudBadge = document.getElementById("prHudBadge");

    if (this.keysCanvas) this.keysCtx = this.keysCanvas.getContext("2d");
    if (this.gridCanvas) this.gridCtx = this.gridCanvas.getContext("2d");
    if (this.velocityCanvas) this.velocityCtx = this.velocityCanvas.getContext("2d");

    this.synth.init();
    this.soundfont.init();

    this.bindFocusManager();
    this.bindDrawerResizer();
    this.bindHeaderControls();
    this.bindCanvasEvents();
    this.bindGlobalShortcuts();
    this.bindMidiRouter();
    this.bindToolModals();

    if (window.MidiInputRouter && window.MidiInputRouter.settings) {
      this.isTypingKeyboard = !!window.MidiInputRouter.settings.typingKeyboard;
    }
    var typingBtn = document.getElementById("prTypingMidiBtn");
    if (typingBtn) {
      typingBtn.classList.toggle("active", this.isTypingKeyboard);
    }
    if (this.drawerEl) this.drawerEl.setAttribute("data-tool", this.currentTool || "draw");
    if (this.gridCanvas) this.gridCanvas.setAttribute("data-tool", this.currentTool || "draw");

    window.addEventListener("resize", this.resizeCanvases.bind(this));
  };

  /* ═══════════ 焦点管理系统 (Focus Manager) ═══════════ */

  PianoRoll.prototype.bindFocusManager = function () {
    var self = this;
    if (!this.drawerEl) return;

    // 点击卷帘内部任意区域激活焦点
    this.drawerEl.addEventListener("mousedown", function (e) {
      self.setFocus(true);
    });

    // 点击外部区域或输入框释放焦点
    document.addEventListener("mousedown", function (e) {
      if (!self.drawerEl.contains(e.target) && !e.target.closest(".modal")) {
        self.setFocus(false);
      }
    });

    // 聊天输入框获得焦点时释放卷帘焦点
    var msgInput = document.getElementById("msgInput");
    if (msgInput) {
      msgInput.addEventListener("focus", function () { self.setFocus(false); });
    }
  };

  PianoRoll.prototype.setFocus = function (focused) {
    this.isFocused = focused && this.isOpen;
    if (this.drawerEl) {
      this.drawerEl.classList.toggle("pr-focused", this.isFocused);
    }
  };

  PianoRoll.prototype.showHUD = function (text) {
    if (!this.hudBadge) return;
    this.hudBadge.textContent = text;
    this.hudBadge.classList.remove("hud-pop");
    void this.hudBadge.offsetWidth;
    this.hudBadge.classList.add("visible", "hud-pop");
    clearTimeout(this.hudTimer);
    this.hudTimer = setTimeout(function () {
      if (this.hudBadge) this.hudBadge.classList.remove("visible", "hud-pop");
    }.bind(this), 1200);
  };

  PianoRoll.prototype.resizeCanvases = function () {
    if (!this.isOpen || !this.gridCanvas) return;
    var container = document.getElementById("prWorkspaceWrap");
    if (!container) return;

    var w = container.clientWidth - this.keysWidth;
    var totalH = container.clientHeight;
    var mainH = Math.max(80, totalH - this.velocityHeight);

    // 抽屉拖拽中高频触发：尺寸未变（或变化 <1px）时跳过——每次 width/height
    // 赋值都会重分配三块画布背板存储并全量重绘
    if (this._lastCanvasW !== undefined &&
        Math.abs(this._lastCanvasW - w) < 1 && Math.abs(this._lastCanvasMainH - mainH) < 1) {
      return;
    }
    this._lastCanvasW = w;
    this._lastCanvasMainH = mainH;

    var dpr = window.devicePixelRatio || 1;

    // 琴键画布
    this.keysCanvas.width = this.keysWidth * dpr;
    this.keysCanvas.height = mainH * dpr;
    this.keysCanvas.style.width = this.keysWidth + "px";
    this.keysCanvas.style.height = mainH + "px";
    this.keysCtx.setTransform(dpr, 0, 0, dpr, 0, 0);

    // 主网格画布
    this.gridCanvas.width = Math.max(10, w) * dpr;
    this.gridCanvas.height = Math.max(10, mainH) * dpr;
    this.gridCanvas.style.width = Math.max(10, w) + "px";
    this.gridCanvas.style.height = Math.max(10, mainH) + "px";
    this.gridCtx.setTransform(dpr, 0, 0, dpr, 0, 0);

    // 力度画布
    this.velocityCanvas.width = Math.max(10, w) * dpr;
    this.velocityCanvas.height = this.velocityHeight * dpr;
    this.velocityCanvas.style.width = Math.max(10, w) + "px";
    this.velocityCanvas.style.height = this.velocityHeight + "px";
    this.velocityCtx.setTransform(dpr, 0, 0, dpr, 0, 0);

    this.render();
  };

  /* ═══════════ 抽屉展开、折叠与缩放 (Studio One 风格) ═══════════ */

  PianoRoll.prototype.open = function () {
    if (!this.drawerEl) return;
    this.isOpen = true;
    this.drawerEl.classList.remove("closed");
    this.drawerEl.classList.add("open");
    this.drawerEl.style.height = this.drawerHeight + "px";
    this.setFocus(true);
    this.synth.resume();
    this.soundfont.resume();
    setTimeout(this.resizeCanvases.bind(this), 50);
  };

  PianoRoll.prototype.close = function () {
    if (!this.drawerEl) return;
    this.stopPlayback();
    this.isOpen = false;
    this.setFocus(false);
    this.drawerEl.classList.remove("open", "maximized");
    this.drawerEl.classList.add("closed");
    this.isMaximized = false;
  };

  PianoRoll.prototype.toggleMaximize = function () {
    if (!this.drawerEl) return;
    this.isMaximized = !this.isMaximized;
    this.drawerEl.classList.toggle("maximized", this.isMaximized);
    if (!this.isMaximized) {
      this.drawerEl.style.height = this.drawerHeight + "px";
    }
    setTimeout(this.resizeCanvases.bind(this), 50);
  };

  PianoRoll.prototype.bindDrawerResizer = function () {
    var self = this;
    var resizer = document.getElementById("prDrawerResizer");
    if (!resizer) return;

    var startY = 0, startH = 0, isDragging = false;

    resizer.addEventListener("mousedown", function (e) {
      if (self.isMaximized) return;
      isDragging = true;
      startY = e.clientY;
      startH = self.drawerEl.offsetHeight;
      document.body.classList.add("resizing-drawer");
      self.setFocus(true);
      e.preventDefault();
    });

    resizer.addEventListener("dblclick", function () {
      self.toggleMaximize();
    });

    window.addEventListener("mousemove", function (e) {
      if (!isDragging) return;
      var dy = startY - e.clientY;
      var newH = startH + dy;
      var maxH = window.innerHeight * 0.88;

      if (newH < self.closeThreshold) {
        self.close();
        isDragging = false;
        document.body.classList.remove("resizing-drawer");
        return;
      }

      self.drawerHeight = Math.max(self.minHeight, Math.min(maxH, newH));
      self.drawerEl.style.height = self.drawerHeight + "px";
      /* 画布重分配按帧合流：一帧内多次 mousemove 只 resize 一次 */
      if (!self._resizeRaf) {
        self._resizeRaf = requestAnimationFrame(function () {
          self._resizeRaf = null;
          self.resizeCanvases();
        });
      }
    });

    window.addEventListener("mouseup", function () {
      if (isDragging) {
        isDragging = false;
        /* 高度跨会话记忆（此前每次启动都回到默认 440px） */
        try { localStorage.setItem("prDrawerHeight", String(self.drawerHeight)); } catch (e) {}
        document.body.classList.remove("resizing-drawer");
      }
    });
  };

  /* ═══════════ 标签页与文件载入 ═══════════ */

  PianoRoll.prototype.openFile = function (projectId, fileName, filePath) {
    var self = this;
    var tabId = projectId + "::" + fileName;
    var existing = this.tabs.find(function (t) { return t.id === tabId; });

    if (existing) {
      this.switchTab(tabId);
      this.open();
      return;
    }

    var url = "/api/projects/" + projectId + "/download?names=" + encodeURIComponent(fileName);
    /* 打开即给反馈（fetch 期间此前完全无响应，慢网络像双击失灵） */
    if (window.UI && window.UI.toast) window.UI.toast("⏳ 正在打开 " + fileName.split("/").pop() + "…", "");
    fetch(url).then(function (res) {
      if (!res.ok) throw new Error("获取 MIDI 文件失败");
      return res.arrayBuffer();
    }).then(function (buffer) {
      var notes = self.parseMidiBytes(buffer);
      var newTab = {
        id: tabId,
        name: fileName.split("/").pop(),
        fullName: fileName,
        projectId: projectId,
        filePath: filePath,
        notes: notes,
        originalNotes: JSON.parse(JSON.stringify(notes)),
        dirty: false,
        undoStack: [],
        redoStack: [],
        bpm: 120
      };
      self.tabs.push(newTab);
      self.switchTab(tabId);
      self.open();
    }).catch(function (err) {
      if (window.UI && window.UI.toast) window.UI.toast("✗ 打开 MIDI 失败: " + err.message, "err");
    });
  };

  PianoRoll.prototype.switchTab = function (tabId) {
    this.activeTabId = tabId;
    var tab = this.getActiveTab();
    if (tab) {
      this.bpm = tab.bpm || 120;
      this.updateLoopBoundsFromNotes(tab.notes);
      this.autoCenterView();
    }
    this.renderTabs();
    this.render();
  };

  PianoRoll.prototype.closeTab = function (tabId, e) {
    if (e) e.stopPropagation();
    var self = this;
    var idx = this.tabs.findIndex(function (t) { return t.id === tabId; });
    if (idx === -1) return;
    var tab = this.tabs[idx];
    /* 有未落盘的编辑（保存失败/防抖未触发）：先保存再关闭；
       保存失败则保留标签页——不再静默丢弃用户编辑 */
    if (tab.dirty) {
      this.saveTab(tab).then(function () {
        self.closeTab(tabId, null);
      }).catch(function () { /* SAVE ERR 已提示，标签保留 */ });
      return;
    }
    this.tabs.splice(idx, 1);
    if (this.activeTabId === tabId) {
      if (this.tabs.length > 0) {
        this.switchTab(this.tabs[Math.max(0, idx - 1)].id);
      } else {
        this.activeTabId = null;
        this.close();
      }
    }
    this.renderTabs();
  };

  PianoRoll.prototype.getActiveTab = function () {
    var self = this;
    return this.tabs.find(function (t) { return t.id === self.activeTabId; }) || null;
  };

  PianoRoll.prototype.renderTabs = function () {
    var container = document.getElementById("prTabBar");
    if (!container) return;
    var self = this;

    // 检查是否已有相同数量与 ID 的现有标签：若是则直接局部更新内容与 active 类，避免 DOM 销毁重排引发抖动
    var existingTabs = container.querySelectorAll(".pr-tab");
    if (existingTabs.length === this.tabs.length && this.tabs.length > 0) {
      var match = true;
      for (var i = 0; i < this.tabs.length; i++) {
        if (existingTabs[i].dataset.tabId !== this.tabs[i].id) { match = false; break; }
      }
      if (match) {
        this.tabs.forEach(function (tab, i) {
          var tabBtn = existingTabs[i];
          tabBtn.classList.toggle("active", tab.id === self.activeTabId);
          var nameSpan = tabBtn.querySelector(".pr-tab-name");
          if (nameSpan) {
            var expected = (tab.dirty ? "● " : "") + tab.name;
            if (nameSpan.textContent !== expected) nameSpan.textContent = expected;
          }
        });
        this.updateGhostTrackSelect();
        return;
      }
    }

    // 标签数量或结构增减时才全量重建
    container.innerHTML = "";
    this.tabs.forEach(function (tab) {
      var tabBtn = document.createElement("div");
      tabBtn.className = "pr-tab" + (tab.id === self.activeTabId ? " active" : "");
      tabBtn.dataset.tabId = tab.id;
      tabBtn.innerHTML =
        '<span class="pr-tab-name">' + (tab.dirty ? "● " : "") + UI.esc(tab.name) + '</span>' +
        '<button type="button" class="pr-tab-close" title="关闭标签">✕</button>';

      tabBtn.addEventListener("click", function () { self.switchTab(tab.id); });
      tabBtn.querySelector(".pr-tab-close").addEventListener("click", function (e) {
        self.closeTab(tab.id, e);
      });
      container.appendChild(tabBtn);
    });

    this.updateGhostTrackSelect();
  };

  PianoRoll.prototype.updateGhostTrackSelect = function () {
    var self = this;
    var opts = [{ value: "", label: "幽灵参考轨: 无" }];
    this.tabs.forEach(function (t) {
      if (t.id !== self.activeTabId) {
        opts.push({ value: t.id, label: "参考: " + t.name });
      }
    });
    if (this.ghostDropdown) {
      this.ghostDropdown.updateOptions(opts, this.ghostTrackId || "");
    }
  };

  PianoRoll.prototype.updateLoopBoundsFromNotes = function (notes) {
    if (!notes || !notes.length) {
      this.loopEnd = 16;
      return;
    }
    var maxEnd = 0;
    notes.forEach(function (n) { if (n.end > maxEnd) maxEnd = n.end; });
    this.loopEnd = Math.max(4, Math.ceil(maxEnd / 4) * 4);
  };

  PianoRoll.prototype.autoCenterView = function () {
    var tab = this.getActiveTab();
    if (!tab || !tab.notes || !tab.notes.length) {
      this.scrollY = (127 - 60) * this.noteRowHeight - 150;
      return;
    }
    var sumPitch = 0;
    tab.notes.forEach(function (n) {
      sumPitch += Tools.noteNameToNumber(n.note);
    });
    var avgPitch = Math.round(sumPitch / tab.notes.length);
    var targetY = (127 - avgPitch) * this.noteRowHeight - 150;
    this.scrollY = Math.max(0, Math.min(128 * this.noteRowHeight - 200, targetY));
    this.scrollX = 0;
  };

  /* ═══════════ 音符数据解析与保存同步 ═══════════ */

  PianoRoll.prototype.parseMidiBytes = function (arrayBuffer) {
    return window.MidiParse ? window.MidiParse.parseBytes(arrayBuffer) : [];
  };

  PianoRoll.prototype.pushHistory = function () {
    var tab = this.getActiveTab();
    if (!tab) return;
    tab.undoStack.push(JSON.stringify(tab.notes));
    if (tab.undoStack.length > 50) tab.undoStack.shift();
    tab.redoStack = [];
    tab.dirty = true;
    this.renderTabs();
    this.scheduleAutoSave();
  };

  PianoRoll.prototype.undo = function () {
    var tab = this.getActiveTab();
    if (!tab || !tab.undoStack || !tab.undoStack.length) {
      this.showHUD("没有更多可撤销的操作 (Undo)");
      return;
    }
    if (!tab.redoStack) tab.redoStack = [];
    tab.redoStack.push(JSON.stringify(tab.notes));
    tab.notes = JSON.parse(tab.undoStack.pop());
    this.selectedNotes = [];
    this.render();
    this.showHUD("↩ 撤销 (Undo / Ctrl+Z)");
    this.scheduleAutoSave();
  };

  PianoRoll.prototype.redo = function () {
    var tab = this.getActiveTab();
    if (!tab || !tab.redoStack || !tab.redoStack.length) {
      this.showHUD("没有更多可重做的操作 (Redo)");
      return;
    }
    if (!tab.undoStack) tab.undoStack = [];
    tab.undoStack.push(JSON.stringify(tab.notes));
    tab.notes = JSON.parse(tab.redoStack.pop());
    this.selectedNotes = [];
    this.render();
    this.showHUD("↪ 重做 (Redo / Ctrl+Alt+Z)");
    this.scheduleAutoSave();
  };

  PianoRoll.prototype.scheduleAutoSave = function () {
    var self = this;
    this.setSaveState("EDIT…");
    clearTimeout(this.saveDebounceTimer);
    this.saveDebounceTimer = setTimeout(function () {
      self.saveCurrentTab().catch(function () { /* 失败已在 saveTab 内提示 */ });
    }, 800);
  };

  /* 保存状态章：EDIT… / SAVING / SAVED / SAVE ERR（对齐编排窗的状态机，
     此前自动保存完全静默、失败被吞，用户无从得知编辑是否落盘） */
  PianoRoll.prototype.setSaveState = function (state) {
    var el = document.getElementById("prSaveStamp");
    if (!el) return;
    el.hidden = false;
    el.textContent = state;
    el.classList.remove("ok", "warn", "danger");
    if (state === "SAVED") el.classList.add("ok");
    else if (state === "SAVE ERR") el.classList.add("danger");
    else el.classList.add("warn");
    clearTimeout(this._saveStampTimer);
    if (state === "SAVED") {
      this._saveStampTimer = setTimeout(function () { el.hidden = true; }, 1600);
    }
  };

  PianoRoll.prototype.saveTab = function (tab) {
    var self = this;
    if (!tab || !tab.dirty) return Promise.resolve();
    // 先"认领" dirty 再发请求（同 arrange.doSave）：响应回来再清 flag 会
    // 误清在途编辑，UI 显示 SAVED 但最后几笔改动从未落盘
    tab.dirty = false;
    this.setSaveState("SAVING");

    var noteLines = tab.notes.map(function (n) {
      return '[note: "' + n.note + '", velocity: "' + n.velocity + '", start: "' + n.start + '", end: "' + n.end + '"]';
    }).join("\n");

    return UI.postJSON("/api/projects/" + tab.projectId + "/files/save_notes", {
      name: tab.fullName,
      note_table: noteLines,
      bpm: tab.bpm || 120
    }).then(function () {
      self.renderTabs();
      self.setSaveState("SAVED");
      if (tab.dirty) self.saveTab(tab); // 保存期间有编辑：补一轮（saveTab 内部会重新认领）
    }).catch(function (e) {
      tab.dirty = true; // 保存失败：恢复 dirty，编辑仍保留，关窗兜底仍会带上
      self.setSaveState("SAVE ERR");
      if (window.UI && window.UI.toast) {
        window.UI.toast("✗ 自动保存失败: " + ((e && e.message) || "网络错误") + "（编辑仍保留）", "err");
      }
      throw e;
    });
  };

  PianoRoll.prototype.saveCurrentTab = function () {
    return this.saveTab(this.getActiveTab());
  };

  /* 应用退出兜底：800ms 防抖窗口内的编辑用 sendBeacon 落盘 */
  PianoRoll.prototype.flushAllOnUnload = function () {
    this.tabs.forEach(function (tab) {
      if (!tab.dirty) return;
      var noteLines = tab.notes.map(function (n) {
        return '[note: "' + n.note + '", velocity: "' + n.velocity + '", start: "' + n.start + '", end: "' + n.end + '"]';
      }).join("\n");
      if (navigator.sendBeacon) {
        navigator.sendBeacon(
          "/api/projects/" + tab.projectId + "/files/save_notes",
          new Blob([JSON.stringify({ name: tab.fullName, note_table: noteLines, bpm: tab.bpm || 120 })],
            { type: "application/json" })
        );
      }
    });
  };

  /* ═══════════ 音频发声中枢 ═══════════ */

  PianoRoll.prototype.playNoteSound = function (midiNote, velocity, when) {
    this.synth.resume();
    this.soundfont.resume();
    if (this.soundSource.indexOf("synth_") === 0) {
      var wave = this.soundSource.replace("synth_", "");
      this.synth.setWaveform(wave);
      this.synth.noteOn(midiNote, velocity, when);
    } else if (this.soundSource.indexOf("sf2_") === 0) {
      var preset = this.soundSource.replace("sf2_", "");
      this.soundfont.setPreset(preset);
      this.soundfont.noteOn(midiNote, velocity, when);
    }
  };

  PianoRoll.prototype.stopNoteSound = function (midiNote, when) {
    this.synth.noteOff(midiNote, when);
    this.soundfont.noteOff(midiNote, when);
  };

  PianoRoll.prototype.stopAllSounds = function () {
    this.synth.stopAll();
    this.soundfont.stopAll();
  };

  /* ═══════════ 播放与时间轴回放 & 实时录制 ═══════════ */

  PianoRoll.prototype.togglePlay = function () {
    if (this.isPlaying) {
      if (this.isRecording) {
        this.stopRecording();
      } else {
        this.stopPlayback();
      }
    } else {
      this.startPlayback();
    }
  };

  PianoRoll.prototype.toggleLoop = function () {
    this.isLooping = !this.isLooping;
    var btn = document.getElementById("prLoopToggleBtn");
    if (btn) btn.classList.toggle("active", this.isLooping);
    this.showHUD(this.isLooping ? "循环选区: 开启 (Loop ON)" : "循环选区: 关闭 (Loop OFF)");
    this.render();
  };

  /* ═══════════ MIDI 实时录制系统 ═══════════ */

  PianoRoll.prototype.toggleRecord = function () {
    if (this.isRecording) {
      this.stopRecording();
    } else {
      this.startRecording();
    }
  };

  PianoRoll.prototype.startRecording = function () {
    var self = this;
    var tab = this.getActiveTab();
    if (!tab) {
      this.showHUD("请先打开或创建一个音符文件");
      return;
    }

    this.pushHistory(); // 录制前历史快照，支持 Ctrl+Z 撤销

    // 如果开启了覆盖模式 (Replace Mode)
    if (this.recordConfig.replaceMode) {
      if (this.isLooping) {
        tab.notes = tab.notes.filter(function (n) {
          return n.end <= self.loopStart || n.start >= self.loopEnd;
        });
      } else {
        tab.notes = tab.notes.filter(function (n) {
          return n.end <= self.playheadBeat;
        });
      }
    }

    this.isRecording = true;
    this.activeRecordNotes = {};

    var recBtn = document.getElementById("prRecordBtn");
    if (recBtn) recBtn.classList.add("active");

    // 录制时自动开启节拍器
    if (this.recordConfig.metronomeOnRecord && !this.isMetronome) {
      this.isMetronome = true;
      var mBtn = document.getElementById("prMetroBtn");
      if (mBtn) mBtn.classList.add("active");
    }

    // 检查预备拍 (Count-in)
    if (this.recordConfig.countIn > 0) {
      this.isCountIn = true;
      this.countInRemaining = this.recordConfig.countIn;
      this.synth.resume();

      var beatIntervalMs = (60 / this.bpm) * 1000;
      var count = this.recordConfig.countIn;

      function tickCountIn() {
        if (!self.isRecording) return;
        if (count > 0) {
          self.synth.playClick(count % 4 === 0 || count === self.recordConfig.countIn);
          self.showHUD("⏱ 预备拍: " + count, 1200);
          count--;
          setTimeout(tickCountIn, beatIntervalMs);
        } else {
          self.isCountIn = false;
          self.showHUD("🔴 正在录制 MIDI…", 2000);
          self.startPlayback();
        }
      }

      tickCountIn();
    } else {
      this.isCountIn = false;
      this.showHUD("🔴 正在录制 MIDI…", 2000);
      this.startPlayback();
    }
  };

  PianoRoll.prototype.stopRecording = function () {
    this.isRecording = false;
    this.isCountIn = false;

    // 结算所有尚未松开的按键
    for (var p in this.activeRecordNotes) {
      if (this.activeRecordNotes.hasOwnProperty(p)) {
        var rec = this.activeRecordNotes[p];
        rec.noteObj.end = Math.max(rec.startBeat + (this.recordConfig.quantizeOnRecord ? this.snapGrid : 0.1), this.playheadBeat);
      }
    }
    this.activeRecordNotes = {};

    var recBtn = document.getElementById("prRecordBtn");
    if (recBtn) recBtn.classList.remove("active");

    this.stopPlayback();
    this.triggerAutoSave();
    this.showHUD("✓ 录制完成 (已写入音符)");
    this.render();
  };

  PianoRoll.prototype.handleLiveNoteOn = function (note, velocity) {
    if (!this.isRecording || this.isCountIn) return;
    var tab = this.getActiveTab();
    if (!tab) return;

    var curBeat = this.playheadBeat;
    var startBeat = this.recordConfig.quantizeOnRecord
      ? Math.round(curBeat / this.snapGrid) * this.snapGrid
      : curBeat;
    startBeat = Math.max(0, Math.round(startBeat * 1000) / 1000);

    var noteName = Tools.numberToNoteName(note);
    var newNote = {
      note: noteName,
      start: startBeat,
      end: startBeat + (this.recordConfig.quantizeOnRecord ? this.snapGrid : 0.1),
      velocity: Math.max(1, Math.min(127, velocity || 100))
    };

    this.activeRecordNotes[note] = {
      noteObj: newNote,
      startBeat: startBeat,
      pressTime: performance.now()
    };

    if (!tab.notes) tab.notes = [];
    tab.notes.push(newNote);
    this.render();
  };

  PianoRoll.prototype.handleLiveNoteOff = function (note) {
    if (!this.isRecording) return;
    var active = this.activeRecordNotes[note];
    if (!active) return;

    var curBeat = this.playheadBeat;
    var endBeat = this.recordConfig.quantizeOnRecord
      ? Math.max(active.startBeat + this.snapGrid, Math.round(curBeat / this.snapGrid) * this.snapGrid)
      : Math.max(active.startBeat + 0.05, curBeat);
    endBeat = Math.round(endBeat * 1000) / 1000;

    active.noteObj.end = endBeat;
    delete this.activeRecordNotes[note];
    this.render();
  };

  /* ═══════════ AudioContext 时钟播放调度 ═══════════
     此前 rAF 逐帧扫描全部音符 + ±0.05 拍触发窗 + setTimeout 停音：
     300BPM 时窗口 10ms 小于一帧 → 丢音符；后台标签 rAF 节流时整段哑火。
     现改为 25ms 调度心跳 / 150ms lookahead 按音频时钟精确排入
     noteOn/noteOff（与编曲窗 audio_engine 同一模式），rAF 只画播放头。 */

  PianoRoll.prototype._audioNow = function () {
    return window.SharedAudio ? window.SharedAudio.now() : this.synth.ctx.currentTime;
  };

  PianoRoll.prototype.startPlayback = function () {
    var self = this;
    this.synth.resume();
    this.soundfont.resume();
    this.synth.init();
    // 记录本次播放起点（「暂停后恢复光标位置」回退目标；循环跳转在前）
    this.playbackOriginBeat = Math.max(0, this.playheadBeat);
    this.isPlaying = true;

    var btn = document.getElementById("prPlayBtn");
    if (btn) {
      btn.innerHTML = "⏸ 暂停";
      btn.classList.add("active");
    }

    /* 锚点：ctx 时钟 + 起始拍。循环语义与旧实现一致——起点在循环区间外
       直接跳到 loopStart；区间内则直线播到 loopEnd 后按 [loopStart, loopEnd) 回绕 */
    this._schedCtxTime = this._audioNow() + 0.06;
    this._schedStartBeat = this.playheadBeat;
    this._schedSegStart = this.playheadBeat;
    this._schedSegLen = Infinity;
    if (this.isLooping && this.loopEnd > this.loopStart) {
      if (this.playheadBeat >= this.loopEnd || this.playheadBeat < this.loopStart) {
        this._schedStartBeat = this.loopStart;
        this._schedSegStart = this.loopStart;
        this.playheadBeat = this.loopStart;
      }
      this._schedSegLen = Math.max(0.001, this.loopEnd - this._schedSegStart);
    }
    this._schedPos = 0;

    this._schedTimer = setInterval(function () { self._scheduleTick(); }, 25);
    this._scheduleTick();

    /* rAF 只负责播放头推进 / 录制音符拉伸 / 自动跟随（不再触发音符） */
    function frame() {
      if (!self.isPlaying) return;
      self.playheadBeat = self._currentPlayBeat();
      if (self.isRecording && !self.isCountIn) {
        for (var p in self.activeRecordNotes) {
          if (self.activeRecordNotes.hasOwnProperty(p)) {
            var rec = self.activeRecordNotes[p];
            rec.noteObj.end = Math.max(rec.startBeat + 0.05, self.playheadBeat);
          }
        }
      }
      var curPx = self.playheadBeat * self.pixelsPerBeat;
      var viewW = self.gridCanvas ? self.gridCanvas.width / (window.devicePixelRatio || 1) : 600;
      if (curPx > self.scrollX * self.pixelsPerBeat + viewW - 100) {
        self.scrollX = self.playheadBeat - 1;
      }
      self.render();
      self.animFrameId = requestAnimationFrame(frame);
    }
    this.animFrameId = requestAnimationFrame(frame);
  };

  /* 展开位置 → 时间线拍。本地播放一律用本地 AudioContext 时钟——
     此前「AUTO 时优先跟随引擎 timecode」的分支是冻结源：钢琴窗从不
     启动引擎走带（EngineBridge.play 仅编曲窗调用），tc.playing 恒为
     false，播放头被钉死在 tc.beat。本地时钟与上方调度器同源，天然同步 */
  PianoRoll.prototype._currentPlayBeat = function () {
    var pos = (this._audioNow() - this._schedCtxTime) * (this.bpm / 60);
    if (pos < 0) pos = 0;
    if (!this.isLooping || this._schedSegLen === Infinity) {
      return this._schedStartBeat + pos;
    }
    if (pos < this._schedSegLen) return this._schedSegStart + pos;
    var loopLen = this.loopEnd - this.loopStart;
    return this.loopStart + ((pos - this._schedSegLen) % loopLen);
  };

  /* 把调度窗口 [from, to) 拆成线性段（循环回绕处分段），每段给出
     pos→beat 的线性映射；段数上限防极端循环长度撑爆 */
  PianoRoll.prototype._expandPosWindow = function (from, to) {
    var segs = [];
    if (!this.isLooping || this._schedSegLen === Infinity) {
      segs.push({ posFrom: from, beatFrom: this._schedStartBeat + from, span: to - from });
      return segs;
    }
    var loopLen = this.loopEnd - this.loopStart;
    var pos = from;
    if (pos < this._schedSegLen) {
      var end = Math.min(to, this._schedSegLen);
      segs.push({ posFrom: pos, beatFrom: this._schedSegStart + pos, span: end - pos });
      pos = end;
    }
    var guard = 0;
    while (pos < to && guard++ < 64) {
      var rel = (pos - this._schedSegLen) % loopLen;
      var segEnd = pos + (loopLen - rel);
      var end2 = Math.min(to, segEnd);
      segs.push({ posFrom: pos, beatFrom: this.loopStart + rel, span: end2 - pos });
      pos = end2;
    }
    return segs;
  };

  PianoRoll.prototype._scheduleTick = function () {
    if (!this.isPlaying) return;
    var horizonPos = (this._audioNow() + 0.15 - this._schedCtxTime) * (this.bpm / 60);
    if (horizonPos <= this._schedPos) return;

    var self = this;
    var spb = 60 / this.bpm;
    var tab = this.getActiveTab();
    var notes = tab && tab.notes ? tab.notes : [];
    var segs = this._expandPosWindow(this._schedPos, horizonPos);

    for (var s = 0; s < segs.length; s++) {
      var seg = segs[s];
      var beatEnd = seg.beatFrom + seg.span;

      // 节拍器（整数拍，按段内位置换算精确时刻）
      if (this.isMetronome) {
        for (var mb = Math.ceil(seg.beatFrom - 1e-9); mb < beatEnd; mb++) {
          if (mb < 0) continue;
          var mpos = seg.posFrom + (mb - seg.beatFrom);
          this.synth.playClick(mb % 4 === 0, this._schedCtxTime + mpos * spb);
        }
      }

      for (var i = 0; i < notes.length; i++) {
        var n = notes[i];
        if (n.start < seg.beatFrom || n.start >= beatEnd) continue;
        var pNum = this.pitchOf(n.note);
        // 不回放当前正在录制的音符
        if (this.isRecording && this.activeRecordNotes[pNum] &&
            this.activeRecordNotes[pNum].noteObj === n) {
          continue;
        }
        var pos = seg.posFrom + (n.start - seg.beatFrom);
        var when = this._schedCtxTime + pos * spb;
        var durSec = Math.max(0.02, (n.end - n.start) * spb);
        // 原生优先且为合成器音源时，经 JUCE 专用演奏轨直通（内置波形声部，
        // 不依赖 SF2；避免 WebAudio 远期包络不可靠，且统一主链路）。
        // _ensureNativeVoice 同步声部参数（签名不变时为 no-op）并兼作
        // 降级判定：setTrackVoice 失败即回退 WebAudio
        var useNative = false;
        try {
          useNative = window.AudioBackend && window.AudioBackend.isNativePreferred && window.AudioBackend.isNativePreferred()
            && this.soundSource && this.soundSource.indexOf("synth_") === 0
            && this.synth && this.synth._ensureNativeVoice && this.synth._ensureNativeVoice();
        } catch(e) {}
        if (useNative) {
          var trk = (window.EngineBridge && window.EngineBridge.PERF_TRACK) || 0;
          (function(p, v, w, d, tr){
            var delayOn = Math.max(0, (w - self._audioNow()) * 1000);
            setTimeout(function(){ try{ window.EngineBridge.noteOnTrack(tr, p, v); }catch(e){} }, delayOn);
            var delayOff = Math.max(0, (w + d - self._audioNow()) * 1000);
            setTimeout(function(){ try{ window.EngineBridge.noteOffTrack(tr, p); }catch(e){} }, delayOff);
          })(pNum, n.velocity || 100, when, durSec, trk);
        } else {
          this.playNoteSound(pNum, n.velocity, when);
          this.stopNoteSound(pNum, when + durSec);
        }
      }
    }

    this._schedPos = horizonPos;
  };

  PianoRoll.prototype.stopPlayback = function () {
    this.isPlaying = false;
    if (this.animFrameId) cancelAnimationFrame(this.animFrameId);
    if (this._schedTimer) { clearInterval(this._schedTimer); this._schedTimer = null; }
    this.stopAllSounds();

    if (this.isRecording) {
      this.stopRecording();
      return;
    }

    /* 「暂停后恢复光标位置」开启时回退到本次播放起点（录制流程除外） */
    var prefs = UI.transportPrefs ? UI.transportPrefs() : null;
    if (prefs && prefs.resumeOnPause && this.playbackOriginBeat !== undefined) {
      this.playheadBeat = Math.max(0, this.playbackOriginBeat);
      this.showHUD("⏪ 光标已回到本次播放起点");
    }

    var btn = document.getElementById("prPlayBtn");
    if (btn) {
      btn.innerHTML = "▶ 播放";
      btn.classList.remove("active");
    }
    this.render();
  };

  /* ═══════════ Canvas 2D 渲染管线 ═══════════ */

  PianoRoll.prototype.render = function () {
    this.renderKeys();
    this.renderGrid();
    this.renderVelocity();
  };

  /* 拖拽/力度画笔等高频路径的合帧渲染：一帧内多次 mousemove 只重绘一次 */
  PianoRoll.prototype.scheduleRender = function () {
    if (this._renderQueued) return;
    this._renderQueued = true;
    var self = this;
    requestAnimationFrame(function () {
      self._renderQueued = false;
      self.render();
    });
  };

  /* 音名→音高号记忆化：128 种结果全量缓存，渲染/调度热路径不再逐音符正则解析 */
  PianoRoll.prototype._pitchCache = {};
  PianoRoll.prototype.pitchOf = function (name) {
    var v = this._pitchCache[name];
    if (v === undefined) {
      v = Tools.noteNameToNumber(name);
      this._pitchCache[name] = v;
    }
    return v;
  };

  PianoRoll.prototype.renderKeys = function () {
    if (!this.keysCtx || !this.keysCanvas) return;
    var ctx = this.keysCtx;
    var w = this.keysWidth;
    var h = this.keysCanvas.height / (window.devicePixelRatio || 1);

    ctx.clearRect(0, 0, w, h);
    ctx.save();
    ctx.translate(0, this.rulerHeight - this.scrollY);

    var isDark = document.documentElement.dataset.theme === "dark";

    for (var p = 0; p < 128; p++) {
      var y = (127 - p) * this.noteRowHeight;
      if (y + this.noteRowHeight < this.scrollY - this.rulerHeight || y > this.scrollY + h) continue;

      var isBlack = [1, 3, 6, 8, 10].indexOf(p % 12) !== -1;
      var isC = (p % 12 === 0);
      var isHeld = (window.MidiInputRouter && window.MidiInputRouter.heldNotes && window.MidiInputRouter.heldNotes[p]);

      if (isHeld) {
        // 琴键按压发光高亮 (FL Studio 经典琥珀橙/高亮反馈)
        ctx.fillStyle = "#ff9f43";
      } else {
        ctx.fillStyle = isBlack ? (isDark ? "#1b2434" : "#2f3640") : (isDark ? "#283446" : "#f5f6fa");
      }
      ctx.fillRect(0, y, w, this.noteRowHeight);

      ctx.strokeStyle = isHeld ? "#ff9f43" : (isDark ? "#141c2b" : "#dcdde1");
      ctx.lineWidth = 1;
      ctx.strokeRect(0, y, w, this.noteRowHeight);

      if (isHeld) {
        ctx.fillStyle = "#ffffff";
        ctx.font = "bold 10px sans-serif";
        ctx.fillText(Tools.numberToNoteName(p), 6, y + 12);
      } else if (isC) {
        ctx.fillStyle = isDark ? "#48dbfb" : "#0abde3";
        ctx.fillRect(w - 6, y, 6, this.noteRowHeight);
        ctx.fillStyle = isDark ? "#ffffff" : "#2f3640";
        ctx.font = "10px sans-serif";
        ctx.fillText("C" + (Math.floor(p / 12) - 1), 6, y + 12);
      }
    }
    ctx.restore();
  };

  PianoRoll.prototype.renderGrid = function () {
    if (!this.gridCtx || !this.gridCanvas) return;
    var ctx = this.gridCtx;
    var w = this.gridCanvas.width / (window.devicePixelRatio || 1);
    var h = this.gridCanvas.height / (window.devicePixelRatio || 1);

    ctx.clearRect(0, 0, w, h);
    var isDark = document.documentElement.dataset.theme === "dark";

    // 1. 音高背景行
    ctx.save();
    ctx.translate(0, this.rulerHeight - this.scrollY);

    for (var p = 0; p < 128; p++) {
      var y = (127 - p) * this.noteRowHeight;
      if (y + this.noteRowHeight < this.scrollY - this.rulerHeight || y > this.scrollY + h) continue;

      var isBlack = [1, 3, 6, 8, 10].indexOf(p % 12) !== -1;
      var inScale = Tools.isNoteInScale(p, this.selectedRootPitch, this.selectedScale);

      if (inScale && this.selectedScale !== "none") {
        ctx.fillStyle = isDark ? "rgba(72, 219, 251, 0.08)" : "rgba(10, 189, 227, 0.08)";
      } else {
        ctx.fillStyle = isBlack ? (isDark ? "#161e2e" : "#eef1f5") : (isDark ? "#1a2436" : "#ffffff");
      }
      ctx.fillRect(0, y, w, this.noteRowHeight);

      ctx.strokeStyle = isDark ? "rgba(255,255,255,0.04)" : "rgba(0,0,0,0.04)";
      ctx.beginPath();
      ctx.moveTo(0, y + this.noteRowHeight);
      ctx.lineTo(w, y + this.noteRowHeight);
      ctx.stroke();
    }
    ctx.restore();

    // 2. 时间轴网格线与循环选区底色
    ctx.save();
    ctx.translate(-this.scrollX * this.pixelsPerBeat, this.rulerHeight);

    // 循环选区画布高亮列
    if (this.isLooping && this.loopStart !== undefined && this.loopEnd !== undefined) {
      var lx1 = this.loopStart * this.pixelsPerBeat;
      var lx2 = this.loopEnd * this.pixelsPerBeat;
      ctx.fillStyle = isDark ? "rgba(255, 159, 67, 0.04)" : "rgba(255, 159, 67, 0.06)";
      ctx.fillRect(lx1, 0, lx2 - lx1, h - this.rulerHeight);
    }

    var startBeat = Math.max(0, Math.floor(this.scrollX));
    var endBeat = Math.ceil(this.scrollX + w / this.pixelsPerBeat);

    for (var b = startBeat; b <= endBeat; b += this.snapGrid) {
      var bx = b * this.pixelsPerBeat;
      var isBar = (Math.abs(b % 4) < 0.001);
      var isBeat = (Math.abs(b % 1) < 0.001);

      if (isBar) {
        ctx.strokeStyle = isDark ? "rgba(255,255,255,0.25)" : "rgba(0,0,0,0.22)";
        ctx.lineWidth = 1.5;
      } else if (isBeat) {
        ctx.strokeStyle = isDark ? "rgba(255,255,255,0.12)" : "rgba(0,0,0,0.1)";
        ctx.lineWidth = 1;
      } else {
        ctx.strokeStyle = isDark ? "rgba(255,255,255,0.05)" : "rgba(0,0,0,0.04)";
        ctx.lineWidth = 0.8;
      }

      ctx.beginPath();
      ctx.moveTo(bx, 0);
      ctx.lineTo(bx, h - this.rulerHeight);
      ctx.stroke();
    }

    // 3. 幽灵参考轨
    if (this.ghostTrackId) {
      var ghostTab = this.tabs.find(function (t) { return t.id === this.ghostTrackId; }.bind(this));
      if (ghostTab && ghostTab.notes) {
        ctx.fillStyle = isDark ? "rgba(255, 255, 255, 0.18)" : "rgba(0, 0, 0, 0.15)";
        var gLeft = this.scrollX * this.pixelsPerBeat;
        var gRight = gLeft + w;
        ghostTab.notes.forEach(function (n) {
          var gp = this.pitchOf(n.note);
          var gx = n.start * this.pixelsPerBeat;
          var gy = (127 - gp) * this.noteRowHeight - this.scrollY;
          var gw = Math.max(4, (n.end - n.start) * this.pixelsPerBeat);
          if (gx + gw < gLeft - 4 || gx > gRight + 4) return;
          ctx.fillRect(gx, gy, gw, this.noteRowHeight - 1);
        }.bind(this));
      }
    }

    // 4. 当前轨音符
    var tab = this.getActiveTab();
    if (tab && tab.notes) {
      /* 选择集大时逐帧建 Set（O(k) 一次），避免 O(n×k) 的 indexOf 扫描 */
      var selSet = this.selectedNotes.length > 8 ? new Set(this.selectedNotes) : null;
      var worldLeft = this.scrollX * this.pixelsPerBeat;
      var worldRight = worldLeft + w;
      tab.notes.forEach(function (n) {
        var p = this.pitchOf(n.note);
        var nx = n.start * this.pixelsPerBeat;
        var ny = (127 - p) * this.noteRowHeight - this.scrollY;
        var nw = Math.max(4, (n.end - n.start) * this.pixelsPerBeat);
        var nh = this.noteRowHeight - 1;

        /* 世界坐标左缘剔除（此前 <0 恒假：视口左侧的音符每帧都在画） */
        if (nx + nw < worldLeft - 4 || nx > worldRight + 4) return;
        if (ny + nh < 0 || ny > h) return;

        var isSel = selSet ? selSet.has(n) : this.selectedNotes.indexOf(n) !== -1;
        var velAlpha = 0.45 + (n.velocity / 127) * 0.55;

        ctx.fillStyle = isSel
          ? (isDark ? "#ff9f43" : "#ee5253")
          : (n.muted ? (isDark ? "#57606f" : "#a4b0be") : (isDark ? "rgba(72, 219, 251, " + velAlpha + ")" : "rgba(10, 189, 227, " + velAlpha + ")"));

        this.roundRect(ctx, nx + 0.5, ny + 0.5, nw - 1, nh, 3, true, true);

        if (isSel) {
          ctx.strokeStyle = "#ffffff";
          ctx.lineWidth = 1.5;
          ctx.stroke();
        }

        if (nw > 20) {
          ctx.fillStyle = isDark ? "#000000" : "#ffffff";
          ctx.font = "bold 9px sans-serif";
          ctx.fillText(n.note, nx + 4, ny + nh - 3);
        }
      }.bind(this));
    }

    // 4.5 FL Studio 经典：被删除音符边缘膨胀放大渐隐特效
    if (this.deleteEffects && this.deleteEffects.length > 0) {
      var now = performance.now();
      var activeEffects = [];
      for (var k = 0; k < this.deleteEffects.length; k++) {
        var eff = this.deleteEffects[k];
        var elapsed = now - eff.startTime;
        var p = elapsed / eff.duration;
        if (p < 1) {
          activeEffects.push(eff);
          var ease = 1 - Math.pow(1 - p, 2); // 柔和减速衰减
          var alpha = (1 - p);
          var expand = ease * 5.5; // 边缘向四周膨胀 5.5px

          var ex = eff.start * this.pixelsPerBeat - expand;
          var ey = (127 - eff.pitch) * this.noteRowHeight - this.scrollY - expand;
          var ew = (eff.end - eff.start) * this.pixelsPerBeat + expand * 2;
          var eh = this.noteRowHeight + expand * 2;

          ctx.save();
          ctx.fillStyle = isDark ? "rgba(255, 107, 107, " + (alpha * 0.35) + ")" : "rgba(238, 82, 83, " + (alpha * 0.35) + ")";
          ctx.strokeStyle = isDark ? "rgba(255, 255, 255, " + (alpha * 0.95) + ")" : "rgba(255, 255, 255, " + (alpha * 0.95) + ")";
          ctx.lineWidth = 1.5 + ease * 1.5;
          this.roundRect(ctx, ex + 0.5, ey + 0.5, ew - 1, eh, 3 + ease * 2, true, true);
          ctx.restore();
        }
      }
      this.deleteEffects = activeEffects;
      if (this.deleteEffects.length > 0) {
        this.requestEffectFrame();
      }
    }

    // 5. 框选矩形
    if (this.dragState && this.dragState.type === "select_box") {
      var sBox = this.dragState;
      ctx.fillStyle = "rgba(72, 219, 251, 0.2)";
      ctx.strokeStyle = "#48dbfb";
      ctx.lineWidth = 1;
      var bx1 = Math.min(sBox.startX, sBox.curX);
      var by1 = Math.min(sBox.startY, sBox.curY) - this.scrollY;
      var bw = Math.abs(sBox.curX - sBox.startX);
      var bh = Math.abs(sBox.curY - sBox.startY);
      ctx.fillRect(bx1, by1, bw, bh);
      ctx.strokeRect(bx1, by1, bw, bh);
    }

    // 6. 切片预览线：未跨行=跟随鼠标的竖直切线；跨行=起点→终点的斜线
    if (this.dragState && this.dragState.type === "slice_line") {
      var sl = this.dragState;
      var ax = sl.startBeat * this.pixelsPerBeat;
      var bx = sl.curBeat * this.pixelsPerBeat;
      var ay = (127 - sl.startPitch) * this.noteRowHeight - this.scrollY + this.noteRowHeight / 2;
      var by = (127 - sl.curPitch) * this.noteRowHeight - this.scrollY + this.noteRowHeight / 2;
      ctx.strokeStyle = "#ff4757";
      ctx.lineWidth = 2;
      ctx.setLineDash([4, 4]);
      ctx.beginPath();
      if (sl.startPitch === sl.curPitch) {
        ctx.moveTo(bx, 0);
        ctx.lineTo(bx, h);
      } else {
        ctx.moveTo(ax, ay);
        ctx.lineTo(bx, by);
      }
      ctx.stroke();
      ctx.setLineDash([]);
    }

    // 7. 播放头
    var px = this.playheadBeat * this.pixelsPerBeat;
    ctx.strokeStyle = "#ff4757";
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(px, 0);
    ctx.lineTo(px, h);
    ctx.stroke();

    ctx.restore();

    // 8. 标尺
    this.renderRuler(ctx, w, isDark);
  };

  PianoRoll.prototype.renderRuler = function (ctx, w, isDark) {
    ctx.save();
    ctx.fillStyle = isDark ? "#101724" : "#e4e7eb";
    ctx.fillRect(0, 0, w, this.rulerHeight);
    ctx.strokeStyle = isDark ? "#283446" : "#cbd1d8";
    ctx.beginPath();
    ctx.moveTo(0, this.rulerHeight);
    ctx.lineTo(w, this.rulerHeight);
    ctx.stroke();

    ctx.translate(-this.scrollX * this.pixelsPerBeat, 0);

    var startBeat = Math.max(0, Math.floor(this.scrollX));
    var endBeat = Math.ceil(this.scrollX + w / this.pixelsPerBeat);

    // 循环选区 (FL Studio 风格顶置标尺选区 + 抓手调节端点)
    if (this.loopStart !== undefined && this.loopEnd !== undefined) {
      var lx1 = this.loopStart * this.pixelsPerBeat;
      var lx2 = this.loopEnd * this.pixelsPerBeat;
      var lWidth = lx2 - lx1;

      if (this.isLooping) {
        // 活跃循环选区底色
        ctx.fillStyle = isDark ? "rgba(255, 159, 67, 0.22)" : "rgba(255, 159, 67, 0.3)";
        ctx.fillRect(lx1, 0, lWidth, this.rulerHeight);

        // 顶部橘色亮条
        ctx.fillStyle = "#ff9f43";
        ctx.fillRect(lx1, 0, lWidth, 4);

        // 左端点手柄 [◀
        ctx.fillStyle = "#ff9f43";
        ctx.fillRect(lx1, 0, 3, 14);
        ctx.beginPath();
        ctx.moveTo(lx1, 14);
        ctx.lineTo(lx1 + 4, 14);
        ctx.lineTo(lx1, 18);
        ctx.fill();

        // 右端点手柄 ▶]
        ctx.fillRect(lx2 - 3, 0, 3, 14);
        ctx.beginPath();
        ctx.moveTo(lx2, 14);
        ctx.lineTo(lx2 - 4, 14);
        ctx.lineTo(lx2, 18);
        ctx.fill();

        // 循环长度提示
        if (lWidth > 55) {
          ctx.fillStyle = isDark ? "#ff9f43" : "#d35400";
          ctx.font = "bold 9px sans-serif";
          var loopDur = Math.round((this.loopEnd - this.loopStart) * 100) / 100;
          ctx.fillText("循环: " + loopDur + " 拍", lx1 + 8, 12);
        }
      } else {
        // 禁用状态的循环选区 (微弱半透明虚线框)
        ctx.fillStyle = isDark ? "rgba(255, 255, 255, 0.03)" : "rgba(0, 0, 0, 0.03)";
        ctx.fillRect(lx1, 0, lWidth, this.rulerHeight);
        ctx.strokeStyle = isDark ? "#57606f" : "#a4b0be";
        ctx.setLineDash([2, 2]);
        ctx.strokeRect(lx1, 1, lWidth, this.rulerHeight - 2);
        ctx.setLineDash([]);
      }
    }

    for (var b = startBeat; b <= endBeat; b++) {
      var bx = b * this.pixelsPerBeat;
      var isBar = (b % 4 === 0);

      if (isBar) {
        ctx.fillStyle = isDark ? "#ffffff" : "#2f3640";
        ctx.font = "bold 11px sans-serif";
        ctx.fillText("" + (b / 4 + 1), bx + 4, 22);
        ctx.strokeStyle = isDark ? "#ffffff" : "#2f3640";
        ctx.beginPath();
        ctx.moveTo(bx, this.rulerHeight - 8);
        ctx.lineTo(bx, this.rulerHeight);
        ctx.stroke();
      } else {
        ctx.strokeStyle = isDark ? "#718093" : "#a4b0be";
        ctx.beginPath();
        ctx.moveTo(bx, this.rulerHeight - 4);
        ctx.lineTo(bx, this.rulerHeight);
        ctx.stroke();
      }
    }

    var hx = this.playheadBeat * this.pixelsPerBeat;
    ctx.fillStyle = "#ff4757";
    ctx.beginPath();
    ctx.moveTo(hx - 5, 0);
    ctx.lineTo(hx + 5, 0);
    ctx.lineTo(hx, 10);
    ctx.closePath();
    ctx.fill();

    ctx.restore();
  };

  PianoRoll.prototype.renderVelocity = function () {
    if (!this.velocityCtx || !this.velocityCanvas) return;
    var ctx = this.velocityCtx;
    var w = this.velocityCanvas.width / (window.devicePixelRatio || 1);
    var h = this.velocityHeight;

    ctx.clearRect(0, 0, w, h);
    var isDark = document.documentElement.dataset.theme === "dark";

    ctx.fillStyle = isDark ? "#121926" : "#f1f3f6";
    ctx.fillRect(0, 0, w, h);
    ctx.strokeStyle = isDark ? "#283446" : "#dcdde1";
    ctx.strokeRect(0, 0, w, h);

    var tab = this.getActiveTab();
    if (!tab || !tab.notes) return;

    ctx.save();
    ctx.translate(-this.scrollX * this.pixelsPerBeat, 0);

    // 绘制力度参考虚线 (78% / 100 vel)
    var defY = h - (100 / 127) * (h - 18) - 4;
    ctx.strokeStyle = isDark ? "rgba(255,255,255,0.08)" : "rgba(0,0,0,0.06)";
    ctx.setLineDash([2, 4]);
    ctx.beginPath();
    ctx.moveTo(0, defY);
    ctx.lineTo(10000, defY);
    ctx.stroke();
    ctx.setLineDash([]);

    tab.notes.forEach(function (n) {
      // FL Studio 风格：力度柱与球位于音符起始节拍位置
      var cx = Math.round(n.start * this.pixelsPerBeat) + 0.5;
      var curVel = parseInt(n.velocity, 10);
      if (isNaN(curVel)) curVel = 100;
      var vH = (curVel / 127) * (h - 18);
      var vy = h - vH - 4;

      var isSel = this.selectedNotes.indexOf(n) !== -1;

      // 竖线 (Stalk)
      ctx.strokeStyle = isSel ? "#ff9f43" : (isDark ? "#48dbfb" : "#0abde3");
      ctx.lineWidth = isSel ? 2.5 : 2;
      ctx.beginPath();
      ctx.moveTo(cx, h);
      ctx.lineTo(cx, vy);
      ctx.stroke();

      // 顶部圆把手 (Handle / Ball)
      ctx.fillStyle = isSel ? "#ff9f43" : (isDark ? "#48dbfb" : "#0abde3");
      ctx.beginPath();
      ctx.arc(cx, vy, isSel ? 4.5 : 3.5, 0, Math.PI * 2);
      ctx.fill();
    }.bind(this));

    // 如果正在画线调力度，绘制引导线
    if (this.dragState && (this.dragState.type === "velocity_line" || this.dragState.type === "velocity_brush")) {
      ctx.strokeStyle = "#ff9f43";
      ctx.lineWidth = 1.5;
      ctx.setLineDash([3, 3]);
      ctx.beginPath();
      ctx.moveTo(this.dragState.startBeat * this.pixelsPerBeat, this.dragState.startY);
      ctx.lineTo(this.dragState.curBeat * this.pixelsPerBeat, this.dragState.curY);
      ctx.stroke();
      ctx.setLineDash([]);
    }

    ctx.restore();
  };

  PianoRoll.prototype.roundRect = function (ctx, x, y, w, h, r, fill, stroke) {
    if (w < 2 * r) r = w / 2;
    if (h < 2 * r) r = h / 2;
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.arcTo(x + w, y, x + w, y + h, r);
    ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r);
    ctx.arcTo(x, y, x + w, y, r);
    ctx.closePath();
    if (fill) ctx.fill();
    if (stroke) ctx.stroke();
  };

  /* ═══════════ FL Studio 经典交互事件系统 ═══════════ */

  PianoRoll.prototype.bindCanvasEvents = function () {
    var self = this;
    var canvas = this.gridCanvas;
    if (!canvas) return;

    // 禁用右键与中键默认菜单/行为
    canvas.addEventListener("contextmenu", function (e) { e.preventDefault(); });
    canvas.addEventListener("auxclick", function (e) { e.preventDefault(); });
    if (this.velocityCanvas) {
      this.velocityCanvas.addEventListener("auxclick", function (e) { e.preventDefault(); });
    }

    canvas.addEventListener("mousedown", function (e) {
      self.setFocus(true);
      self.handleGridMouseDown(e);
    });

    canvas.addEventListener("dblclick", function (e) {
      self.handleGridDblClick(e);
    });

    window.addEventListener("mousemove", function (e) {
      self.handleGridMouseMove(e);
    });

    window.addEventListener("mouseup", function (e) {
      self.handleGridMouseUp(e);
    });

    // 鼠标滚轮（缩放、Alt+力度、Shift+微移）
    canvas.addEventListener("wheel", function (e) {
      e.preventDefault();
      var pos = self.getGridCoords(e);
      var hitNote = self.findNoteAt(pos.beat, pos.pitch);

      if (e.altKey) {
        // Alt + 滚轮：调节音符力度 (FL Studio 经典)
        var dVel = e.deltaY < 0 ? 5 : -5;
        var tab = self.getActiveTab();
        if (!tab) return;

        var targetNotes = [];
        if (hitNote) {
          if (self.selectedNotes.length > 1 && self.selectedNotes.indexOf(hitNote) !== -1) {
            targetNotes = self.selectedNotes;
          } else {
            targetNotes = [hitNote];
            self.selectedNotes = [hitNote];
          }
        } else if (self.selectedNotes.length) {
          targetNotes = self.selectedNotes;
        }

        if (targetNotes.length) {
          self.pushHistory();
          targetNotes.forEach(function (n) {
            var curV = parseInt(n.velocity, 10);
            if (isNaN(curV)) curV = 100;
            n.velocity = Math.max(1, Math.min(127, curV + dVel));
          });
          var rep = targetNotes[0];
          self.showHUD("力度: " + rep.velocity + " (" + Math.round(rep.velocity / 1.27) + "%)");
          self.render();
        }
      } else if (e.shiftKey) {
        // Shift + 滚轮：水平微移 (Nudge)
        var dBeat = (e.deltaY > 0 ? 1 : -1) * self.snapGrid;
        var targets = self.selectedNotes.length ? self.selectedNotes : (hitNote ? [hitNote] : []);
        if (targets.length) {
          self.pushHistory();
          targets.forEach(function (n) {
            var dur = n.end - n.start;
            n.start = Math.max(0, Math.round((n.start + dBeat) * 1000) / 1000);
            n.end = Math.round((n.start + dur) * 1000) / 1000;
          });
          self.showHUD("微移: " + (dBeat > 0 ? "+" : "") + dBeat + " 拍");
          self.render();
        } else {
          self.scrollX = Math.max(0, self.scrollX + (e.deltaY > 0 ? 1 : -1));
          self.render();
        }
      } else if (e.ctrlKey) {
        // Ctrl + 滚轮：时间水平缩放
        var zoomFactor = e.deltaY < 0 ? 1.15 : 0.87;
        self.pixelsPerBeat = Math.max(16, Math.min(256, self.pixelsPerBeat * zoomFactor));
        self.showHUD("缩放: " + Math.round(self.pixelsPerBeat) + " px/beat");
        self.render();
      } else {
        // 普通垂直滚动
        self.scrollY = Math.max(0, Math.min(128 * self.noteRowHeight - 100, self.scrollY + e.deltaY * 0.8));
        self.render();
      }
    });

    // 力度窗交互 (单点调整 + 连续画线调力度)
    if (this.velocityCanvas) {
      this.velocityCanvas.addEventListener("contextmenu", function (e) {
        e.preventDefault();
        self.handleVelocityRightClick(e);
      });
      this.velocityCanvas.addEventListener("mousedown", function (e) {
        self.setFocus(true);
        self.handleVelocityMouseDown(e);
      });
      this.velocityCanvas.addEventListener("dblclick", function () {
        self.openLevelScaleModal();
      });
    }

    // 琴键侧边栏点击试听
    if (this.keysCanvas) {
      this.keysCanvas.addEventListener("mousedown", function (e) {
        var rect = self.keysCanvas.getBoundingClientRect();
        var my = e.clientY - rect.top - self.rulerHeight + self.scrollY;
        var p = 127 - Math.floor(my / self.noteRowHeight);
        if (p >= 0 && p <= 127) {
          self.playNoteSound(p, 100);
          setTimeout(function () { self.stopNoteSound(p); }, 300);
        }
      });
    }
  };

  PianoRoll.prototype.getGridCoords = function (e) {
    var rect = this.gridCanvas.getBoundingClientRect();
    var mx = e.clientX - rect.left;
    var my = e.clientY - rect.top;

    var beat = this.scrollX + (mx / this.pixelsPerBeat);
    var pitch = 127 - Math.floor((my - this.rulerHeight + this.scrollY) / this.noteRowHeight);

    return {
      mx: mx,
      my: my,
      beat: beat,
      pitch: Math.max(0, Math.min(127, pitch)),
      inRuler: (my <= this.rulerHeight)
    };
  };

  PianoRoll.prototype.findNoteAt = function (beat, pitch) {
    var tab = this.getActiveTab();
    if (!tab || !tab.notes) return null;
    return tab.notes.find(function (n) {
      var p = Tools.noteNameToNumber(n.note);
      return p === pitch && beat >= n.start && beat <= n.end;
    }) || null;
  };

  PianoRoll.prototype.handleGridMouseDown = function (e) {
    // 0. 视图平移拖拽模式 (FL Studio 经典：按住鼠标中键 或 Shift + 按住鼠标右键)
    if (e.button === 1 || (e.shiftKey && e.button === 2)) {
      e.preventDefault();
      document.body.classList.add("is-panning");
      if (this.drawerEl) this.drawerEl.classList.add("is-panning");
      if (this.gridCanvas) this.gridCanvas.classList.add("is-panning");

      this.dragState = {
        type: "pan_view",
        startClientX: e.clientX,
        startClientY: e.clientY,
        origScrollX: this.scrollX,
        origScrollY: this.scrollY
      };
      return;
    }

    var pos = this.getGridCoords(e);
    var tab = this.getActiveTab();
    if (!tab) return;

    // 1. 标尺区域 (时间轴跳转、循环选区边界调节与创建)
    if (pos.inRuler) {
      var lx1 = (this.loopStart !== undefined ? this.loopStart : 0) * this.pixelsPerBeat;
      var lx2 = (this.loopEnd !== undefined ? this.loopEnd : 16) * this.pixelsPerBeat;

      // 标尺右键拖拽：拉取建立新的循环选区 (FL Studio 经典)
      if (e.button === 2) {
        this.isLooping = true;
        var snappedB = Math.max(0, Math.floor(pos.beat / this.snapGrid) * this.snapGrid);
        this.loopStart = snappedB;
        this.loopEnd = snappedB + this.snapGrid;
        this.dragState = { type: "ruler_loop_create", startBeat: pos.beat };
        var loopBtn = document.getElementById("prLoopToggleBtn");
        if (loopBtn) loopBtn.classList.add("active");
        this.render();
        return;
      }

      // 标尺左键：判断是否点中左把手、右把手、选区中条移动 或 播放头跳步
      if (e.button === 0) {
        var nearLeft = Math.abs(pos.mx - (lx1 - this.scrollX * this.pixelsPerBeat)) <= 8;
        var nearRight = Math.abs(pos.mx - (lx2 - this.scrollX * this.pixelsPerBeat)) <= 8;

        if (nearLeft) {
          this.dragState = { type: "ruler_loop_left" };
          return;
        }
        if (nearRight) {
          this.dragState = { type: "ruler_loop_right" };
          return;
        }
        if (this.isLooping && pos.my <= 16 && pos.beat >= this.loopStart && pos.beat <= this.loopEnd) {
          this.dragState = {
            type: "ruler_loop_move",
            startBeat: pos.beat,
            origStart: this.loopStart,
            origEnd: this.loopEnd
          };
          return;
        }

        // 默认点击标尺移动播放头
        this.playheadBeat = Math.max(0, Math.round(pos.beat / this.snapGrid) * this.snapGrid);
        this.dragState = { type: "ruler_scrub" };
        this.render();
        return;
      }
    }

    // 2. 缩放工具模式 (Zoom)
    if (this.currentTool === "zoom") {
      if (e.button === 2) {
        this.pixelsPerBeat = Math.max(16, this.pixelsPerBeat * 0.8);
      } else {
        this.pixelsPerBeat = Math.min(256, this.pixelsPerBeat * 1.25);
      }
      this.showHUD("缩放: " + Math.round(this.pixelsPerBeat) + " px/beat");
      this.render();
      return;
    }

    // 3. 右键点击 (FL Studio 经典：即刻擦除，若有选区则顺便清空选区)
    if (e.button === 2 || this.currentTool === "erase") {
      // 动态挂载橡皮擦光标样式
      if (this.drawerEl) this.drawerEl.classList.add("is-right-erasing");
      if (this.gridCanvas) this.gridCanvas.classList.add("is-right-erasing");
      document.body.classList.add("is-right-erasing");

      // 顺便清空框选
      if (this.selectedNotes.length > 0) {
        this.selectedNotes = [];
      }

      var noteToErase = this.findNoteAt(pos.beat, pos.pitch);
      if (noteToErase) {
        this.pushHistory();
        this.addDeleteEffect(noteToErase);
        var idx = tab.notes.indexOf(noteToErase);
        if (idx !== -1) tab.notes.splice(idx, 1);
      }
      this.render();
      this.dragState = { type: "erase_sweep" };
      return;
    }

    // 3. 切片刀模式：按下记录起点，拖拽画线（跨行后锁定角度，
    //    继续横向移动=平移整根线），松开时切分被线经过的音符
    if (this.currentTool === "slice") {
      this.dragState = {
        type: "slice_line",
        startBeat: pos.beat, startPitch: pos.pitch,
        curBeat: pos.beat, curPitch: pos.pitch,
        anchored: false
      };
      this.render();
      return;
    }

    // 4. 静音工具
    if (this.currentTool === "mute") {
      var noteToMute = this.findNoteAt(pos.beat, pos.pitch);
      if (noteToMute) {
        this.pushHistory();
        noteToMute.muted = !noteToMute.muted;
        this.render();
      }
      return;
    }

    // 5. Ctrl + 拖拽 或 框选工具 (快捷框选)
    if (this.currentTool === "select" || e.ctrlKey) {
      this.dragState = {
        type: "select_box",
        startX: pos.beat * this.pixelsPerBeat,
        startY: (127 - pos.pitch) * this.noteRowHeight,
        curX: pos.beat * this.pixelsPerBeat,
        curY: (127 - pos.pitch) * this.noteRowHeight
      };
      return;
    }

    // 6. 画笔/笔刷工具
    var hitNote = this.findNoteAt(pos.beat, pos.pitch);
    if (hitNote) {
      // 检查是否点在右边缘调整长度 (Resize)
      var noteEndPx = hitNote.end * this.pixelsPerBeat;
      var curPx = pos.beat * this.pixelsPerBeat;
      var isResizeEdge = Math.abs(curPx - noteEndPx) < 10;

      if (!e.ctrlKey && this.selectedNotes.indexOf(hitNote) === -1) {
        this.selectedNotes = [hitNote];
      }

      this.playNoteSound(pos.pitch, hitNote.velocity);

      /* 暂存变异前快照：mouseup 确认发生位移才正式入栈（点击选中
         不污染撤销栈）；此前移动/缩放音符完全没有历史快照——不可撤销 */
      var gestureSnapshot = JSON.stringify(tab.notes);

      // Shift + 拖拽：克隆复制音符（FL 惯例：位移超阈值才克隆，点击不误触）
      if (e.shiftKey) {
        this.dragState = {
          type: "move_note",
          startBeat: pos.beat,
          startPitch: pos.pitch,
          freeSnap: e.altKey,
          clonePending: true,
          snapshot: gestureSnapshot,
          changed: false,
          origNotes: this.selectedNotes.map(function (n) {
            return { note: n, start: n.start, end: n.end, pitch: Tools.noteNameToNumber(n.note) };
          })
        };
        this.render();
        return;
      }

      if (isResizeEdge) {
        this.dragState = {
          type: "resize_note",
          note: hitNote,
          startBeat: hitNote.end,
          freeSnap: e.altKey,
          snapshot: gestureSnapshot,
          changed: false
        };
      } else {
        this.dragState = {
          type: "move_note",
          startBeat: pos.beat,
          startPitch: pos.pitch,
          freeSnap: e.altKey,
          snapshot: gestureSnapshot,
          changed: false,
          origNotes: this.selectedNotes.map(function (n) {
            return { note: n, start: n.start, end: n.end, pitch: Tools.noteNameToNumber(n.note) };
          })
        };
      }
      this.render();
      return;
    }

    // 点击空白处：和弦印章 or 创建新音符
    this.pushHistory();
    var snappedBeat = Math.floor(pos.beat / this.snapGrid) * this.snapGrid;

    if (this.chordStamp !== "none") {
      var stamped = Tools.stampChord(pos.pitch, this.chordStamp, snappedBeat, 1.0, 100);
      tab.notes = tab.notes.concat(stamped);
      this.selectedNotes = stamped;
      stamped.forEach(function (n) {
        this.playNoteSound(Tools.noteNameToNumber(n.note), 100);
      }.bind(this));
    } else {
      var newNote = {
        note: Tools.numberToNoteName(pos.pitch),
        velocity: 100,
        start: snappedBeat,
        end: snappedBeat + Math.max(this.snapGrid, 0.5)
      };
      tab.notes.push(newNote);
      this.selectedNotes = [newNote];
      this.playNoteSound(pos.pitch, 100);

      this.dragState = {
        type: "resize_note",
        note: newNote,
        startBeat: newNote.end,
        freeSnap: e.altKey
      };
    }
    this.render();
  };

  PianoRoll.prototype.handleGridDblClick = function (e) {
    var pos = this.getGridCoords(e);
    if (pos.inRuler) {
      this.toggleLoop();
      return;
    }
    var hitNote = this.findNoteAt(pos.beat, pos.pitch);
    if (hitNote) {
      this.openNotePropertiesModal(hitNote);
    }
  };

  PianoRoll.prototype.handleGridMouseMove = function (e) {
    // 鼠标悬停但未按下：动态更新光标样式
    if (!this.dragState) {
      var pos = this.getGridCoords(e);
      if (pos.inRuler && this.gridCanvas) {
        var lx1 = (this.loopStart !== undefined ? this.loopStart : 0) * this.pixelsPerBeat;
        var lx2 = (this.loopEnd !== undefined ? this.loopEnd : 16) * this.pixelsPerBeat;
        var nearLeft = Math.abs(pos.mx - (lx1 - this.scrollX * this.pixelsPerBeat)) <= 8;
        var nearRight = Math.abs(pos.mx - (lx2 - this.scrollX * this.pixelsPerBeat)) <= 8;

        if (nearLeft || nearRight) {
          this.gridCanvas.style.cursor = "ew-resize";
        } else if (this.isLooping && pos.my <= 16 && pos.beat >= this.loopStart && pos.beat <= this.loopEnd) {
          this.gridCanvas.style.cursor = "grab";
        } else {
          this.gridCanvas.style.cursor = "pointer";
        }
      } else if (this.gridCanvas) {
        this.gridCanvas.style.cursor = "";
      }
      return;
    }

    if (this.dragState.type === "pan_view") {
      var dx = e.clientX - this.dragState.startClientX;
      var dy = e.clientY - this.dragState.startClientY;

      var deltaBeats = dx / this.pixelsPerBeat;
      this.scrollX = Math.max(0, this.dragState.origScrollX - deltaBeats);
      this.scrollY = Math.max(0, Math.min(128 * this.noteRowHeight - 100, this.dragState.origScrollY - dy));
      this.scheduleRender();
      return;
    }

    var pos = this.getGridCoords(e);
    var tab = this.getActiveTab();
    if (!tab) return;

    if (this.dragState.type === "ruler_scrub") {
      this.playheadBeat = Math.max(0, pos.beat);
      this.scheduleRender();
    } else if (this.dragState.type === "ruler_loop_left") {
      var snapped = Math.max(0, Math.round(pos.beat / this.snapGrid) * this.snapGrid);
      this.loopStart = Math.min(snapped, this.loopEnd - this.snapGrid);
      this.scheduleRender();
    } else if (this.dragState.type === "ruler_loop_right") {
      var snapped = Math.max(this.loopStart + this.snapGrid, Math.round(pos.beat / this.snapGrid) * this.snapGrid);
      this.loopEnd = snapped;
      this.scheduleRender();
    } else if (this.dragState.type === "ruler_loop_move") {
      var delta = Math.round((pos.beat - this.dragState.startBeat) / this.snapGrid) * this.snapGrid;
      var dur = this.dragState.origEnd - this.dragState.origStart;
      var newStart = Math.max(0, this.dragState.origStart + delta);
      this.loopStart = newStart;
      this.loopEnd = newStart + dur;
      this.scheduleRender();
    } else if (this.dragState.type === "ruler_loop_create") {
      var b1 = Math.min(this.dragState.startBeat, pos.beat);
      var b2 = Math.max(this.dragState.startBeat, pos.beat);
      this.loopStart = Math.max(0, Math.floor(b1 / this.snapGrid) * this.snapGrid);
      this.loopEnd = Math.max(this.loopStart + this.snapGrid, Math.ceil(b2 / this.snapGrid) * this.snapGrid);
      this.scheduleRender();
    } else if (this.dragState.type === "slice_line") {
      /* 切片拖拽：未跨行时终点跟随鼠标（竖直切线随横移平移）；
         一旦跨到不同音高行即锁定线的角度——此后横向移动整根线平移
         （保持斜率与纵向跨度），与 FL Studio 的 Chop 行为一致 */
      var sl = this.dragState;
      if (!sl.anchored) {
        sl.curBeat = pos.beat;
        sl.curPitch = pos.pitch;
        if (pos.pitch !== sl.startPitch) sl.anchored = true;
      } else {
        var newCur = pos.beat;
        var deltaB = newCur - sl.curBeat;
        sl.curBeat = newCur;
        sl.startBeat += deltaB;
      }
      this.scheduleRender();
    } else if (this.dragState.type === "erase_sweep") {
      var n = this.findNoteAt(pos.beat, pos.pitch);
      if (n) {
        var idx = tab.notes.indexOf(n);
        if (idx !== -1) {
          this.addDeleteEffect(n);
          tab.notes.splice(idx, 1);
          this.scheduleRender();
        }
      }
    } else if (this.dragState.type === "resize_note") {
      var note = this.dragState.note;
      var snap = this.dragState.freeSnap ? 0.02 : this.snapGrid;
      var snappedEnd = Math.max(note.start + snap, Math.round(pos.beat / snap) * snap);
      var snappedRounded = Math.round(snappedEnd * 1000) / 1000;
      if (Math.abs(snappedRounded - note.end) > 1e-9) this.dragState.changed = true;
      note.end = snappedRounded;
      this.scheduleRender();
    } else if (this.dragState.type === "move_note") {
      var ds = this.dragState;

      /* 延迟克隆：Shift 按下后位移超过阈值（4px 等效）才克隆副本，
         单纯 Shift+点击不产生克隆——FL Piano Roll 的复制是"拖动"语义 */
      if (ds.clonePending) {
        var dragPx = Math.abs(pos.beat - ds.startBeat) * this.pixelsPerBeat +
                     Math.abs(pos.pitch - ds.startPitch) * this.noteRowHeight;
        if (dragPx <= 4) return;
        this.pushHistory();   // 快照=克隆前（undo 一次回滚整个克隆+拖动）
        var cloned = this.selectedNotes.map(function (n) {
          return { note: n.note, velocity: n.velocity, start: n.start, end: n.end, muted: n.muted };
        });
        tab.notes = tab.notes.concat(cloned);
        this.selectedNotes = cloned;
        ds.origNotes = cloned.map(function (n) {
          return { note: n, start: n.start, end: n.end, pitch: Tools.noteNameToNumber(n.note) };
        });
        ds.clonePending = false;
        ds.changed = true;
        ds.historyPushed = true;
        this.showHUD("⧉ 克隆拖动");
      }

      var snapM = ds.freeSnap ? 0.02 : this.snapGrid;
      var deltaBeat = Math.round((pos.beat - ds.startBeat) / snapM) * snapM;
      var deltaPitch = pos.pitch - ds.startPitch;
      if (deltaBeat !== 0 || deltaPitch !== 0) ds.changed = true;

      ds.origNotes.forEach(function (item) {
        var newStart = Math.max(0, Math.round((item.start + deltaBeat) * 1000) / 1000);
        var dur = item.end - item.start;
        item.note.start = newStart;
        item.note.end = Math.round((newStart + dur) * 1000) / 1000;
        var newP = Math.max(0, Math.min(127, item.pitch + deltaPitch));
        item.note.note = Tools.numberToNoteName(newP);
      });
      this.scheduleRender();
    } else if (this.dragState.type === "select_box") {
      this.dragState.curX = pos.beat * this.pixelsPerBeat;
      this.dragState.curY = (127 - pos.pitch) * this.noteRowHeight;

      var minB = Math.min(this.dragState.startX, this.dragState.curX) / this.pixelsPerBeat;
      var maxB = Math.max(this.dragState.startX, this.dragState.curX) / this.pixelsPerBeat;
      var minP = 127 - Math.max(this.dragState.startY, this.dragState.curY) / this.noteRowHeight;
      var maxP = 127 - Math.min(this.dragState.startY, this.dragState.curY) / this.noteRowHeight;

      var pitchOf = this.pitchOf.bind(this);
      this.selectedNotes = tab.notes.filter(function (n) {
        var p = pitchOf(n.note);
        return n.start < maxB && n.end > minB && p >= minP && p <= maxP;
      });
      this.scheduleRender();
    }
  };

  PianoRoll.prototype.handleGridMouseUp = function (e) {
    document.body.classList.remove("is-panning", "is-right-erasing");
    if (this.drawerEl) this.drawerEl.classList.remove("is-panning", "is-right-erasing");
    if (this.gridCanvas) this.gridCanvas.classList.remove("is-panning", "is-right-erasing");

    if (!this.dragState) return;
    var tab = this.getActiveTab();

    /* 移动/缩放/克隆手势收尾：确认发生过变异才把 mousedown 暂存的
       快照正式入撤销栈（克隆在拖动阈值处已自行入栈，此处跳过）。
       此前移动/缩放音符没有任何历史快照——拖错位置无法 Ctrl+Z */
    var gs = this.dragState;
    if (tab && gs.snapshot && gs.changed && !gs.historyPushed) {
      tab.undoStack.push(gs.snapshot);
      if (tab.undoStack.length > 50) tab.undoStack.shift();
      tab.redoStack = [];
      tab.dirty = true;
      this.renderTabs();
      this.scheduleAutoSave();
    }

    if (this.dragState.type === "slice_line" && tab) {
      var sl = this.dragState;
      var pitchOf = this.pitchOf.bind(this);
      /* 每个音符的切割拍：
         - 竖直切线（未跨行）：所有跨过切线拍的音符在该拍切开（保持旧点击行为）
         - 斜线（跨行）：线段经过的每个音高行按行中心交点换算切割拍 */
      var isDiagonal = sl.startPitch !== sl.curPitch;
      var lo = Math.min(sl.startPitch, sl.curPitch);
      var hi = Math.max(sl.startPitch, sl.curPitch);
      var cutCount = 0;

      this.pushHistory();   // 快照必须在变异前（原实现先切后快照，撤销无效）
      var newNotes = [];
      tab.notes.forEach(function (n) {
        var cut = null;
        if (isDiagonal) {
          var r = pitchOf(n.note);
          if (r >= lo && r <= hi) {
            var t = (r - sl.startPitch) / (sl.curPitch - sl.startPitch);
            cut = sl.startBeat + t * (sl.curBeat - sl.startBeat);
          }
        } else {
          cut = sl.curBeat;
        }
        if (cut !== null && n.start < cut - 1e-6 && n.end > cut + 1e-6) {
          cut = Math.round(cut * 1000) / 1000;
          var left = Object.assign({}, n, { end: cut });
          var right = Object.assign({}, n, { start: cut });
          newNotes.push(left, right);
          cutCount++;
        } else {
          newNotes.push(n);
        }
      });
      tab.notes = newNotes;
      if (cutCount) {
        this.triggerAutoSave();
        this.showHUD("✂ 已切分 " + cutCount + " 个音符");
      }
    }

    this.dragState = null;
    this.stopAllSounds();
    this.render();
  };

  /* ═══════════ 力度窗区域计算与连续画笔调节 (FL Studio 风格) ═══════════ */

  // 获取音符分组与其有效生效区域 (生效范围：从当前音符起始直到下一个音符起始)
  PianoRoll.prototype.getVelocityNoteGroups = function (notesList) {
    if (!notesList || !notesList.length) return [];
    var sorted = notesList.slice().sort(function (a, b) { return a.start - b.start; });

    var groups = [];
    var currentGroup = null;

    sorted.forEach(function (n) {
      // 确保 velocity 格式为安全数值
      var v = parseInt(n.velocity, 10);
      if (isNaN(v)) v = 100;
      n.velocity = Math.max(1, Math.min(127, v));

      if (!currentGroup || (n.start - currentGroup.start) > 0.05) {
        currentGroup = {
          start: n.start,
          maxEnd: n.end,
          notes: [n]
        };
        groups.push(currentGroup);
      } else {
        currentGroup.notes.push(n);
        if (n.end > currentGroup.maxEnd) {
          currentGroup.maxEnd = n.end;
        }
      }
    });

    for (var i = 0; i < groups.length; i++) {
      var g = groups[i];
      g.zoneStart = (i === 0) ? (g.start - 0.5) : g.start;
      if (i < groups.length - 1) {
        g.zoneEnd = groups[i + 1].start;
      } else {
        g.zoneEnd = Math.max(g.maxEnd, g.start + 4.0);
      }
    }

    return groups;
  };

  // 根据拍数定位生效的音符组 (包含球后方整片区域直至下一个球)
  PianoRoll.prototype.findVelocityGroupAt = function (beat, groups) {
    if (!groups || !groups.length) return null;

    for (var i = 0; i < groups.length; i++) {
      var g = groups[i];
      if (beat >= g.zoneStart && beat < g.zoneEnd) {
        return g;
      }
    }

    if (beat < groups[0].zoneStart) {
      return groups[0];
    }
    return groups[groups.length - 1];
  };

  PianoRoll.prototype.handleVelocityMouseDown = function (e) {
    if (e.button === 1 || (e.shiftKey && e.button === 2)) {
      this.handleGridMouseDown(e);
      return;
    }

    var rect = this.velocityCanvas.getBoundingClientRect();
    var mx = e.clientX - rect.left;
    var my = e.clientY - rect.top;
    var beat = this.scrollX + (mx / this.pixelsPerBeat);

    var tab = this.getActiveTab();
    if (!tab || !tab.notes || !tab.notes.length) return;

    // 核心修复：始终从当前轨道所有音符构建全局分组，避免残留的 selectedNotes 拦截目标
    var allGroups = this.getVelocityNoteGroups(tab.notes);
    if (!allGroups.length) return;

    var matchedGroup = this.findVelocityGroupAt(beat, allGroups);
    if (!matchedGroup) return;

    // 判断是否在真正的多选集合中
    var hasMultiSelection = (this.selectedNotes.length > 1);
    var isHitInSelection = hasMultiSelection && matchedGroup.notes.some(function (n) {
      return this.selectedNotes.indexOf(n) !== -1;
    }.bind(this));

    if (!isHitInSelection) {
      // 若不是多选集命中，则精准选中并激活当前点击的音符
      this.selectedNotes = matchedGroup.notes.slice();
    }

    var isRightClick = (e.button === 2);
    var newVel = isRightClick ? 100 : Math.max(1, Math.min(127, Math.round((1 - my / this.velocityHeight) * 127)));

    this.pushHistory();

    if (isHitInSelection) {
      var deltaV = newVel - matchedGroup.notes[0].velocity;
      this.selectedNotes.forEach(function (n) {
        n.velocity = Math.max(1, Math.min(127, (parseInt(n.velocity, 10) || 100) + deltaV));
      });
    } else {
      matchedGroup.notes.forEach(function (n) {
        n.velocity = newVel;
      });
    }

    if (!isRightClick && matchedGroup.notes.length > 0) {
      var topNote = matchedGroup.notes[0];
      this.playNoteSound(Tools.noteNameToNumber(topNote.note), newVel);
    }

    this.showHUD(isRightClick ? "力度重置: 100 (78%)" : ("力度: " + newVel + " (" + Math.round(newVel / 1.27) + "%)"));
    this.render();

    var isStraightLine = (e.altKey || e.shiftKey);
    this.dragState = {
      type: isStraightLine ? "velocity_line" : "velocity_brush",
      startBeat: beat,
      startY: my,
      startVel: newVel,
      curBeat: beat,
      curY: my,
      curVel: newVel,
      lastBeat: beat,
      lastVel: newVel,
      isRightClick: isRightClick,
      isSelectionMode: isHitInSelection,
      initialVelocities: this.selectedNotes.map(function (n) {
        return { note: n, vel: parseInt(n.velocity, 10) || 100 };
      })
    };

    var self = this;
    function onVelMove(me) {
      if (!self.dragState) return;
      var curMx = me.clientX - rect.left;
      var curMy = me.clientY - rect.top;
      var curB = self.scrollX + (curMx / self.pixelsPerBeat);
      var curV = self.dragState.isRightClick ? 100 : Math.max(1, Math.min(127, Math.round((1 - curMy / self.velocityHeight) * 127)));

      self.dragState.curBeat = curB;
      self.dragState.curY = curMy;
      self.dragState.curVel = curV;

      var currentTab = self.getActiveTab();
      if (!currentTab || !currentTab.notes) return;

      if (self.dragState.isSelectionMode) {
        // 多选模式：相对同步调整选区内所有音符
        var delta = curV - self.dragState.startVel;
        self.dragState.initialVelocities.forEach(function (item) {
          item.note.velocity = Math.max(1, Math.min(127, item.vel + delta));
        });
      } else {
        // 全局画笔/区域拖拽模式：精准作用于经过的全部音符
        var curGroups = self.getVelocityNoteGroups(currentTab.notes);
        if (self.dragState.type === "velocity_line") {
          self.applyVelocityStraightLine(self.dragState.startBeat, self.dragState.startVel, curB, curV, curGroups);
        } else {
          self.applyVelocityBrush(self.dragState.lastBeat, self.dragState.lastVel, curB, curV, curGroups);
        }
      }

      self.dragState.lastBeat = curB;
      self.dragState.lastVel = curV;

      self.showHUD(self.dragState.isRightClick ? "力度重置: 100 (78%)" : ("力度: " + curV + " (" + Math.round(curV / 1.27) + "%)"));
      self.scheduleRender();
    }

    function onVelUp() {
      window.removeEventListener("mousemove", onVelMove);
      window.removeEventListener("mouseup", onVelUp);
      self.dragState = null;
      self.stopAllSounds();
      self.render();
    }

    window.addEventListener("mousemove", onVelMove);
    window.addEventListener("mouseup", onVelUp);
  };

  // 自由画笔步进调节：覆盖当前鼠标移动轨迹所经过的所有音符区域
  PianoRoll.prototype.applyVelocityBrush = function (b1, v1, b2, v2, groups) {
    if (!groups || !groups.length) return;

    var minB = Math.min(b1, b2);
    var maxB = Math.max(b1, b2);

    if (Math.abs(b2 - b1) < 0.001) {
      var g = this.findVelocityGroupAt(b2, groups);
      if (g) {
        g.notes.forEach(function (n) { n.velocity = v2; });
      }
      return;
    }

    groups.forEach(function (g) {
      var isInPath = (g.start >= minB && g.start <= maxB);
      var isZoneCovered = (minB < g.zoneEnd && maxB >= g.zoneStart);

      if (isInPath || isZoneCovered) {
        var t = (g.start - b1) / (b2 - b1);
        t = Math.max(0, Math.min(1, t));
        var targetV = Math.round(v1 + (v2 - v1) * t);
        targetV = Math.max(1, Math.min(127, targetV));
        g.notes.forEach(function (n) {
          n.velocity = targetV;
        });
      }
    });
  };

  // 直线插值调节 (Alt / Shift + 拖拽)
  PianoRoll.prototype.applyVelocityStraightLine = function (b1, v1, b2, v2, groups) {
    if (!groups || !groups.length) return;
    var minB = Math.min(b1, b2);
    var maxB = Math.max(b1, b2);

    groups.forEach(function (g) {
      if (g.start >= minB - 0.2 && g.start <= maxB + 0.2) {
        var t = (maxB === minB) ? 0.5 : (g.start - minB) / (maxB - minB);
        var targetV = (b1 <= b2) ? Math.round(v1 + (v2 - v1) * t) : Math.round(v2 + (v1 - v2) * t);
        targetV = Math.max(1, Math.min(127, targetV));
        g.notes.forEach(function (n) {
          n.velocity = targetV;
        });
      }
    });
  };

  PianoRoll.prototype.handleVelocityRightClick = function (e) {
    // 右键已在 handleVelocityMouseDown 统一按 FL Studio 规范处理为即刻重置与涂抹重置
  };

  /* ═══════════ 全套 FL Studio 快捷键矩阵 ═══════════ */

  /* ═══════════ 全套 FL Studio 快捷键矩阵 ═══════════ */

  PianoRoll.prototype.bindGlobalShortcuts = function () {
    var self = this;
    window.addEventListener("keydown", function (e) {
      if (!self.isOpen) return;

      var tag = (e.target && e.target.tagName) || "";
      var isInputActive = tag === "INPUT" || tag === "TEXTAREA" || (e.target && e.target.isContentEditable);
      if (isInputActive) return;

      // 弹窗打开时：全部快捷键挂起（此前仅输入框挂起，单键仍会在弹窗
      // 背后切换工具）；Esc 只关最上层弹窗，不连带关闭整个抽屉
      if (self.anyModalOpen()) {
        if (e.key === "Escape" && !e.ctrlKey && !e.altKey && !e.metaKey && !e.shiftKey) {
          e.preventDefault();
          self.closeTopModal();
        }
        return;
      }

      // 仅当卷帘聚焦时拦截快捷键
      if (self.isFocused) {
        // 1. FL Studio 组合快捷键 (无论是否开启键盘弹奏模式，始终生效)
        // FL Studio 经典重做快捷键: Ctrl+Alt+Z
        if (e.ctrlKey && e.altKey && !e.shiftKey) {
          if (e.code === "KeyZ") {
            e.preventDefault();
            self.redo();
            return;
          }
        }

        // 现代 DAW 重做快捷键: Ctrl+Shift+Z
        if (e.ctrlKey && e.shiftKey && !e.altKey) {
          if (e.code === "KeyZ") {
            e.preventDefault();
            self.redo();
            return;
          }
        }

        if (e.ctrlKey && !e.altKey && !e.shiftKey) {
          if (e.code === "KeyT") {
            e.preventDefault();
            self.toggleTypingKeyboard();
            return;
          }
          if (e.code === "KeyA") {
            e.preventDefault();
            var tab = self.getActiveTab();
            if (tab) { self.selectedNotes = tab.notes.slice(); self.render(); self.showHUD("全选 (Select All)"); }
            return;
          }
          if (e.code === "KeyD") {
            e.preventDefault();
            self.selectedNotes = [];
            self.render();
            self.showHUD("取消选择 (Deselect)");
            return;
          }
          if (e.code === "KeyI") {
            e.preventDefault();
            var tabI = self.getActiveTab();
            if (tabI) {
              self.selectedNotes = tabI.notes.filter(function (n) { return self.selectedNotes.indexOf(n) === -1; });
              self.render();
              self.showHUD("反选 (Invert Selection)");
            }
            return;
          }
          if (e.code === "KeyB") {
            e.preventDefault();
            self.duplicateSelectionRight();
            return;
          }
          if (e.code === "KeyQ") {
            e.preventDefault();
            self.quickQuantize();
            return;
          }
          if (e.code === "KeyL") {
            e.preventDefault();
            self.quickLegato();
            return;
          }
          if (e.code === "ArrowUp") {
            e.preventDefault();
            self.transposeSelection(12);
            return;
          }
          if (e.code === "ArrowDown") {
            e.preventDefault();
            self.transposeSelection(-12);
            return;
          }
          if (e.code === "KeyR") {
            e.preventDefault();
            self.toggleRecord();
            return;
          }
          if (e.code === "KeyZ") { e.preventDefault(); self.undo(); return; }
          if (e.code === "KeyY") { e.preventDefault(); self.redo(); return; }
          if (e.code === "KeyC") { e.preventDefault(); self.copySelection(); return; }
          if (e.code === "KeyV") { e.preventDefault(); self.pasteSelection(); return; }
          if (e.code === "KeyX") { e.preventDefault(); self.cutSelection(); return; }
        }

        // 2. 单键快捷键 (当开启键盘 MIDI 弹奏时挂起失效，关闭时恢复生效)
        if (!e.ctrlKey && !e.altKey && !e.shiftKey && !e.metaKey) {
          if (self.isTypingKeyboard) {
            // 键盘弹奏模式已启动：字母键分配给 MIDI 演奏，单键工具快捷键全部屏蔽
            if (e.code === "Space") { e.preventDefault(); self.togglePlay(); return; }
            if (e.code === "Escape") { e.preventDefault(); self.escapeAction(); return; }
            return;
          }

          var key = e.code.toUpperCase();
          if (key === "KEYP") { e.preventDefault(); self.setTool("draw"); return; }
          if (key === "KEYB") { e.preventDefault(); self.setTool("paint"); return; }
          if (key === "KEYD") { e.preventDefault(); self.setTool("erase"); return; }
          if (key === "KEYC") { e.preventDefault(); self.setTool("slice"); return; }
          if (key === "KEYE") { e.preventDefault(); self.setTool("select"); return; }
          if (key === "KEYT") { e.preventDefault(); self.setTool("mute"); return; }
          if (key === "KEYZ") { e.preventDefault(); self.setTool("zoom"); return; }
          if (key === "SPACE") { e.preventDefault(); self.togglePlay(); return; }
          if (key === "ESCAPE") { e.preventDefault(); self.escapeAction(); return; }
        }

        if (e.altKey && !e.ctrlKey) {
          if (e.code === "KeyX") { e.preventDefault(); self.openLevelScaleModal(); return; }
          if (e.code === "KeyS") { e.preventDefault(); self.openStrumModal(); return; }
          if (e.code === "KeyA") { e.preventDefault(); self.openArpModal(); return; }
          if (e.code === "KeyR") { e.preventDefault(); self.openRandomModal(); return; }
          if (e.code === "KeyY") { e.preventDefault(); self.openFlipModal(); return; }
          if (e.code === "KeyQ") { e.preventDefault(); self.openQuantizeModal(); return; }
          if (e.code === "KeyL") { e.preventDefault(); self.quickLegato(); return; }
        }

        if (e.shiftKey && !e.ctrlKey && !e.altKey) {
          if (e.code === "ArrowUp") { e.preventDefault(); self.transposeSelection(1); return; }
          if (e.code === "ArrowDown") { e.preventDefault(); self.transposeSelection(-1); return; }
        }

        if (e.code === "Delete" || e.code === "Backspace") {
          e.preventDefault();
          self.deleteSelection();
          return;
        }
      }
    });
  };

  PianoRoll.prototype.setTool = function (tool) {
    this.currentTool = tool;
    var toolBtns = document.querySelectorAll(".pr-tool-btn");
    toolBtns.forEach(function (b) {
      b.classList.toggle("active", b.dataset.tool === tool);
    });
    if (this.drawerEl) {
      this.drawerEl.setAttribute("data-tool", tool);
    }
    if (this.gridCanvas) {
      this.gridCanvas.setAttribute("data-tool", tool);
    }
    var toolNames = {
      draw: "画笔 (DRAW)",
      paint: "笔刷 (PAINT)",
      erase: "擦除 (ERASE)",
      slice: "切片 (SLICE)",
      select: "框选 (SELECT)",
      mute: "静音 (MUTE)",
      zoom: "缩放 (ZOOM)"
    };
    this.showHUD("工具: " + (toolNames[tool] || tool.toUpperCase()));
  };

  PianoRoll.prototype.toggleTypingKeyboard = function (forcedVal) {
    if (forcedVal !== undefined) {
      this.isTypingKeyboard = !!forcedVal;
    } else {
      this.isTypingKeyboard = !this.isTypingKeyboard;
    }
    if (window.MidiInputRouter) {
      window.MidiInputRouter.updateSettings({ typingKeyboard: this.isTypingKeyboard });
    }
    var btn = document.getElementById("prTypingMidiBtn");
    if (btn) {
      btn.classList.toggle("active", this.isTypingKeyboard);
    }
    if (this.isTypingKeyboard) {
      this.showHUD("🎹 键盘弹奏: 已开启 (单键快捷键已挂起)");
      if (window.UI && window.UI.toast) window.UI.toast("🎹 键盘弹奏已开启 (单键快捷键已挂起，按 Ctrl+T 可切换)", "ok");
    } else {
      this.showHUD("🎹 键盘弹奏: 已关闭 (单键快捷键已恢复)");
      if (window.UI && window.UI.toast) window.UI.toast("🎹 键盘弹奏已关闭 (单键快捷键已恢复)", "ok");
    }
  };

  PianoRoll.prototype.duplicateSelectionRight = function () {
    var tab = this.getActiveTab();
    if (!tab || !tab.notes.length) return;
    this.pushHistory();

    var targets = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    var minStart = Math.min.apply(null, targets.map(function (n) { return n.start; }));
    var maxEnd = Math.max.apply(null, targets.map(function (n) { return n.end; }));
    var spanBeats = Math.max(4, Math.ceil((maxEnd - minStart) / 4) * 4);

    var duplicated = targets.map(function (n) {
      return {
        note: n.note,
        velocity: n.velocity,
        start: Math.round((n.start + spanBeats) * 1000) / 1000,
        end: Math.round((n.end + spanBeats) * 1000) / 1000
      };
    });

    tab.notes = tab.notes.concat(duplicated);
    this.selectedNotes = duplicated;
    this.render();
    this.showHUD("顺延复制 (Ctrl+B)");
  };

  PianoRoll.prototype.quickQuantize = function () {
    var tab = this.getActiveTab();
    if (!tab) return;
    this.pushHistory();
    var targets = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    var res = Tools.quantize(targets, this.snapGrid, true);
    if (this.selectedNotes.length) {
      targets.forEach(function (n, idx) { Object.assign(n, res[idx]); });
    } else {
      tab.notes = res;
    }
    this.render();
    this.showHUD("量化完成 (Ctrl+Q)");
  };

  PianoRoll.prototype.quickLegato = function () {
    var tab = this.getActiveTab();
    if (!tab) return;
    this.pushHistory();
    var targets = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    Tools.legato(targets);
    this.render();
    this.showHUD("快速连奏 (Legato)");
  };

  PianoRoll.prototype.transposeSelection = function (semitones) {
    var tab = this.getActiveTab();
    if (!tab) return;
    this.pushHistory();
    var targets = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    Tools.transpose(targets, semitones);
    this.render();
    this.showHUD("移调: " + (semitones > 0 ? "+" : "") + semitones + " 半音");
  };

  PianoRoll.prototype.deleteSelection = function () {
    var tab = this.getActiveTab();
    if (!tab || !this.selectedNotes.length) return;
    this.pushHistory();
    var self = this;
    this.selectedNotes.forEach(function (n) { self.addDeleteEffect(n); });
    tab.notes = tab.notes.filter(function (n) {
      return self.selectedNotes.indexOf(n) === -1;
    });
    this.selectedNotes = [];
    this.render();
    this.showHUD("已删除选中音符");
  };

  PianoRoll.prototype.copySelection = function () {
    var targets = this.selectedNotes.length ? this.selectedNotes : [];
    if (!targets.length) return;
    this.clipboard = JSON.parse(JSON.stringify(targets));
    this.showHUD("已复制 " + targets.length + " 个音符");
  };

  PianoRoll.prototype.pasteSelection = function () {
    var tab = this.getActiveTab();
    if (!tab || !this.clipboard.length) return;
    this.pushHistory();

    var minStart = Math.min.apply(null, this.clipboard.map(function (n) { return n.start; }));
    var pasteStart = this.playheadBeat;

    var pasted = this.clipboard.map(function (n) {
      var dur = n.end - n.start;
      var st = Math.round((pasteStart + (n.start - minStart)) * 1000) / 1000;
      return {
        note: n.note,
        velocity: n.velocity,
        start: st,
        end: Math.round((st + dur) * 1000) / 1000
      };
    });

    tab.notes = tab.notes.concat(pasted);
    this.selectedNotes = pasted;
    this.render();
    this.showHUD("已粘贴 " + pasted.length + " 个音符");
  };

  PianoRoll.prototype.cutSelection = function () {
    this.copySelection();
    this.deleteSelection();
  };

  /* ═══════════ 弹窗与高级工具管理 (LevelScale / Strum / NoteProps) ═══════════ */

  /* Esc 分层：有选中音符先取消选择，无选择才关闭抽屉——编辑中按 Esc
     的反射不再直接收起整个工作区（多层防误触：弹窗 > 选择 > 关闭） */
  PianoRoll.prototype.escapeAction = function () {
    /* Esc 分层：弹窗（keydown 顶部 anyModalOpen 拦截）→ 下拉菜单 →
       清选区 → 关抽屉。下拉此前只能点外部关闭，与弹层 Esc 分层惯例不一致 */
    var openDD = document.querySelector(".pr-dropdown.open");
    if (openDD) { this.closeAllDropdowns(); return; }
    if (this.selectedNotes && this.selectedNotes.length) {
      this.selectedNotes = [];
      this.render();
      this.showHUD("取消选择");
      return;
    }
    this.close();
  };

  PianoRoll.prototype.openModalAnimated = function (modalEl) {
    if (!modalEl) return;
    modalEl.hidden = false;
    modalEl.classList.remove("modal-in", "modal-out");
    void modalEl.offsetWidth;
    modalEl.classList.add("modal-in");
  };

  PianoRoll.prototype.closeModalAnimated = function (modalEl) {
    if (!modalEl) return;
    modalEl.classList.remove("modal-in");
    modalEl.classList.add("modal-out");
    setTimeout(function () {
      modalEl.hidden = true;
      modalEl.classList.remove("modal-out");
    }, 220);
  };

  /* 本窗口管理的全部模态弹窗（Esc 分层关闭 / 弹窗开启时挂起快捷键） */
  PianoRoll.prototype.prModalIds = [
    "notePropModal", "levelScaleModal", "strumModal",
    "prShortcutsModal", "soundLibModalOverlay"
  ];

  PianoRoll.prototype.closeTopModal = function () {
    for (var i = this.prModalIds.length - 1; i >= 0; i--) {
      var m = document.getElementById(this.prModalIds[i]);
      if (m && !m.hidden) {
        this.closeModalAnimated(m);
        return true;
      }
    }
    return false;
  };

  PianoRoll.prototype.anyModalOpen = function () {
    for (var i = 0; i < this.prModalIds.length; i++) {
      var m = document.getElementById(this.prModalIds[i]);
      if (m && !m.hidden) return true;
    }
    return false;
  };

  PianoRoll.prototype.bindToolModals = function () {
    var self = this;

    // 音符属性弹窗
    var npOk = document.getElementById("notePropOk");
    var npCancel = document.getElementById("notePropCancel");
    var npModal = document.getElementById("notePropModal");
    if (npOk) {
      npOk.addEventListener("click", function () {
        if (self.editingNote) {
          self.pushHistory();
          var pName = document.getElementById("notePropPitch").value;
          var vel = parseInt(document.getElementById("notePropVel").value, 10) || 100;
          var st = parseFloat(document.getElementById("notePropStart").value) || 0;
          var dur = parseFloat(document.getElementById("notePropDur").value) || 1;

          self.editingNote.note = pName;
          self.editingNote.velocity = Math.max(1, Math.min(127, vel));
          self.editingNote.start = Math.max(0, st);
          self.editingNote.end = Math.max(st + 0.1, st + dur);
          self.render();
        }
        if (npModal) self.closeModalAnimated(npModal);
      });
    }
    if (npCancel && npModal) {
      npCancel.addEventListener("click", function () { self.closeModalAnimated(npModal); });
    }

    // 力度缩放弹窗 (Alt+X)
    var lsOk = document.getElementById("levelScaleOk");
    var lsCancel = document.getElementById("levelScaleCancel");
    var lsModal = document.getElementById("levelScaleModal");
    if (lsOk) {
      lsOk.addEventListener("click", function () {
        var mult = parseFloat(document.getElementById("lsMultiply").value) || 1.0;
        var off = parseInt(document.getElementById("lsOffset").value, 10) || 0;
        var rStart = parseInt(document.getElementById("lsRampStart").value, 10) || 0;
        var rEnd = parseInt(document.getElementById("lsRampEnd").value, 10) || 0;

        var tab = self.getActiveTab();
        if (tab) {
          self.pushHistory();
          var targets = self.selectedNotes.length ? self.selectedNotes : tab.notes;
          var res = Tools.levelScale(targets, mult, off, rStart, rEnd);
          if (self.selectedNotes.length) {
            targets.forEach(function (n, idx) { Object.assign(n, res[idx]); });
          } else {
            tab.notes = res;
          }
          self.render();
        }
        if (lsModal) self.closeModalAnimated(lsModal);
      });
    }
    if (lsCancel && lsModal) {
      lsCancel.addEventListener("click", function () { self.closeModalAnimated(lsModal); });
    }

    // 扫弦弹窗 (Alt+S)
    var stOk = document.getElementById("strumOk");
    var stCancel = document.getElementById("strumCancel");
    var stModal = document.getElementById("strumModal");
    if (stOk) {
      stOk.addEventListener("click", function () {
        var timeOffset = (parseInt(document.getElementById("strumTime").value, 10) || 20) / 480;
        var velRamp = parseInt(document.getElementById("strumVelRamp").value, 10) || -8;
        var altDir = document.getElementById("strumAltDir").checked;

        var tab = self.getActiveTab();
        if (tab) {
          self.pushHistory();
          var targets = self.selectedNotes.length ? self.selectedNotes : tab.notes;
          var res = Tools.strum(targets, timeOffset, velRamp, altDir);
          if (!self.selectedNotes.length) tab.notes = res;
          self.render();
        }
        if (stModal) self.closeModalAnimated(stModal);
      });
    }
    if (stCancel && stModal) {
      stCancel.addEventListener("click", function () { self.closeModalAnimated(stModal); });
    }

    // 快捷键帮助弹窗
    var helpBtn = document.getElementById("prShortcutsHelpBtn");
    var helpModal = document.getElementById("prShortcutsModal");
    var helpClose = document.getElementById("prShortcutsClose");
    if (helpBtn && helpModal) {
      helpBtn.addEventListener("click", function () { self.openModalAnimated(helpModal); });
    }
    if (helpClose && helpModal) {
      helpClose.addEventListener("click", function () { self.closeModalAnimated(helpModal); });
    }

    var soundLibClose = document.getElementById("soundLibCloseBtn");
    var soundLibModal = document.getElementById("soundLibModalOverlay");
    if (soundLibClose && soundLibModal) {
      soundLibClose.addEventListener("click", function () { self.closeModalAnimated(soundLibModal); });
    }
  };

  PianoRoll.prototype.openNotePropertiesModal = function (note) {
    this.editingNote = note;
    var modal = document.getElementById("notePropModal");
    if (!modal) return;

    var pitchInput = document.getElementById("notePropPitch");
    var velInput = document.getElementById("notePropVel");
    var startInput = document.getElementById("notePropStart");
    var durInput = document.getElementById("notePropDur");

    if (pitchInput) pitchInput.value = note.note;
    if (velInput) velInput.value = note.velocity;
    if (startInput) startInput.value = note.start;
    if (durInput) durInput.value = Math.round((note.end - note.start) * 1000) / 1000;

    this.openModalAnimated(modal);
  };

  PianoRoll.prototype.openLevelScaleModal = function () {
    var modal = document.getElementById("levelScaleModal");
    if (modal) this.openModalAnimated(modal);
  };

  PianoRoll.prototype.openStrumModal = function () {
    var modal = document.getElementById("strumModal");
    if (modal) this.openModalAnimated(modal);
  };

  PianoRoll.prototype.openArpModal = function () {
    var tab = this.getActiveTab();
    if (!tab) return;
    this.pushHistory();
    var targets = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    tab.notes = Tools.arpeggiate(targets, "up", this.snapGrid);
    this.selectedNotes = [];
    this.render();
    this.showHUD("琶音已生成 (Alt+A)");
  };

  PianoRoll.prototype.openRandomModal = function () {
    var tab = this.getActiveTab();
    if (!tab) return;
    this.pushHistory();
    var targets = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    tab.notes = Tools.randomize(targets, 15, 0, 0);
    this.render();
    this.showHUD("力度已随机化 (Alt+R)");
  };

  PianoRoll.prototype.openFlipModal = function () {
    var tab = this.getActiveTab();
    if (!tab) return;
    this.pushHistory();
    var targets = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    var res = Tools.flip(targets, "vertical");
    if (this.selectedNotes.length) {
      targets.forEach(function (n, idx) { Object.assign(n, res[idx]); });
    } else {
      tab.notes = res;
    }
    this.render();
    this.showHUD("旋律已翻转 (Alt+Y)");
  };

  /* ═══════════ 高保真自定义下拉组件管理 (对齐设置页模型下拉框与 Q 弹动画) ═══════════ */

  PianoRoll.prototype.setupCustomDropdown = function (wrapId, btnId, menuId, options, initialValue, onSelect) {
    var wrap = document.getElementById(wrapId);
    var btn = document.getElementById(btnId);
    var menu = document.getElementById(menuId);
    if (!wrap || !btn || !menu) return null;

    var self = this;
    var currentValue = initialValue;

    function renderOptions(opts, currentVal) {
      menu.innerHTML = "";
      opts.forEach(function (opt) {
        var optDiv = document.createElement("div");
        optDiv.className = "select-option" + (opt.value === currentVal ? " selected" : "");
        optDiv.textContent = opt.label;
        optDiv.dataset.value = opt.value;
        optDiv.addEventListener("click", function (e) {
          e.stopPropagation();
          currentValue = opt.value;
          var labelSpan = btn.querySelector(".pr-dropdown-label");
          if (labelSpan) labelSpan.textContent = opt.label;
          closeMenu();
          if (onSelect) onSelect(opt.value, opt.label);
        });
        menu.appendChild(optDiv);
      });
    }

    function openMenu() {
      self.closeAllDropdowns();
      wrap.classList.add("open");
      menu.hidden = false;
      menu.classList.remove("menu-in");
      void menu.offsetWidth;
      menu.classList.add("menu-in");
    }

    function closeMenu() {
      wrap.classList.remove("open");
      menu.hidden = true;
      menu.classList.remove("menu-in");
    }

    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      if (wrap.classList.contains("open")) {
        closeMenu();
      } else {
        openMenu();
      }
    });

    renderOptions(options, initialValue);

    return {
      setValue: function (val, label) {
        currentValue = val;
        var labelSpan = btn.querySelector(".pr-dropdown-label");
        if (labelSpan && label) labelSpan.textContent = label;
        renderOptions(options, val);
      },
      updateOptions: function (newOpts, val) {
        options = newOpts;
        if (val !== undefined) currentValue = val;
        renderOptions(newOpts, currentValue);
        var cur = newOpts.find(function (o) { return o.value === currentValue; });
        var labelSpan = btn.querySelector(".pr-dropdown-label");
        if (labelSpan && cur) labelSpan.textContent = cur.label;
      },
      close: closeMenu
    };
  };

  PianoRoll.prototype.closeAllDropdowns = function () {
    document.querySelectorAll(".pr-dropdown.open").forEach(function (wrap) {
      wrap.classList.remove("open");
      var m = wrap.querySelector(".select-menu");
      if (m) { m.hidden = true; m.classList.remove("menu-in"); }
    });
  };

  /* ═══════════ 顶部控制栏绑定 ═══════════ */

  PianoRoll.prototype.bindHeaderControls = function () {
    var self = this;

    // 全局点击自动收起下拉菜单与录制右键菜单
    document.addEventListener("click", function () {
      self.closeAllDropdowns();
      var recMenu = document.getElementById("prRecordMenu");
      if (recMenu) recMenu.hidden = true;
    });

    var toolBtns = document.querySelectorAll(".pr-tool-btn");
    toolBtns.forEach(function (btn) {
      btn.addEventListener("click", function () {
        self.setTool(btn.dataset.tool);
      });
    });

    // 录制按钮 (左键启闭，右键打开配置菜单)
    var recBtn = document.getElementById("prRecordBtn");
    var recMenu = document.getElementById("prRecordMenu");
    if (recBtn) {
      recBtn.addEventListener("click", function () {
        self.toggleRecord();
      });
      recBtn.addEventListener("contextmenu", function (e) {
        e.preventDefault();
        e.stopPropagation();
        if (recMenu) {
          recMenu.hidden = !recMenu.hidden;
        }
      });
    }

    if (recMenu) {
      recMenu.addEventListener("click", function (e) { e.stopPropagation(); });

      recMenu.querySelectorAll("[data-countin]").forEach(function (opt) {
        opt.addEventListener("click", function (e) {
          e.stopPropagation();
          var c = parseInt(opt.getAttribute("data-countin"), 10) || 0;
          self.recordConfig.countIn = c;
          recMenu.querySelectorAll("[data-countin]").forEach(function (o) { o.classList.remove("active"); });
          opt.classList.add("active");
          self.showHUD("预备拍: " + (c ? c + " 拍" : "关闭"));
        });
      });

      recMenu.querySelectorAll("[data-recmode]").forEach(function (opt) {
        opt.addEventListener("click", function (e) {
          e.stopPropagation();
          var m = opt.getAttribute("data-recmode");
          self.recordConfig.replaceMode = (m === "replace");
          recMenu.querySelectorAll("[data-recmode]").forEach(function (o) { o.classList.remove("active"); });
          opt.classList.add("active");
          self.showHUD("录制模式: " + (m === "replace" ? "覆盖已有音符 (Replace)" : "叠加混录 (Overdub)"));
        });
      });

      var recQToggle = document.getElementById("recQuantizeToggle");
      if (recQToggle) {
        recQToggle.addEventListener("change", function (e) {
          self.recordConfig.quantizeOnRecord = e.target.checked;
          self.showHUD("实时吸附量化: " + (e.target.checked ? "开启" : "关闭"));
        });
      }

      var recMToggle = document.getElementById("recMetroToggle");
      if (recMToggle) {
        recMToggle.addEventListener("change", function (e) {
          self.recordConfig.metronomeOnRecord = e.target.checked;
          self.showHUD("录制自动节拍器: " + (e.target.checked ? "开启" : "关闭"));
        });
      }
    }

    var playBtn = document.getElementById("prPlayBtn");
    if (playBtn) playBtn.addEventListener("click", function () { self.togglePlay(); });

    var stopBtn = document.getElementById("prStopBtn");
    if (stopBtn) stopBtn.addEventListener("click", function () {
      if (self.isRecording) self.stopRecording();
      self.stopPlayback();
      self.playheadBeat = self.loopStart !== undefined ? self.loopStart : 0;
      self.render();
    });

    var loopToggleBtn = document.getElementById("prLoopToggleBtn");
    if (loopToggleBtn) {
      loopToggleBtn.classList.toggle("active", self.isLooping);
      loopToggleBtn.addEventListener("click", function () { self.toggleLoop(); });
    }

    var metroBtn = document.getElementById("prMetroBtn");
    if (metroBtn) metroBtn.addEventListener("click", function () {
      self.isMetronome = !self.isMetronome;
      metroBtn.classList.toggle("active", self.isMetronome);
    });

    var bpmInput = document.getElementById("prBpmInput");
    if (bpmInput) bpmInput.addEventListener("change", function () {
      self.bpm = Math.max(20, Math.min(400, parseInt(this.value, 10) || 120));
      var tab = self.getActiveTab();
      if (tab) tab.bpm = self.bpm;
    });

    // 1. 网格吸附自定义下拉菜单 (对齐模型列表 Q 弹动画)
    this.snapDropdown = this.setupCustomDropdown(
      "prSnapDropdown", "prSnapBtn", "prSnapMenu",
      [
        { value: "1", label: "1 拍" },
        { value: "0.5", label: "1/2 拍" },
        { value: "0.25", label: "1/4 拍 (1/16音符)" },
        { value: "0.125", label: "1/8 拍 (1/32音符)" },
        { value: "0.333333", label: "1/3 拍 (三连音)" },
        { value: "0.166666", label: "1/6 拍 (六连音)" }
      ],
      "0.25",
      function (val, label) {
        self.snapGrid = parseFloat(val) || 0.25;
        self.render();
        self.showHUD("吸附: " + label);
      }
    );

    // 2. 音阶高亮自定义下拉菜单
    this.scaleDropdown = this.setupCustomDropdown(
      "prScaleDropdown", "prScaleBtn", "prScaleMenu",
      [
        { value: "none", label: "音阶高亮: 无" },
        { value: "major", label: "自然大调" },
        { value: "minor", label: "自然小调" },
        { value: "harmonic_minor", label: "和声小调" },
        { value: "melodic_minor", label: "旋律小调" },
        { value: "dorian", label: "多利亚 (Dorian)" },
        { value: "pentatonic_major", label: "五声大调" },
        { value: "pentatonic_minor", label: "五声小调" },
        { value: "blues", label: "布鲁斯 (Blues)" }
      ],
      "none",
      function (val, label) {
        self.selectedScale = val;
        self.render();
        self.showHUD("高亮: " + label);
      }
    );

    // 3. 和弦印章自定义下拉菜单
    this.chordDropdown = this.setupCustomDropdown(
      "prChordDropdown", "prChordBtn", "prChordMenu",
      [
        { value: "none", label: "和弦印章: 单音" },
        { value: "maj", label: "大三和弦 (Major)" },
        { value: "min", label: "小三和弦 (Minor)" },
        { value: "maj7", label: "Maj7 七和弦" },
        { value: "min7", label: "Min7 七和弦" },
        { value: "dom7", label: "属七和弦 (7th)" },
        { value: "sus4", label: "挂四和弦 (Sus4)" },
        { value: "add9", label: "加九和弦 (Add9)" }
      ],
      "none",
      function (val, label) {
        self.chordStamp = val;
        self.showHUD("和弦: " + label);
      }
    );

    // 4. 幽灵参考轨自定义下拉菜单
    this.ghostDropdown = this.setupCustomDropdown(
      "prGhostDropdown", "prGhostBtn", "prGhostMenu",
      [{ value: "", label: "幽灵参考轨: 无" }],
      "",
      function (val, label) {
        self.ghostTrackId = val || null;
        self.render();
        self.showHUD("参考轨: " + label);
      }
    );

    // 5. 音源选择自定义下拉菜单
    this.soundDropdown = this.setupCustomDropdown(
      "prSoundDropdown", "prSoundBtn", "prSoundMenu",
      [
        { value: "synth_sawtooth", label: "合成器: 锯齿波 (Sawtooth)" },
        { value: "synth_square", label: "合成器: 方波 (Square)" },
        { value: "synth_triangle", label: "合成器: 三角波 (Triangle)" },
        { value: "synth_sine", label: "合成器: 正弦波 (Sine)" },
        { value: "sf2_piano", label: "SF2: 温暖钢琴 (Piano)" },
        { value: "sf2_strings", label: "SF2: 弦乐群 (Strings)" }
      ],
      "synth_sawtooth",
      function (val, label) {
        self.soundSource = val;
        self.showHUD("音源: " + label);
      }
    );

    var typingMidiBtn = document.getElementById("prTypingMidiBtn");
    if (typingMidiBtn) {
      typingMidiBtn.addEventListener("click", function () {
        self.toggleTypingKeyboard();
      });
    }

    var quantizeBtn = document.getElementById("prQuantizeBtn");
    if (quantizeBtn) quantizeBtn.addEventListener("click", function () { self.quickQuantize(); });

    var strumBtn = document.getElementById("prStrumBtn");
    if (strumBtn) strumBtn.addEventListener("click", function () { self.openStrumModal(); });

    var arpBtn = document.getElementById("prArpBtn");
    if (arpBtn) arpBtn.addEventListener("click", function () { self.openArpModal(); });

    var sendChatBtn = document.getElementById("prSendToChatBtn");
    if (sendChatBtn) sendChatBtn.addEventListener("click", function () { self.sendToChat(); });

    var soundLibBtn = document.getElementById("prSoundLibBtn");
    if (soundLibBtn) soundLibBtn.addEventListener("click", function () { self.openSoundLibraryModal(); });

    var maxBtn = document.getElementById("prMaxBtn");
    if (maxBtn) maxBtn.addEventListener("click", function () { self.toggleMaximize(); });

    var closeBtn = document.getElementById("prCloseBtn");
    if (closeBtn) closeBtn.addEventListener("click", function () { self.close(); });
  };

  PianoRoll.prototype.bindMidiRouter = function () {
    var self = this;
    if (!window.MidiInputRouter) return;
    window.MidiInputRouter.addListener(function (type, note, velocity) {
      if (type === "noteon") {
        self.handleLiveNoteOn(note, velocity);
        self.playNoteSound(note, velocity);
      } else if (type === "noteoff") {
        self.handleLiveNoteOff(note);
        self.stopNoteSound(note);
      }
      self.renderKeys();
    });
  };

  PianoRoll.prototype.sendToChat = function () {
    var tab = this.getActiveTab();
    if (!tab) return;
    var targetNotes = this.selectedNotes.length ? this.selectedNotes : tab.notes;
    if (!targetNotes.length) {
      if (window.UI && window.UI.toast) window.UI.toast("卷帘中没有音符可发送", "warn");
      return;
    }
    var noteLines = targetNotes.map(function (n) {
      return '[note: "' + n.note + '", velocity: "' + n.velocity + '", start: "' + n.start + '", end: "' + n.end + '"]';
    }).join("\n");

    var input = document.getElementById("msgInput");
    if (input) {
      var prompt = "请针对以下音符（来自 " + tab.name + "）进行配和弦与对位编排：\n" + noteLines;
      input.value = prompt;
      input.focus();
      if (window.UI && window.UI.toast) window.UI.toast("✓ 已将音符注入对话输入框", "ok");
    }
  };

  PianoRoll.prototype.openSoundLibraryModal = function () {
    var modal = document.getElementById("soundLibModalOverlay");
    if (!modal) return;
    this.openModalAnimated(modal);
    this.refreshSoundLibraryList();
  };

  PianoRoll.prototype.refreshSoundLibraryList = function () {
    var listEl = document.getElementById("soundLibList");
    if (!listEl || !window.SoundLibrary) return;
    listEl.innerHTML = '<div style="padding:14px;font-size:12px;color:var(--color-ink-faint);text-align:center">正在加载音源列表…</div>';

    var self = this;
    window.SoundLibrary.listSoundFonts().then(function (fonts) {
      listEl.innerHTML = "";
      if (!fonts.length) {
        listEl.innerHTML = '<div style="padding:14px;font-size:12px;color:var(--color-ink-faint);text-align:center">暂无自定义音色库，可点击下方上传 .sf2 文件</div>';
        return;
      }
      fonts.forEach(function (f) {
        var row = document.createElement("div");
        row.className = "sound-lib-row";
        row.innerHTML =
          '<div style="flex:1"><strong>' + UI.esc(f.name) + '</strong> <span class="dim">(' + UI.fmtSize(f.size) + ')</span></div>' +
          '<button class="btn btn-secondary btn-sm action-load">加载使用</button>' +
          '<button class="btn btn-danger btn-sm action-del">删除</button>';

        row.querySelector(".action-load").addEventListener("click", function () {
          window.SoundLibrary.getSoundFont(f.id).then(function (rec) {
            if (rec && rec.data) {
              var parsed = self.soundfont.parseSF2(rec.data);
              self.updateSoundSelectOptions(parsed);
              if (window.UI && window.UI.toast) window.UI.toast("✓ 已加载音色库: " + f.name, "ok");
              var modal = document.getElementById("soundLibModalOverlay");
              if (modal) self.closeModalAnimated(modal);
            } else {
              if (window.UI && window.UI.toast) window.UI.toast("✗ 音色数据读取失败，请重新上传", "err");
            }
          }).catch(function (err) {
            /* 此前无 catch：加载失败时弹窗永远停在加载态且无提示 */
            if (window.UI && window.UI.toast) window.UI.toast("✗ 加载音色失败: " + (err && err.message ? err.message : "未知错误"), "err");
          });
        });

        row.querySelector(".action-del").addEventListener("click", function () {
          if (!window.confirm("确定删除音色库「" + f.name + "」？此操作不可恢复。")) return;
          window.SoundLibrary.deleteSoundFont(f.id, f.name).then(function () {
            self.refreshSoundLibraryList();
            if (window.UI && window.UI.toast) window.UI.toast("✓ 已删除音色库: " + f.name, "ok");
          }).catch(function (err) {
            if (window.UI && window.UI.toast) window.UI.toast("✗ 删除失败: " + (err && err.message ? err.message : "未知错误"), "err");
          });
        });

        listEl.appendChild(row);
      });
    }).catch(function (err) {
      /* IndexedDB 打不开等场景：此前列表永远停在"正在加载…"且无报错 */
      listEl.innerHTML = '<div style="padding:14px;font-size:12px;color:var(--color-danger);text-align:center">音源列表加载失败: ' + UI.esc(err && err.message ? err.message : String(err)) + '</div>';
    });
  };

  PianoRoll.prototype.updateSoundSelectOptions = function (parsed) {
    if (!parsed || !parsed.presets) return;
    var self = this;
    var baseOpts = [
      { value: "synth_sawtooth", label: "合成器: 锯齿波 (Sawtooth)" },
      { value: "synth_square", label: "合成器: 方波 (Square)" },
      { value: "synth_triangle", label: "合成器: 三角波 (Triangle)" },
      { value: "synth_sine", label: "合成器: 正弦波 (Sine)" },
      { value: "sf2_piano", label: "SF2: 温暖钢琴 (Piano)" },
      { value: "sf2_strings", label: "SF2: 弦乐群 (Strings)" }
    ];
    parsed.presets.forEach(function (p) {
      baseOpts.push({ value: p.id, label: "SF2: " + (p.name || parsed.name) });
    });
    if (!parsed.presets.length) return;   /* 空预设列表：保持当前音源，避免取 [0] 崩溃 */
    if (this.soundDropdown) {
      this.soundDropdown.updateOptions(baseOpts, parsed.presets[0].id);
    }
    this.soundSource = parsed.presets[0].id;
  };

  window.PianoRoll = new PianoRoll();
  document.addEventListener("DOMContentLoaded", function () {
    window.PianoRoll.init();
  });
  /* 应用退出时兜底落盘防抖窗口内的编辑（sendBeacon 不阻塞卸载） */
  window.addEventListener("beforeunload", function () {
    window.PianoRoll.flushAllOnUnload();
  });
})(window);
