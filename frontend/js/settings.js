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

  function init() {
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
        UI.toast("已重新扫描 MIDI 端口", "ok");
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
