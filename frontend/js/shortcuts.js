/* 快捷键注册表：默认键位 + localStorage 自定义覆盖。
   统一内部格式：修饰键前缀 + e.code（如 "Ctrl+KeyZ"、"Shift+ArrowUp"、
   "Space"、"F2"）——code 与 Shift 状态解耦，符号键（=/-/[）不受
   大小写/输入法影响；显示名由 prettyKey 生成（设置页用）。
   系统保留不可自定义：Esc（分层关闭）、Enter（输入框确认/发送）、
   键盘弹奏 27 键（乐器布局）、鼠标手势——不在注册表中，设置页注明。
   存储：localStorage "ai-midi-shortcuts"（{action: spec|spec[]}）；
   加载时与默认表合并（新增动作自动补默认值）。 */
(function (window) {
  "use strict";

  var STORAGE_KEY = "ai-midi-shortcuts";

  /* 动作 → 默认键位（现状盘点：pianoroll.js / arrange.js / midi_input.js
     改造前的硬编码；数组 = 任一命中即触发） */
  var DEFAULTS = {
    /* ── 全局（键盘弹奏） ── */
    "global.octaveDown": ["BracketLeft", "Minus", "PageDown"],
    "global.octaveUp": ["BracketRight", "Equal", "PageUp"],

    /* ── 钢琴窗 ── */
    "piano.undo": "Ctrl+KeyZ",
    "piano.redo": ["Ctrl+KeyY", "Ctrl+Shift+KeyZ", "Ctrl+Alt+KeyZ"],
    "piano.selectAll": "Ctrl+KeyA",
    "piano.deselect": "Ctrl+KeyD",
    "piano.invertSelect": "Ctrl+KeyI",
    "piano.copy": "Ctrl+KeyC",
    "piano.paste": "Ctrl+KeyV",
    "piano.cut": "Ctrl+KeyX",
    "piano.delete": ["Delete", "Backspace"],
    "piano.record": "Ctrl+KeyR",
    "piano.playPause": "Space",
    "piano.duplicateNext": "Ctrl+KeyB",
    "piano.quickQuantize": "Ctrl+KeyQ",
    "piano.legato": ["Ctrl+KeyL", "Alt+KeyL"],
    "piano.transposeUp12": "Ctrl+ArrowUp",
    "piano.transposeDown12": "Ctrl+ArrowDown",
    "piano.transposeUp1": "Shift+ArrowUp",
    "piano.transposeDown1": "Shift+ArrowDown",
    "piano.toolDraw": "KeyP",
    "piano.toolPaint": "KeyB",
    "piano.toolErase": "KeyD",
    "piano.toolSlice": "KeyC",
    "piano.toolSelect": "KeyE",
    "piano.toolMute": "KeyT",
    "piano.toolZoom": "KeyZ",
    "piano.toggleTyping": "Ctrl+KeyT",
    "piano.levelScale": "Alt+KeyX",
    "piano.strum": "Alt+KeyS",
    "piano.arpeggio": "Alt+KeyA",
    "piano.randomize": "Alt+KeyR",
    "piano.flip": "Alt+KeyY",
    "piano.quantizeDialog": "Alt+KeyQ",

    /* ── 编曲窗 ── */
    "arrange.playPause": "Space",
    "arrange.home": "Home",
    "arrange.end": "End",
    "arrange.nudgeLeft": "ArrowLeft",
    "arrange.nudgeRight": "ArrowRight",
    "arrange.nudgeBarLeft": "Shift+ArrowLeft",
    "arrange.nudgeBarRight": "Shift+ArrowRight",
    "arrange.trackUp": "ArrowUp",
    "arrange.trackDown": "ArrowDown",
    "arrange.muteTrack": "KeyM",
    "arrange.soloTrack": "Shift+KeyS",
    "arrange.splitAtPlayhead": "KeyS",
    "arrange.toggleLoop": "KeyL",
    "arrange.renameTrack": "F2",
    "arrange.undo": "Ctrl+KeyZ",
    "arrange.redo": ["Ctrl+KeyY", "Ctrl+Shift+KeyZ"],
    "arrange.copy": "Ctrl+KeyC",
    "arrange.cut": "Ctrl+KeyX",
    "arrange.paste": "Ctrl+KeyV",
    "arrange.clone": "Ctrl+KeyD",
    "arrange.duplicateNext": "Ctrl+KeyB",
    "arrange.selectAll": "Ctrl+KeyA",
    "arrange.delete": ["Delete", "Backspace"],
    "arrange.zoomIn": ["Equal", "NumpadAdd"],
    "arrange.zoomOut": ["Minus", "NumpadSubtract"]
  };

  var GROUP_ORDER = [
    { key: "global", label: "全局（键盘弹奏）" },
    { key: "piano", label: "钢琴窗" },
    { key: "arrange", label: "编曲窗" }
  ];

  var _cache = null;   // {action: [spec, ...]}

  function loadRaw() {
    try {
      var v = localStorage.getItem(STORAGE_KEY);
      if (v) return JSON.parse(v);
    } catch (e) {}
    return {};
  }

  function getKeys(action) {
    if (!_cache) buildCache();
    return _cache[action] || [];
  }

  function buildCache() {
    _cache = {};
    var custom = loadRaw();
    Object.keys(DEFAULTS).forEach(function (action) {
      var spec = custom[action] !== undefined ? custom[action] : DEFAULTS[action];
      _cache[action] = Array.isArray(spec) ? spec.slice() : [spec];
    });
  }

  /* 事件 → 内部键位串（修饰键前缀 + e.code） */
  function normalizeKey(e) {
    var parts = [];
    if (e.ctrlKey || e.metaKey) parts.push("Ctrl");
    if (e.altKey) parts.push("Alt");
    if (e.shiftKey) parts.push("Shift");
    parts.push(e.code || "");
    return parts.join("+");
  }

  /* 当前事件是否命中指定动作（任一绑定键位命中即 true） */
  function matches(e, action) {
    var spec = normalizeKey(e);
    var keys = getKeys(action);
    for (var i = 0; i < keys.length; i++) {
      if (keys[i] === spec) return true;
    }
    return false;
  }

  /* 内部串 → 可读显示（设置页/提示用） */
  function prettyKey(spec) {
    var CODE_NAMES = {
      Space: "空格", Enter: "回车", Tab: "Tab", Backspace: "退格", Delete: "Delete",
      Escape: "Esc", ArrowUp: "↑", ArrowDown: "↓", ArrowLeft: "←", ArrowRight: "→",
      PageUp: "PageUp", PageDown: "PageDown", Home: "Home", End: "End",
      BracketLeft: "[", BracketRight: "]", Minus: "-", Equal: "=",
      Semicolon: ";", Comma: ",", Period: ".", Slash: "/", Quote: "'",
      Backquote: "`", Backslash: "\\", NumpadAdd: "小键盘+", NumpadSubtract: "小键盘-"
    };
    return String(spec).split("+").map(function (part) {
      if (part === "Ctrl") return "Ctrl";
      if (part === "Alt") return "Alt";
      if (part === "Shift") return "Shift";
      if (CODE_NAMES[part]) return CODE_NAMES[part];
      if (part.indexOf("Key") === 0) return part.slice(3);
      if (part.indexOf("Digit") === 0) return part.slice(5);
      if (part.indexOf("F") === 0 && /^F\d+$/.test(part)) return part;
      return part;
    }).join("+");
  }

  function pretty(specOrList) {
    var list = Array.isArray(specOrList) ? specOrList : [specOrList];
    return list.map(prettyKey).join(" / ");
  }

  /* 绑定新键位（spec 为内部格式，可传数组）；返回错误文本或 null */
  function set(action, spec) {
    if (!DEFAULTS[action]) return "未知动作: " + action;
    var norm = Array.isArray(spec) ? spec.slice() : [spec];
    // 冲突检测：同键位被其他动作占用 → 拒绝并告知占用者（首个冲突即停）
    var custom = loadRaw();
    var conflict = null;
    Object.keys(DEFAULTS).forEach(function (other) {
      if (other === action || conflict) return;
      var otherKeys = custom[other] !== undefined ? custom[other] : DEFAULTS[other];
      var list = Array.isArray(otherKeys) ? otherKeys : [otherKeys];
      for (var i = 0; i < list.length && !conflict; i++) {
        if (norm.indexOf(list[i]) !== -1) conflict = other;
      }
    });
    if (conflict) {
      return "键位 " + pretty(norm) + " 已被「" + actionLabel(conflict) + "」占用";
    }
    custom[action] = norm.length === 1 ? norm[0] : norm;
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(custom));
    } catch (e) {
      return "保存失败（浏览器存储不可用）";
    }
    _cache = null;
    return null;
  }

  function reset(action) {
    var custom = loadRaw();
    delete custom[action];
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(custom)); } catch (e) {}
    _cache = null;
  }

  function resetAll() {
    try { localStorage.removeItem(STORAGE_KEY); } catch (e) {}
    _cache = null;
  }

  function all() {
    if (!_cache) buildCache();
    var out = [];
    Object.keys(DEFAULTS).forEach(function (action) {
      out.push({ action: action, keys: _cache[action], group: action.split(".")[0], label: actionLabel(action) });
    });
    return out;
  }

  function groups() {
    var byGroup = {};
    all().forEach(function (item) {
      (byGroup[item.group] = byGroup[item.group] || []).push(item);
    });
    return GROUP_ORDER.map(function (g) {
      return { key: g.key, label: g.label, items: byGroup[g.key] || [] };
    });
  }

  var LABELS = {
    "global.octaveDown": "键盘八度降低（弹奏）",
    "global.octaveUp": "键盘八度升高（弹奏）",
    "piano.undo": "撤销", "piano.redo": "重做",
    "piano.selectAll": "全选", "piano.deselect": "取消选择", "piano.invertSelect": "反选",
    "piano.copy": "复制", "piano.paste": "粘贴", "piano.cut": "剪切",
    "piano.delete": "删除选中音符", "piano.record": "录制开关", "piano.playPause": "播放/暂停",
    "piano.duplicateNext": "顺延复制至下一小节", "piano.quickQuantize": "快速量化", "piano.legato": "快速连奏",
    "piano.transposeUp12": "八度上移", "piano.transposeDown12": "八度下移",
    "piano.transposeUp1": "半音上移", "piano.transposeDown1": "半音下移",
    "piano.toolDraw": "工具：画笔", "piano.toolPaint": "工具：笔刷", "piano.toolErase": "工具：擦除",
    "piano.toolSlice": "工具：切片刀", "piano.toolSelect": "工具：框选", "piano.toolMute": "工具：静音",
    "piano.toolZoom": "工具：缩放", "piano.toggleTyping": "切换键盘弹奏",
    "piano.levelScale": "力度缩放器", "piano.strum": "扫弦工具", "piano.arpeggio": "琶音器",
    "piano.randomize": "随机化", "piano.flip": "翻转", "piano.quantizeDialog": "量化弹窗",
    "arrange.playPause": "播放/暂停", "arrange.home": "回到开头", "arrange.end": "跳到末尾",
    "arrange.nudgeLeft": "播放头左移（吸附）", "arrange.nudgeRight": "播放头右移（吸附）",
    "arrange.nudgeBarLeft": "播放头左移（小节）", "arrange.nudgeBarRight": "播放头右移（小节）",
    "arrange.trackUp": "选中上一轨", "arrange.trackDown": "选中下一轨",
    "arrange.muteTrack": "静音当前轨", "arrange.soloTrack": "独奏当前轨",
    "arrange.splitAtPlayhead": "播放头处拆分", "arrange.toggleLoop": "循环开关",
    "arrange.renameTrack": "重命名轨道", "arrange.undo": "撤销", "arrange.redo": "重做",
    "arrange.copy": "复制", "arrange.cut": "剪切", "arrange.paste": "粘贴",
    "arrange.clone": "原地克隆", "arrange.duplicateNext": "向右顺延复制",
    "arrange.selectAll": "全选 Clip", "arrange.delete": "删除选中",
    "arrange.zoomIn": "放大", "arrange.zoomOut": "缩小"
  };

  function actionLabel(action) {
    return LABELS[action] || action;
  }

  window.Shortcuts = {
    all: all,
    groups: groups,
    get: getKeys,
    matches: matches,
    normalizeKey: normalizeKey,
    pretty: pretty,
    set: set,
    reset: reset,
    resetAll: resetAll,
    label: actionLabel,
    STORAGE_KEY: STORAGE_KEY
  };
})(window);
