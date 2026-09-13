/* Web MIDI 硬件设备监听与电脑键盘弹奏映射路由器 */
(function (window) {
  "use strict";

  var MIDI_STORAGE_KEY = "ai-midi-hardware-settings";

  function MidiInputRouter() {
    this.midiAccess = null;
    this.activeInput = null;
    this.baseOctave = 4; // C4 (MIDI 60)
    this.pressedKeys = {}; // keycode -> midiNote
    this.heldNotes = {}; // midiNote -> bool
    this.listeners = []; // callback functions: fn(type, note, velocity)
    this.settings = this.loadSettings();

    this.bindTypingKeyboard();
    this.initWebMIDI();
  }

  MidiInputRouter.prototype.loadSettings = function () {
    try {
      var raw = localStorage.getItem(MIDI_STORAGE_KEY);
      return raw ? JSON.parse(raw) : {
        enabled: true,
        deviceId: "auto",
        channel: "all",
        velocityCurve: "linear",
        typingKeyboard: false
      };
    } catch (e) {
      return { enabled: true, deviceId: "auto", channel: "all", velocityCurve: "linear", typingKeyboard: false };
    }
  };

  MidiInputRouter.prototype.updateSettings = function (newSettings) {
    this.settings = Object.assign({}, this.settings, newSettings);
    try {
      localStorage.setItem(MIDI_STORAGE_KEY, JSON.stringify(this.settings));
    } catch (e) {}
    if (this.midiAccess) {
      this.bindDevice(this.settings.deviceId);
    }
  };

  MidiInputRouter.prototype.addListener = function (fn) {
    if (this.listeners.indexOf(fn) === -1) {
      this.listeners.push(fn);
    }
  };

  MidiInputRouter.prototype.removeListener = function (fn) {
    var idx = this.listeners.indexOf(fn);
    if (idx !== -1) this.listeners.splice(idx, 1);
  };

  MidiInputRouter.prototype.emit = function (type, note, velocity) {
    for (var i = 0; i < this.listeners.length; i++) {
      try {
        this.listeners[i](type, note, velocity);
      } catch (e) {
        console.error("MIDI listener error:", e);
      }
    }
  };

  MidiInputRouter.prototype.applyVelocityCurve = function (rawVel) {
    var curve = this.settings.velocityCurve || "linear";
    if (curve === "fixed_100") return 100;
    if (curve === "fixed_127") return 127;
    var norm = rawVel / 127.0;
    if (curve === "compressed") {
      return Math.max(1, Math.min(127, Math.round(Math.pow(norm, 0.65) * 127)));
    }
    if (curve === "expanded") {
      return Math.max(1, Math.min(127, Math.round(Math.pow(norm, 1.45) * 127)));
    }
    return rawVel;
  };

  MidiInputRouter.prototype.initWebMIDI = function () {
    var self = this;
    if (!navigator.requestMIDIAccess) return;
    navigator.requestMIDIAccess({ sysex: false }).then(function (access) {
      self.midiAccess = access;
      self.bindDevice(self.settings.deviceId);
      access.onstatechange = function () {
        self.bindDevice(self.settings.deviceId);
      };
    }).catch(function (err) {
      console.warn("Web MIDI access denied/failed:", err);
    });
  };

  MidiInputRouter.prototype.bindDevice = function (deviceId) {
    var self = this;
    if (!this.activeInputs) this.activeInputs = [];

    // 清除旧监听
    this.activeInputs.forEach(function (inp) {
      try { inp.onmidimessage = null; } catch (e) {}
    });
    this.activeInputs = [];

    if (!this.midiAccess || this.settings.enabled === false) return;

    var inputs = Array.from(this.midiAccess.inputs.values());
    if (!inputs.length) return;

    if (deviceId === "auto" || !deviceId) {
      // auto 模式下挂载所有可用 MIDI 输入端口，确保无论接在哪个 USB/MIDI 口均能 100% 收到信号
      inputs.forEach(function (inp) {
        inp.onmidimessage = function (e) {
          self.handleMidiMessage(e);
        };
        self.activeInputs.push(inp);
      });
    } else {
      var target = this.midiAccess.inputs.get(deviceId) || null;
      if (target) {
        target.onmidimessage = function (e) {
          self.handleMidiMessage(e);
        };
        self.activeInputs.push(target);
      } else {
        // 如果指定设备未找到，回退绑定所有可用输入
        inputs.forEach(function (inp) {
          inp.onmidimessage = function (e) {
            self.handleMidiMessage(e);
          };
          self.activeInputs.push(inp);
        });
      }
    }
  };

  MidiInputRouter.prototype.handleMidiMessage = function (e) {
    var data = e.data;
    if (!data || data.length < 2) return;
    var status = data[0] & 0xf0;
    var channel = (data[0] & 0x0f) + 1;
    var cfgChannel = this.settings.channel || "all";
    if (cfgChannel !== "all" && parseInt(cfgChannel, 10) !== channel) return;

    var note = data[1];
    var rawVel = data.length > 2 ? data[2] : 0;

    if (status === 0x90 && rawVel > 0) {
      var vel = this.applyVelocityCurve(rawVel);
      this.heldNotes[note] = true;
      this.emit("noteon", note, vel);
    } else if (status === 0x80 || (status === 0x90 && rawVel === 0)) {
      delete this.heldNotes[note];
      this.emit("noteoff", note, 0);
    }
  };

  /* ═══════════ 电脑键盘弹奏 (FL Studio 经典按键映射) ═══════════ */

  MidiInputRouter.prototype.bindTypingKeyboard = function () {
    var self = this;
    // 基础键位与半音偏移映射
    var KEY_MAP_LOW = {
      "KeyZ": 0,  "KeyS": 1,  "KeyX": 2,  "KeyD": 3,  "KeyC": 4,
      "KeyV": 5,  "KeyG": 6,  "KeyB": 7,  "KeyH": 8,  "KeyN": 9,
      "KeyJ": 10, "KeyM": 11, "Comma": 12, "KeyL": 13, "Period": 14,
      "Semicolon": 15, "Slash": 16
    };
    var KEY_MAP_HIGH = {
      "KeyQ": 12, "Digit2": 13, "KeyW": 14, "Digit3": 15, "KeyE": 16,
      "KeyR": 17, "Digit5": 18, "KeyT": 19, "Digit6": 20, "KeyY": 21,
      "Digit7": 22, "KeyU": 23, "KeyI": 24, "Digit9": 25, "KeyO": 26,
      "Digit0": 27, "KeyP": 28
    };

    window.addEventListener("keydown", function (e) {
      if (self.settings.typingKeyboard === false) return;
      /* 幽灵触发守卫：钢琴卷帘未打开时按键不发声（此前监听挂在 window
         且只挡输入框——关掉抽屉后在档案库/聊天区按字母键也会弹奏，
         若正录制还会把按键写进音符数据） */
      if (!window.PianoRoll || !window.PianoRoll.isOpen) return;
      var tag = (e.target && e.target.tagName) || "";
      if (tag === "INPUT" || tag === "TEXTAREA" || (e.target && e.target.isContentEditable)) return;
      if (e.ctrlKey || e.metaKey || e.altKey) return;

      // 八度升降快捷键（键位查表：Shortcuts 注册表 global.octaveDown/Up，
      // 设置页可自定义；弹奏键区本身是乐器布局，固定不自定义）
      var KS = window.Shortcuts;
      if (KS && KS.matches(e, "global.octaveDown")) {
        self.baseOctave = Math.max(1, self.baseOctave - 1);
        if (window.UI && window.UI.toast) window.UI.toast("键盘八度: C" + self.baseOctave, "ok");
        return;
      }
      if (KS && KS.matches(e, "global.octaveUp")) {
        self.baseOctave = Math.min(7, self.baseOctave + 1);
        if (window.UI && window.UI.toast) window.UI.toast("键盘八度: C" + self.baseOctave, "ok");
        return;
      }

      if (e.repeat || self.pressedKeys[e.code]) return;

      var offset = undefined;
      if (KEY_MAP_LOW[e.code] !== undefined) {
        offset = KEY_MAP_LOW[e.code];
      } else if (KEY_MAP_HIGH[e.code] !== undefined) {
        offset = KEY_MAP_HIGH[e.code];
      }

      if (offset !== undefined) {
        var baseMidi = (self.baseOctave + 1) * 12; // C4 = 60
        var note = Math.max(0, Math.min(127, baseMidi + offset));
        self.pressedKeys[e.code] = note;
        self.heldNotes[note] = true;
        self.emit("noteon", note, 100);
      }
    });

    window.addEventListener("keyup", function (e) {
      if (self.pressedKeys[e.code] !== undefined) {
        var note = self.pressedKeys[e.code];
        delete self.pressedKeys[e.code];
        delete self.heldNotes[note];
        self.emit("noteoff", note, 0);
      }
    });
  };

  window.MidiInputRouter = new MidiInputRouter();
})(window);
