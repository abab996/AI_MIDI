/* 设置页逻辑 */
(function () {
  "use strict";
  var UI = window.UI;
  var $ = UI.qs;

  var DEFAULTS = {
    api_key: "",
    base_url: "https://api.deepseek.com",
    api_path: "",
    model: "deepseek-v4-pro",
    max_tokens: "",
    max_completion_tokens: "",
    reasoning_effort: "max",
    thinking_enabled: true,
  };

  var current = null;
  var menuOpen = false;

  function renderModelMenu(models) {
    var menu = $("#modelMenu");
    menu.innerHTML = "";
    if (!models || !models.length) {
      var empty = document.createElement("div");
      empty.className = "select-empty";
      empty.textContent = "暂无模型，点击刷新列表";
      menu.appendChild(empty);
      return;
    }
    models.forEach(function (m) {
      var opt = document.createElement("div");
      opt.className = "select-option";
      opt.textContent = m;
      opt.title = m;
      opt.addEventListener("click", function () {
        $("#model").value = m;
        closeModelMenu();
      });
      menu.appendChild(opt);
    });
  }

  function openModelMenu() {
    var menu = $("#modelMenu");
    if (menuOpen) return;
    menu.hidden = false;
    menu.classList.remove("menu-in");
    void menu.offsetWidth;
    menu.classList.add("menu-in");
    menuOpen = true;
  }

  function closeModelMenu() {
    var menu = $("#modelMenu");
    if (!menuOpen) return;
    menu.hidden = true;
    menu.classList.remove("menu-in");
    menuOpen = false;
  }

  function fillForm(s) {
    current = s;
    $("#apiKey").value = s.api_key || "";
    $("#baseUrl").value = s.base_url || "";
    $("#apiPath").value = s.api_path || "";
    $("#model").value = s.model || "";
    $("#maxTokens").value = s.max_tokens == null ? "" : s.max_tokens;
    $("#maxCompletion").value = s.max_completion_tokens == null ? "" : s.max_completion_tokens;
    $("#thinkingEnabled").checked = !!s.thinking_enabled;

    var effort = (s.reasoning_effort || "max").toLowerCase();
    UI.qsa(".fn", $("#effortSelector")).forEach(function (b) {
      b.classList.toggle("active", b.dataset.effort === effort);
    });
  }

  function collect() {
    var effort = "max";
    UI.qsa(".fn.active", $("#effortSelector")).forEach(function (b) {
      effort = b.dataset.effort;
    });
    /* 数值字段：空串或 0 都按"未设置"处理（0 在运行时被当作未设置，
       若存 0 设置页会显示 0 但实际不生效——误导用户） */
    function numOrNull(input) {
      var v = input.value;
      if (v === "") return null;
      var n = Number(v);
      return n === 0 ? null : n;
    }
    return {
      api_key: $("#apiKey").value,
      base_url: $("#baseUrl").value,
      api_path: $("#apiPath").value,
      model: $("#model").value,
      max_tokens: numOrNull($("#maxTokens")),
      max_completion_tokens: numOrNull($("#maxCompletion")),
      reasoning_effort: effort,
      thinking_enabled: $("#thinkingEnabled").checked,
    };
  }

  /* ═══════════ MIDI 硬件设备管理 ═══════════ */
  var midiAccess = null;
  var activeMidiInput = null;
  var midiSignalTimer = null;

  var MIDI_STORAGE_KEY = "ai-midi-hardware-settings";
  var DEFAULT_MIDI_SETTINGS = {
    enabled: true,
    deviceId: "",
    channel: "all",
    velocityCurve: "linear",
    typingKeyboard: true,
  };

  function loadMidiSettings() {
    try {
      var raw = localStorage.getItem(MIDI_STORAGE_KEY);
      return raw ? Object.assign({}, DEFAULT_MIDI_SETTINGS, JSON.parse(raw)) : DEFAULT_MIDI_SETTINGS;
    } catch (e) {
      return DEFAULT_MIDI_SETTINGS;
    }
  }

  function saveMidiSettings(s) {
    try {
      localStorage.setItem(MIDI_STORAGE_KEY, JSON.stringify(s));
    } catch (e) {}
  }

  function flashMidiSignal(note, vel) {
    var stamp = $("#midiSignalStamp");
    if (!stamp) return;
    stamp.style.display = "inline-flex";
    stamp.className = "stamp ok";
    stamp.textContent = "● MIDI: " + note + " (v" + vel + ")";
    clearTimeout(midiSignalTimer);
    midiSignalTimer = setTimeout(function () {
      stamp.className = "stamp";
      stamp.textContent = "● MIDI IN";
    }, 400);
  }

  function onMidiMessage(e) {
    var data = e.data;
    if (!data || data.length < 2) return;
    var status = data[0] & 0xf0;
    var channel = (data[0] & 0x0f) + 1;
    var cfgChannel = $("#midiChannel") ? $("#midiChannel").value : "all";
    if (cfgChannel !== "all" && parseInt(cfgChannel, 10) !== channel) return;

    var note = data[1];
    var vel = data.length > 2 ? data[2] : 0;
    if (status === 0x90 && vel > 0) {
      flashMidiSignal(note, vel);
    }
  }

  var lastMidiDeviceSig = "";

  function scanMidiDevices(preferredId, isBackgroundEvent) {
    var sel = $("#midiDeviceSelect");
    var status = $("#midiDeviceStatus");
    if (!sel || !status) return;

    if (!navigator.requestMIDIAccess) {
      status.textContent = "当前环境/浏览器不支持 Web MIDI API";
      status.className = "dim err";
      sel.innerHTML = '<option value="">（不支持 Web MIDI API）</option>';
      return;
    }

    if (!isBackgroundEvent && !midiAccess) {
      status.textContent = "正在扫描 MIDI 设备…";
    }

    var promise = midiAccess ? Promise.resolve(midiAccess) : navigator.requestMIDIAccess();
    promise.then(function (access) {
      midiAccess = access;

      var inputs = Array.from(access.inputs.values());
      var currentSig = inputs.map(function (inp) { return inp.id + ":" + inp.state; }).join(",");

      // 仅在设备列表有真实物理变动时才重绘下拉列表与提示，彻底根治递归死循环抽搐
      if (currentSig !== lastMidiDeviceSig || !sel.options.length) {
        lastMidiDeviceSig = currentSig;
        sel.innerHTML = "";

        var optAuto = document.createElement("option");
        optAuto.value = "auto";
        optAuto.textContent = "自动连接首个可用设备 (Auto)";
        sel.appendChild(optAuto);

        if (!inputs.length) {
          var optNone = document.createElement("option");
          optNone.value = "";
          optNone.textContent = "（未检测到硬件 MIDI 键盘，仍可使用电脑键盘弹奏）";
          sel.appendChild(optNone);
          status.textContent = "未检测到外部 MIDI 输入设备";
          status.className = "dim";
        } else {
          inputs.forEach(function (inp) {
            var opt = document.createElement("option");
            opt.value = inp.id;
            opt.textContent = (inp.name || "MIDI 设备") + (inp.manufacturer ? " (" + inp.manufacturer + ")" : "");
            sel.appendChild(opt);
          });
          status.textContent = "✓ 已检测到 " + inputs.length + " 个 MIDI 输入设备";
          status.className = "dim ok";
        }

        var targetId = preferredId !== undefined ? preferredId : (sel.value || "auto");
        if (targetId && Array.from(sel.options).some(function (o) { return o.value === targetId; })) {
          sel.value = targetId;
        }

        bindSelectedMidiInput(sel.value);
      }

      // 仅绑定一次 onstatechange 事件
      if (!access._stateChangeBound) {
        access._stateChangeBound = true;
        access.onstatechange = function () {
          scanMidiDevices(sel.value, true);
        };
      }
    }).catch(function (err) {
      status.textContent = "获取 Web MIDI 权限失败: " + err.message;
      status.className = "dim err";
    });
  }

  function bindSelectedMidiInput(deviceId) {
    if (!midiAccess || !$("#midiInputEnabled") || !$("#midiInputEnabled").checked) {
      if (activeMidiInput) {
        try { activeMidiInput.onmidimessage = null; } catch (e) {}
        activeMidiInput = null;
      }
      var stamp = $("#midiSignalStamp");
      if (stamp) stamp.style.display = "none";
      return;
    }

    var inputs = Array.from(midiAccess.inputs.values());
    var target = null;
    if (deviceId === "auto" || !deviceId) {
      target = inputs[0] || null;
    } else {
      target = midiAccess.inputs.get(deviceId) || null;
    }

    // 若当前设备已正确挂载监听器，避免重复解绑与重新赋值触发端口状态事件
    if (activeMidiInput === target && target && target.onmidimessage === onMidiMessage) {
      var stamp = $("#midiSignalStamp");
      if (stamp) stamp.style.display = "inline-flex";
      return;
    }

    if (activeMidiInput) {
      try { activeMidiInput.onmidimessage = null; } catch (e) {}
      activeMidiInput = null;
    }

    if (target) {
      activeMidiInput = target;
      activeMidiInput.onmidimessage = onMidiMessage;
      var stamp = $("#midiSignalStamp");
      if (stamp) stamp.style.display = "inline-flex";
    }
  }

  function fillMidiSettings() {
    var ms = loadMidiSettings();
    if ($("#midiInputEnabled")) $("#midiInputEnabled").checked = ms.enabled !== false;
    if ($("#midiChannel")) $("#midiChannel").value = ms.channel || "all";
    if ($("#midiVelocityCurve")) $("#midiVelocityCurve").value = ms.velocityCurve || "linear";
    if ($("#typingKeyboardEnabled")) $("#typingKeyboardEnabled").checked = ms.typingKeyboard !== false;
    scanMidiDevices(ms.deviceId);
  }

  function collectMidiSettings() {
    return {
      enabled: $("#midiInputEnabled") ? $("#midiInputEnabled").checked : true,
      deviceId: $("#midiDeviceSelect") ? $("#midiDeviceSelect").value : "auto",
      channel: $("#midiChannel") ? $("#midiChannel").value : "all",
      velocityCurve: $("#midiVelocityCurve") ? $("#midiVelocityCurve").value : "linear",
      typingKeyboard: $("#typingKeyboardEnabled") ? $("#typingKeyboardEnabled").checked : true,
    };
  }

  /* ═══════════ 标签页（左侧标签 + 右侧面板） ═══════════ */

  var reducedMotion = typeof window.matchMedia === "function"
    && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  var activePane = "";
  var paneToken = 0;

  function clearPaneAnim(p) {
    p.classList.remove("sc-out-up", "sc-out-down", "sc-in-up", "sc-in-down");
  }

  /* 标签序号：新标签在旧标签之后 = 向下滚动，反之向上 */
  function tabOrderIndex(paneId) {
    var btns = UI.qsa(".settings-tab", $(".settings-tabs"));
    for (var i = 0; i < btns.length; i++) {
      if (btns[i].dataset.pane === paneId) return i;
    }
    return -1;
  }

  /* 初始化/复位：无动画直接同步标签与面板显隐 */
  function activatePane(paneId) {
    UI.qsa(".settings-tab", $(".settings-tabs")).forEach(function (t) {
      var on = t.dataset.pane === paneId;
      t.classList.toggle("active", on);
      t.setAttribute("aria-selected", on ? "true" : "false");
    });
    UI.qsa(".settings-pane", $(".settings-main")).forEach(function (p) {
      p.hidden = p.id !== paneId;
    });
    activePane = paneId;
  }

  /* 切换面板：纵向滚动 + 运动模糊——旧面板朝反方向带模糊滑出（0.34s），
     新面板从另一侧带模糊推入、落定时清晰（0.46s）；token 防快速连点竞态；
     reduced-motion 直接切换显隐 */
  function switchPane(btn, paneId) {
    if (paneId === activePane) return;
    if (paneId === "paneShortcuts") renderShortcutsPane();
    if (paneId === "paneLibrary") loadLibraryFiles();
    paneToken++;
    var token = paneToken;

    UI.qsa(".settings-tab", $(".settings-tabs")).forEach(function (t) {
      var on = t === btn;
      t.classList.toggle("active", on);
      t.setAttribute("aria-selected", on ? "true" : "false");
    });
    btn.classList.add("pressed");
    setTimeout(function () {
      if (token === paneToken) btn.classList.remove("pressed");
    }, 420);

    var current = $("#" + activePane);
    var pane = $("#" + paneId);
    if (!current || !pane) { activePane = paneId; return; }

    if (reducedMotion) {
      clearPaneAnim(current);
      clearPaneAnim(pane);
      current.hidden = true;
      pane.hidden = false;
      activePane = paneId;
      return;
    }

    var down = tabOrderIndex(paneId) >= tabOrderIndex(activePane);
    var outCls = down ? "sc-out-up" : "sc-out-down";
    var inCls = down ? "sc-in-down" : "sc-in-up";

    clearPaneAnim(current);
    clearPaneAnim(pane);
    current.classList.add(outCls);

    setTimeout(function () {
      if (token !== paneToken) return;
      current.hidden = true;
      current.classList.remove(outCls);
      activePane = paneId;

      pane.hidden = false;
      void pane.offsetWidth;   /* 重启动画 */
      pane.classList.add(inCls);
      setTimeout(function () {
        if (token !== paneToken) return;
        pane.classList.remove(inCls);
      }, 500);
    }, 340);
  }

  /* ═══════════ 快捷键面板（paneShortcuts） ═══════════
     渲染注册表全部动作（Shortcuts.groups），每行：动作名 + 当前键位 +
     「更改」（进入捕获态，按下新键位即绑定，Esc 取消）+「恢复默认」。
     绑定即时写入 localStorage（与音频设置"即时生效"一致，chat 页下次
     加载即生效）；冲突键位由 Shortcuts.set 拒绝并提示占用者。 */
  var _capturingRow = null;   // 当前捕获态的 DOM 行

  function renderShortcutsPane() {
    var list = $("#shortcutsList");
    if (!list || !window.Shortcuts) return;
    list.innerHTML = "";
    var groups = window.Shortcuts.groups();
    groups.forEach(function (g) {
      if (!g.items.length) return;
      var title = document.createElement("div");
      title.className = "group-title";
      title.style.marginTop = "8px";
      title.textContent = g.label;
      list.appendChild(title);
      g.items.forEach(function (item) {
        var row = document.createElement("div");
        row.className = "shortcut-row";
        row.dataset.action = item.action;

        var name = document.createElement("span");
        name.className = "shortcut-name";
        name.textContent = item.label;

        var keys = document.createElement("span");
        keys.className = "shortcut-keys";
        keys.textContent = window.Shortcuts.pretty(item.keys);

        var changeBtn = document.createElement("button");
        changeBtn.type = "button";
        changeBtn.className = "btn btn-secondary btn-sm";
        changeBtn.textContent = "更改";
        changeBtn.addEventListener("click", function () {
          startCapture(row);
        });

        var resetBtn = document.createElement("button");
        resetBtn.type = "button";
        resetBtn.className = "btn btn-secondary btn-sm shortcut-reset";
        resetBtn.textContent = "恢复默认";
        resetBtn.addEventListener("click", function () {
          window.Shortcuts.reset(item.action);
          renderShortcutsPane();
          UI.toast("✓ 已恢复默认: " + item.label, "ok");
        });

        row.appendChild(name);
        row.appendChild(keys);
        row.appendChild(changeBtn);
        row.appendChild(resetBtn);
        list.appendChild(row);
      });
    });

    var resetAll = $("#shortcutsResetAllBtn");
    if (resetAll) {
      resetAll.onclick = function () {
        window.Shortcuts.resetAll();
        renderShortcutsPane();
        UI.toast("✓ 已恢复全部默认键位", "ok");
      };
    }
  }

  /* 捕获态：行高亮 + 提示"按下新键位…"；Esc/鼠标点击取消；
     修饰键组合按下时等待松开再判定（避免 Ctrl 按下瞬间误绑） */
  function startCapture(row) {
    if (_capturingRow) cancelCapture();
    _capturingRow = row;
    row.classList.add("capturing");
    var hint = $("#shortcutsHint");
    if (hint) hint.textContent = "按下新键位…（Esc 取消）";
    row.querySelector(".shortcut-keys").textContent = "…";

    var done = false;
    function finish() {
      if (done) return;
      done = true;
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("mousedown", onMouse, true);
      if (_capturingRow === row) _capturingRow = null;
      var h2 = $("#shortcutsHint");
      if (h2) h2.textContent = "";
      row.classList.remove("capturing");
      renderShortcutsPane();   // 恢复键位显示
    }
    function onKey(e) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(); return; }
      if (e.key === "Control" || e.key === "Alt" || e.key === "Shift" || e.key === "Meta") return; // 等修饰键松开
      e.preventDefault();
      e.stopPropagation();
      var spec = window.Shortcuts.normalizeKey(e);
      if (!spec || spec === "+") { finish(); return; }
      var action = row.dataset.action;
      var err = window.Shortcuts.set(action, spec);
      if (err) {
        UI.toast("✗ " + err, "err");
      } else {
        UI.toast("✓ 已绑定 " + window.Shortcuts.label(action) + ": " + window.Shortcuts.pretty(spec), "ok");
      }
      finish();
    }
    function onMouse(e) {
      if (!row.contains(e.target)) finish();
    }
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("mousedown", onMouse, true);
  }

  function cancelCapture() {
    if (!_capturingRow) return;
    _capturingRow.classList.remove("capturing");
    _capturingRow = null;
    var hint = $("#shortcutsHint");
    if (hint) hint.textContent = "";
  }

  /* ═══════════ 用户知识库（paneLibrary） ═══════════
     Library/user/ 下的 .md/.txt 文件管理：文件名即内容概括，AI 据此判断
     是否 read_library_file。列表/上传/删除/重命名/预览，操作即时生效
     （AI 每轮请求都会重建文件清单，无需保存配置）。 */

  function libFmtSize(n) {
    if (!(n >= 0)) return "";
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    return (n / 1024 / 1024).toFixed(2) + " MB";
  }

  function libFmtTime(ms) {
    try { return new Date(ms).toLocaleDateString(); } catch (e) { return ""; }
  }

  /* 当前用户文件名（小写，Windows 文件名不区分大小写），上传时判断同名覆盖 */
  var libUserNames = [];

  function libFileRow(f, builtin) {
    var row = document.createElement("div");
    row.className = "lib-file-row";
    row.dataset.name = f.name;

    var name = document.createElement("span");
    name.className = "lib-file-name";
    name.textContent = f.name;
    name.title = f.name;
    row.appendChild(name);

    if (builtin) {
      var tag = document.createElement("span");
      tag.className = "lib-file-tag";
      tag.textContent = "内置";
      row.appendChild(tag);
    }

    var meta = document.createElement("span");
    meta.className = "lib-file-meta dim";
    meta.textContent = libFmtSize(f.size) + " · " + libFmtTime(f.modified);
    row.appendChild(meta);

    var actions = document.createElement("span");
    actions.className = "lib-file-actions";

    var previewBtn = document.createElement("button");
    previewBtn.type = "button";
    previewBtn.className = "btn btn-secondary btn-sm action-preview";
    previewBtn.textContent = "预览";
    actions.appendChild(previewBtn);

    if (!builtin) {
      var renameBtn = document.createElement("button");
      renameBtn.type = "button";
      renameBtn.className = "btn btn-secondary btn-sm action-rename";
      renameBtn.textContent = "重命名";
      actions.appendChild(renameBtn);

      var delBtn = document.createElement("button");
      delBtn.type = "button";
      delBtn.className = "btn btn-danger btn-sm action-del";
      delBtn.textContent = "删除";
      actions.appendChild(delBtn);
    }

    row.appendChild(actions);
    return row;
  }

  function libEmptyHint(text) {
    var empty = document.createElement("div");
    empty.className = "dim lib-file-empty";
    empty.textContent = text;
    return empty;
  }

  function renderLibraryLists(data) {
    var list = $("#libFileList");
    var builtinList = $("#libBuiltinList");
    if (!list || !builtinList) return;
    list.innerHTML = "";
    builtinList.innerHTML = "";
    libUserNames = [];

    var user = data.user || [];
    var builtin = data.builtin || [];
    var status = $("#libStatus");
    if (status) {
      status.textContent = user.length
        ? "共 " + user.length + " 个自定义文件"
        : "尚未添加自定义知识文件";
    }

    if (!user.length) {
      list.appendChild(libEmptyHint("还没有自定义知识文件，点击「上传知识文件」添加（支持多选）。"));
    }
    user.forEach(function (f) {
      libUserNames.push((f.name || "").toLowerCase());
      list.appendChild(libFileRow(f, false));
    });

    if (!builtin.length) {
      builtinList.appendChild(libEmptyHint("未检测到内置知识文件（Library 目录缺失或为空）。"));
    }
    builtin.forEach(function (f) {
      builtinList.appendChild(libFileRow(f, true));
    });
  }

  function loadLibraryFiles() {
    return UI.getJSON("/api/library/files").then(renderLibraryLists).catch(function (e) {
      UI.toast("✗ 加载知识库文件列表失败: " + e.message, "err");
    });
  }

  /* 预览：内嵌展开在行下方，再点收起（每次展开重新拉取，保证最新内容） */
  function toggleLibPreview(row) {
    var scope = row.parentElement && row.parentElement.id === "libBuiltinList" ? "builtin" : "user";
    var next = row.nextElementSibling;
    if (next && next.classList.contains("lib-preview")) {
      next.remove();
      return;
    }
    var url = "/api/library/files/content?scope=" + scope + "&name=" + encodeURIComponent(row.dataset.name);
    UI.getJSON(url).then(function (j) {
      if (!row.isConnected) return;
      var nx = row.nextElementSibling;
      if (nx && nx.classList.contains("lib-preview")) nx.remove();
      var pre = document.createElement("pre");
      pre.className = "lib-preview";
      pre.textContent = (j.content || "") + (j.truncated ? "\n\n…（内容过长，已截断显示）" : "");
      row.after(pre);
    }).catch(function (e) {
      UI.toast("✗ 预览失败: " + e.message, "err");
    });
  }

  function deleteLibFile(row) {
    var name = row.dataset.name;
    if (!window.confirm("确定删除知识文件「" + name + "」？此操作不可恢复。")) return;
    UI.delJSON("/api/library/files?name=" + encodeURIComponent(name)).then(function () {
      UI.toast("✓ 已删除 " + name, "ok");
      loadLibraryFiles();
    }).catch(function (e) {
      UI.toast("✗ 删除失败: " + e.message, "err");
    });
  }

  function renameLibFile(row) {
    var name = row.dataset.name;
    var input = window.prompt("重命名知识文件（文件名即内容概括，供 AI 判断是否调用）：", name);
    if (input === null) return;
    var to = input.trim();
    if (!to || to === name) return;
    UI.postJSON("/api/library/files/rename", { from: name, to: to }).then(function () {
      UI.toast("✓ 已重命名为 " + to, "ok");
      loadLibraryFiles();
    }).catch(function (e) {
      UI.toast("✗ 重命名失败: " + e.message, "err");
    });
  }

  /* 逐个串行上传（单文件失败不阻断后续），完成后统一刷新列表 */
  function uploadLibFiles(files) {
    var pending = Array.prototype.slice.call(files || []);
    var next = function () {
      var f = pending.shift();
      if (!f) { loadLibraryFiles(); return; }
      if (libUserNames.indexOf(f.name.toLowerCase()) >= 0 &&
          !window.confirm("已存在同名文件「" + f.name + "」，覆盖？")) {
        next();
        return;
      }
      Promise.resolve(f.arrayBuffer ? f.arrayBuffer() : new Promise(function (res, rej) {
        var fr = new FileReader();
        fr.onload = function () { res(fr.result); };
        fr.onerror = function () { rej(new Error("读取文件失败")); };
        fr.readAsArrayBuffer(f);
      })).then(function (buf) {
        return fetch("/api/library/files?name=" + encodeURIComponent(f.name), {
          method: "POST",
          headers: { "Content-Type": "application/octet-stream" },
          body: buf,
        }).then(function (r) {
          if (!r.ok) {
            return r.json().then(function (j) {
              throw new Error(j.detail || ("HTTP " + r.status));
            }, function () {
              throw new Error("HTTP " + r.status);
            });
          }
          UI.toast("✓ 已上传 " + f.name, "ok");
        });
      }).catch(function (e) {
        UI.toast("✗ 上传「" + f.name + "」失败: " + e.message, "err");
      }).then(next);
    };
    next();
  }

  function bindLibraryPane() {
    if (!$("#libUploadBtn")) return;
    $("#libUploadBtn").addEventListener("click", function () {
      $("#libFileInput").click();
    });
    $("#libFileInput").addEventListener("change", function () {
      uploadLibFiles(this.files);
      this.value = "";   // 允许重复选择同一文件
    });
    $("#libOpenDirBtn").addEventListener("click", function () {
      UI.postJSON("/api/library/open", {}).catch(function (e) {
        UI.toast("✗ 打开文件夹失败: " + e.message, "err");
      });
    });
    $("#libFileList").addEventListener("click", function (e) {
      var row = e.target.closest(".lib-file-row");
      if (!row) return;
      if (e.target.classList.contains("action-preview")) toggleLibPreview(row);
      else if (e.target.classList.contains("action-del")) deleteLibFile(row);
      else if (e.target.classList.contains("action-rename")) renameLibFile(row);
    });
    $("#libBuiltinList").addEventListener("click", function (e) {
      var row = e.target.closest(".lib-file-row");
      if (row && e.target.classList.contains("action-preview")) toggleLibPreview(row);
    });
  }

  /* ═══════════ 关于页专属特效（paneAbout） ═══════════
     纯白球 + mix-blend-mode: difference：浅色主题呈黑球白字、深色主题呈
     浅色球洞，随主题自动翻转。三态：
     - 文字：48px 圆球跟随光标；
     - Logo：26px 小球，::after 三层光影（深核/暗晕/隆起高光）定位到光标处
       ——触点局部凹陷，整块仅 0.985 微缩；
     - 按钮：瞬间"吸附"——球弹到按钮中心、变形为按钮矩形（含圆角），
       覆盖处整体反色；离开按钮恢复跟随。 */
  function initAboutFx() {
    var pane = $("#paneAbout");
    if (!pane) return;

    /* 系统开启"减弱动态效果"时跳过整个特效：
       CSS 侧只关了 transition，JS 侧不跳过的话光标仍会被球体盖住
       （cursor 样式）且球体跟随导致整屏反色重绘 */
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }

    var ball = document.createElement("div");
    ball.className = "about-ball";
    pane.appendChild(ball);

    var logoWrap = pane.querySelector(".about-logo-wrap");
    var raf = null;
    var mx = 0, my = 0, size = 48;
    var snapBtn = null;

    function paint() {
      raf = null;
      if (snapBtn) return;   // 吸附态：球钉在按钮中心，不跟光标
      ball.style.transform = "translate3d(" + mx + "px," + my + "px,0) translate(-50%,-50%)";
      ball.style.width = size + "px";
      ball.style.height = size + "px";
    }

    function releaseSnap() {
      if (!snapBtn) return;
      snapBtn = null;
      ball.classList.remove("snap");
      ball.style.borderRadius = "50%";
    }

    function snapTo(btn) {
      if (snapBtn === btn) return;
      snapBtn = btn;
      /* 几何测量只在"进入新按钮"时做一次（光标停在按钮上移动期间
         按钮几何不变）：此前每次 pointermove 都同步
         getBoundingClientRect + getComputedStyle，强制布局/样式重算，
         光标在 About 页按钮上快速移动时产生每帧抖动 */
      var r = btn.getBoundingClientRect();
      var cs = getComputedStyle(btn);
      ball.classList.add("snap");
      ball.style.transform = "translate3d(" + (r.left + r.width / 2) + "px," + (r.top + r.height / 2) + "px,0) translate(-50%,-50%)";
      ball.style.width = Math.round(r.width) + "px";
      ball.style.height = Math.round(r.height) + "px";
      ball.style.borderRadius = cs.borderRadius || "4px";
    }

    var TILT_MAX = 18;   // 3D 倾斜最大角度（度）：触点一侧明显下沉

    function moveLogoDent(x, y) {
      if (!logoWrap) return;
      var lr = logoWrap.getBoundingClientRect();
      /* 钳制到 [0,1]：光标贴边越界时（凸出条带上仍可能派发 move），
         倾斜角与凹陷位置不得越界放大 */
      var px = Math.min(1, Math.max(0, (x - lr.left) / lr.width));
      var py = Math.min(1, Math.max(0, (y - lr.top) / lr.height));
      logoWrap.style.setProperty("--dx", (px * 100).toFixed(1) + "%");
      logoWrap.style.setProperty("--dy", (py * 100).toFixed(1) + "%");
      /* rotateX 正值 = 上边下沉、rotateY 负值 = 右边下沉：
         光标在哪一侧，哪一侧朝屏幕里陷进去 */
      logoWrap.style.setProperty("--rx", ((0.5 - py) * TILT_MAX).toFixed(2) + "deg");
      logoWrap.style.setProperty("--ry", ((0.5 - px) * TILT_MAX).toFixed(2) + "deg");
    }

    function clearLogoDent() {
      if (!logoWrap) return;
      logoWrap.classList.remove("pressed");
      ["--dx", "--dy", "--rx", "--ry"].forEach(function (p) {
        logoWrap.style.removeProperty(p);
      });
    }

    pane.addEventListener("pointermove", function (e) {
      mx = e.clientX; my = e.clientY;
      var t = e.target;
      var btn = t.closest ? t.closest(".about-link, .btn") : null;
      var onLogo = t.closest ? t.closest(".about-logo-wrap") : null;
      if (btn) {
        /* 只在目标按钮变化时重测几何（snapTo 内 snapBtn===btn 直接
           return）：光标在按钮上连续移动时不再每帧 getBoundingClientRect
           + getComputedStyle，消除布局抖动 */
        snapTo(btn);
      } else {
        releaseSnap();
        if (onLogo) {
          size = 26;
          moveLogoDent(mx, my);
        } else {
          size = 48;
        }
        if (!raf) raf = requestAnimationFrame(paint);
      }
    });

    pane.addEventListener("pointerenter", function () { ball.classList.add("on"); });
    pane.addEventListener("pointerleave", function () {
      ball.classList.remove("on");
      releaseSnap();
      clearLogoDent();
    });

    if (logoWrap) {
      logoWrap.addEventListener("pointerenter", function () { logoWrap.classList.add("pressed"); });
      logoWrap.addEventListener("pointerleave", clearLogoDent);
    }
  }

  function init() {
    /* 标签页：默认激活「连接」；支持 ?tab=paneXXX 深链（chat.html 的
       「⌨ 快捷键」入口直达快捷键面板） */
    var tabFromUrl = null;
    try {
      tabFromUrl = new URLSearchParams(location.search).get("tab");
    } catch (e) {}
    var initialPane = (tabFromUrl && $("#" + tabFromUrl)) ? tabFromUrl : "paneConnect";
    activatePane(initialPane);
    UI.qsa(".settings-tab", $(".settings-tabs")).forEach(function (t) {
      t.addEventListener("click", function () { switchPane(t, t.dataset.pane); });
    });
    if (initialPane === "paneShortcuts") {
      renderShortcutsPane();
    }
    if (initialPane === "paneLibrary") {
      loadLibraryFiles();
    }
    bindLibraryPane();
    initAboutFx();

    /* 提交 Issue：跳转 GitHub issue 新建页（系统默认浏览器） */
    if ($("#issueBtn")) {
      $("#issueBtn").addEventListener("click", function () {
        if (window.UI && window.UI.openExternal) {
          UI.openExternal("https://github.com/abab996/AI_MIDI/issues/new");
        }
      });
    }

    /* 关于页外链（官网/GitHub/License）：统一走 /api/open-url 白名单，
       由系统默认浏览器打开（Wails 内 WebView 直接导航会离开应用界面） */
    UI.qsa(".about-links a[data-ext]").forEach(function (a) {
      a.addEventListener("click", function (e) {
        e.preventDefault();
        UI.openExternal(a.href);
      });
    });

    /* 加载设置（每次进入页面都从服务器读取，保证内容最新） */
    UI.getJSON("/api/settings").then(function (s) {
      fillForm(s);
      fillMidiSettings();
    }).catch(function (e) {
      UI.toast("✗ 加载设置失败: " + e.message, "err");
      fillMidiSettings();
    });

    /* About 卡片：版本号（与 wails.json 同源，失败静默显示 --） */
    if ($("#aboutVersion")) {
      UI.getJSON("/api/version").then(function (v) {
        $("#aboutVersion").textContent = "v" + (v.version || "--");
      }).catch(function () {});
    }

    /* MIDI 控制事件 */
    if ($("#refreshMidiBtn")) {
      $("#refreshMidiBtn").addEventListener("click", function () {
        scanMidiDevices($("#midiDeviceSelect") ? $("#midiDeviceSelect").value : "auto");
        UI.toast("✓ 已重新扫描 MIDI 端口", "ok");
      });
    }
    if ($("#midiDeviceSelect")) {
      $("#midiDeviceSelect").addEventListener("change", function () {
        bindSelectedMidiInput(this.value);
      });
    }
    if ($("#midiInputEnabled")) {
      $("#midiInputEnabled").addEventListener("change", function () {
        bindSelectedMidiInput($("#midiDeviceSelect").value);
      });
    }

    /* 显示/隐藏 API Key */
    var keyInput = $("#apiKey");
    $("#showKeyBtn").addEventListener("click", function () {
      var showing = keyInput.type === "text";
      keyInput.type = showing ? "password" : "text";
      this.textContent = showing ? "显示" : "隐藏";
    });

    /* 推理强度分段 */
    UI.qsa(".fn", $("#effortSelector")).forEach(function (btn) {
      btn.addEventListener("click", function () {
        UI.qsa(".fn", $("#effortSelector")).forEach(function (b) {
          b.classList.toggle("active", b === btn);
        });
      });
    });

    /* 刷新模型列表 */
    $("#refreshModelsBtn").addEventListener("click", function () {
      var btn = this;
      var s = collect();
      var status = $("#modelStatus");
      btn.disabled = true;
      status.textContent = "正在获取模型列表…";
      status.className = "dim";
      UI.postJSON("/api/models", {
        api_key: s.api_key,
        base_url: s.base_url,
        api_path: s.api_path,
      }).then(function (data) {
        status.textContent = data.message;
        status.className = data.models && data.models.length ? "ok" : "err";
        renderModelMenu(data.models || []);
        if (data.models && data.models.length) {
          if (!s.model) $("#model").value = data.models[0];
          UI.toast("✓ 已获取 " + data.models.length + " 个模型", "ok");
        } else {
          UI.toast("✗ " + data.message, "err");
        }
      }).catch(function (e) {
        status.textContent = "✗ 获取失败: " + e.message;
        status.className = "err";
      }).finally(function () { btn.disabled = false; });
    });

    /* 自定义模型下拉 */
    $("#modelToggle").addEventListener("click", function () {
      if (menuOpen) closeModelMenu(); else openModelMenu();
    });
    document.addEventListener("click", function (e) {
      if (!e.target.closest(".select-wrap")) closeModelMenu();
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") closeModelMenu();
    });

    /* 保存 */
    $("#saveBtn").addEventListener("click", function () {
      var btn = this;
      btn.disabled = true;
      saveMidiSettings(collectMidiSettings());
      UI.putJSON("/api/settings", collect()).then(function (data) {
        var st = $("#saveStatus");
        st.textContent = data.message;
        st.style.color = "var(--color-primary)";
        UI.toast(data.message, "ok");
      }).catch(function (e) {
        var st = $("#saveStatus");
        st.textContent = "✗ 保存失败: " + e.message;
        st.style.color = "var(--color-danger)";
        UI.toast("✗ " + e.message, "err");
      }).finally(function () {
        btn.disabled = false;
      });
    });

    /* 恢复默认：立即持久化且无回头路，先确认（不丢表单里未保存的 API Key） */
    $("#resetBtn").addEventListener("click", function () {
      if (!window.confirm("恢复默认将重置表单并立即保存默认参数（不影响已保存的 API Key），确定？")) return;
      fillForm(DEFAULTS);
      saveMidiSettings(DEFAULT_MIDI_SETTINGS);
      fillMidiSettings();
      /* LLM 设置也同步落盘：此前只填表单不保存，确认框却写着「立即保存」，
         用户以为已持久化，重启后旧值回来。api_key 为空时服务端保留已存密钥 */
      UI.putJSON("/api/settings", collect()).then(function () {
        UI.toast("✓ 已恢复默认设置", "ok");
      }).catch(function (e) {
        UI.toast("✗ 恢复失败: " + e.message, "err");
      });
    });
  }

  document.addEventListener("DOMContentLoaded", init);
})();
