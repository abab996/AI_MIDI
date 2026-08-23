/* 工作台逻辑 */
(function () {
  "use strict";
  var UI = window.UI;
  var $ = UI.qs;
  var noteTable = [];
  var busy = false;
  var currentFunc = "配和弦";
  var funcToken = 0;
  var reducedMotion = typeof window.matchMedia === "function"
    && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function funcFields(func) {
    var map = {
      "配和弦": "fReq",
      "翻译歌词": "fLyrics fLang",
      "设计转音": "fLyrics fReq",
      "其他要求": "fLyrics fNote fReq",
    };
    return (map[func] || "fReq").split(/\s+/);
  }

  function clearDynField(f) {
    f.classList.remove("dyn-in", "dyn-out");
    f.style.removeProperty("--dyn-delay");
    f.style.removeProperty("--out-dx");
    f.style.removeProperty("--out-dy");
    f.style.removeProperty("--fly-dx");
    f.style.removeProperty("--fly-dy");
  }

  function applyFunc(btn) {
    var name = btn.textContent.trim();
    if (name === currentFunc) return;
    funcToken++;
    var token = funcToken;

    UI.qsa(".fn", $("#funcSelector")).forEach(function (b) {
      b.classList.toggle("active", b === btn);
    });
    currentFunc = name;
    var show = funcFields(currentFunc);

    /* 全部控件（.dyn-control：4 字段 + BPM/拍号行 + 开始按钮）每次切换都
       完整参与"收拢 → 弹出"，包括切换前后都存在的元素；
       切换后不再显示的字段在收拢完成后置 hidden */
    var controls = UI.qsa(".dyn-control");
    var persistent = controls.filter(function (f) {
      return f.id === "measureRow" || f.id === "startBtn";
    });
    var fields = controls.filter(function (f) {
      return f.id !== "measureRow" && f.id !== "startBtn";
    });

    if (reducedMotion) {
      fields.forEach(function (f) { f.hidden = show.indexOf(f.id) === -1; });
      return;
    }

    /* 按钮 Q 弹按压反馈（切换的"撞击点"） */
    btn.classList.add("pressed");
    setTimeout(function () {
      if (token === funcToken) btn.classList.remove("pressed");
    }, 420);

    /* 退场：当前可见的全部控件从下往上依次朝按钮中心收拢
       （级联间隔 60ms：最下面的开始按钮先收、依次向上，隐藏中的字段不参与） */
    var br = btn.getBoundingClientRect();
    var ox = br.left + br.width / 2;
    var oy = br.top + br.height / 2;
    var outList = controls.filter(function (f) { return !f.hidden; });
    outList.forEach(function (f, i) {
      clearDynField(f);
      var r = f.getBoundingClientRect();
      var fx = r.left + r.width / 2;
      var fy = r.top + r.height / 2;
      var dx = ox - fx;
      var dy = oy - fy;
      var d = Math.hypot(dx, dy);
      if (d < 1) { dx = 0; dy = 1; d = 1; }
      var pull = 14;
      f.style.setProperty("--out-dx", (dx / d * pull).toFixed(1) + "px");
      f.style.setProperty("--out-dy", (dy / d * pull).toFixed(1) + "px");
      f.style.setProperty("--dyn-delay", ((outList.length - 1 - i) * 60) + "ms");
      f.classList.add("dyn-out");
    });
    var hideMs = outList.length ? (outList.length - 1) * 60 + 240 : 0;

    setTimeout(function () {
      if (token !== funcToken) return;
      /* 切换后不再显示的字段收束完成，置 hidden；常驻控件与保留字段不动 */
      fields.forEach(function (f) { f.hidden = show.indexOf(f.id) === -1; });
      startShow(token);
    }, hideMs);

    /* 进场：全部控件从下往上（开始按钮 → BPM/拍号行 → 保留字段自下而上）
       从面板底边中点起飞（级联间隔 90ms），惯性过冲后落定；
       先完成最终布局再测量，确保起飞点与最终位置一致 */
    function startShow(tok) {
      var station = btn.closest(".station");
      var sr = station.getBoundingClientRect();
      var lx = sr.left + sr.width / 2;
      var ly = sr.bottom;
      var inList = persistent.slice().reverse().concat(
        fields.filter(function (f) { return show.indexOf(f.id) !== -1; }).slice().reverse()
      );
      inList.forEach(function (f, i) {
        clearDynField(f);
        var r = f.getBoundingClientRect();
        var fx = r.left + r.width / 2;
        var fy = r.top + r.height / 2;
        f.style.setProperty("--fly-dx", (fx - lx).toFixed(1) + "px");
        f.style.setProperty("--fly-dy", (fy - ly).toFixed(1) + "px");
        f.style.setProperty("--dyn-delay", (i * 60) + "ms");
        f.classList.add("dyn-in");
        setTimeout(function () {
          if (tok !== funcToken) return;
          clearDynField(f);
        }, i * 60 + 700);
      });
    }
  }

  function consoleLine(text, cls) {
    var body = $("#consoleBody");
    var div = document.createElement("div");
    div.className = "c-line " + (cls || "");
    div.textContent = text;
    body.appendChild(div);
    body.scrollTop = body.scrollHeight;
    return div;
  }

  function setStatus(text, cls) {
    var el = $("#runStatus");
    el.textContent = text;
    el.className = cls || "";
  }

  /* ---- 初始化 ---- */
  function init() {
    /* 功能分段 */
    UI.qsa(".fn", $("#funcSelector")).forEach(function (btn) {
      btn.addEventListener("click", function () { applyFunc(btn); });
    });

    /* 解析状态与 API 印章 */
    UI.getJSON("/api/settings").then(function (s) {
      var stamp = $("#stampApi");
      if (s.api_key) {
        stamp.textContent = "API: READY";
        stamp.classList.add("ok");
      } else {
        stamp.textContent = "API: NONE";
        stamp.classList.remove("ok");
      }
    }).catch(function () {});

    /* 上传解析 */
    var input = $("#midiFileInput");
    var dz = $("#dropzone");
    var parseBtn = $("#parseBtn");
    var parseBusy = false;

    function setParseBusy(b) {
      parseBusy = b;
      if (parseBtn) parseBtn.disabled = b;
      if (dz) dz.classList.toggle("busy", b);
    }

    function handleFile(file) {
      if (!file || parseBusy) return;
      setParseBusy(true);
      var form = new FormData();
      form.append("file", file);
      UI.toast("正在解析 " + file.name + " …", "");
      fetch("/api/parse", { method: "POST", body: form })
        .then(function (r) {
          if (!r.ok) return r.json().then(function (j) { throw new Error(j.detail || "解析失败"); });
          return r.json();
        })
        .then(function (data) {
          noteTable = data.note_table;
          $("#parseStatus").textContent = data.status;
          $("#parseStatus").className = "ok";
          $("#dzSub").textContent = file.name;
          UI.toast(data.status, "ok");
        })
        .catch(function (e) {
          $("#parseStatus").textContent = "✗ " + e.message;
          $("#parseStatus").className = "err";
          UI.toast("✗ " + e.message, "err");
        })
        .finally(function () { setParseBusy(false); });
    }

    dz.addEventListener("click", function () { input.click(); });
    input.addEventListener("change", function () {
      if (input.files && input.files[0]) handleFile(input.files[0]);
      input.value = "";
    });
    var dragCounter = 0;
    dz.addEventListener("dragenter", function (e) {
      e.preventDefault();
      dragCounter++;
      dz.classList.add("dragover");
    });
    dz.addEventListener("dragover", function (e) {
      e.preventDefault();
      dz.classList.add("dragover");
    });
    dz.addEventListener("dragleave", function (e) {
      e.preventDefault();
      dragCounter--;
      if (dragCounter <= 0) {
        dragCounter = 0;
        dz.classList.remove("dragover");
      }
    });
    dz.addEventListener("drop", function (e) {
      e.preventDefault();
      dragCounter = 0;
      dz.classList.remove("dragover");
      var f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
      if (f) handleFile(f);
    });
    $("#parseBtn").addEventListener("click", function () { input.click(); });

    /* 开始任务 */
    $("#startBtn").addEventListener("click", runTask);
  }

  function runTask() {
    if (busy) return;

    var requiresMidi = currentFunc !== "其他要求";
    if (requiresMidi && !noteTable.length) {
      UI.toast("⚠ 请先解析 MIDI 文件", "warn");
      return;
    }

    var body = {
      func: currentFunc,
      note_table: noteTable,
      bpm: $("#bpmInput").value,
      time_signature: $("#tsInput").value,
      lyrics: $("#lyricsBox").value,
      original_language: $("#origLang").value,
      target_language: $("#targetLang").value,
      note_output: $("#noteOutput").checked,
      requirements: $("#reqBox").value,
    };

    busy = true;
    setStatus("RUNNING", "");
    $("#startBtn").disabled = true;
    $("#downloadLink").hidden = true;
    $("#consoleBody").innerHTML = "";
    consoleLine("> 任务启动: " + currentFunc, "dim");

    UI.ssePost("/api/run", body, function (ev) {
      if (ev.type === "progress") {
        consoleLine("> " + ev.desc, "dim");
      } else if (ev.type === "warn") {
        consoleLine("⚠ " + ev.message, "warn");
      } else if (ev.type === "error") {
        consoleLine("✗ " + ev.message, "err");
        setStatus("FAILED", "err");
        finish();
      } else if (ev.type === "done") {
        if (ev.status) {
          var okLine = ev.status.indexOf("✓") === 0;
          consoleLine(ev.status, okLine ? "ok" : "warn");
        }
        if (ev.result) {
          var det = document.createElement("details");
          det.innerHTML =
            "<summary style='font-family:monospace;font-size:11px;color:var(--color-primary);cursor:pointer'>" +
            "▼ 完整结果（" + ev.result.length + " 字符）</summary>" +
            "<pre style='white-space:pre-wrap;word-break:break-all;font-size:11px'>" + UI.esc(ev.result) + "</pre>";
          $("#consoleBody").appendChild(det);
        }
        if (ev.download_url) {
          var link = $("#downloadLink");
          link.href = ev.download_url;
          link.hidden = false;
        }
        setStatus("DONE", "ok");
        consoleLine("> _", "dim");
        finish();
      }
    }).catch(function (e) {
      consoleLine("✗ " + e.message, "err");
      setStatus("FAILED", "err");
      finish();
    });
  }

  function finish() {
    busy = false;
    $("#startBtn").disabled = false;
  }

  document.addEventListener("DOMContentLoaded", init);
})();
