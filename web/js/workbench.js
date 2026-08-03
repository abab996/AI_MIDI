/* 工作台逻辑 */
(function () {
  "use strict";
  var UI = window.UI;
  var $ = UI.qs;
  var noteTable = [];
  var busy = false;
  var currentFunc = "配和弦";

  function funcFields(func) {
    var map = {
      "配和弦": "fReq",
      "翻译歌词": "fLyrics fLang",
      "设计转音": "fLyrics fReq",
      "其他要求": "fLyrics fNote fReq",
    };
    return (map[func] || "fReq").split(/\s+/);
  }

  function applyFunc(btn) {
    UI.qsa(".fn", $("#funcSelector")).forEach(function (b) {
      b.classList.toggle("active", b === btn);
    });
    currentFunc = btn.textContent.trim();
    var show = funcFields(currentFunc);
    UI.qsa(".dyn-field").forEach(function (f) {
      f.hidden = show.indexOf(f.id) === -1;
    });
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

    function handleFile(file) {
      if (!file) return;
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
        });
    }

    dz.addEventListener("click", function () { input.click(); });
    input.addEventListener("change", function () {
      if (input.files && input.files[0]) handleFile(input.files[0]);
      input.value = "";
    });
    ["dragover", "dragenter"].forEach(function (ev) {
      dz.addEventListener(ev, function (e) { e.preventDefault(); dz.classList.add("dragover"); });
    });
    ["dragleave", "drop"].forEach(function (ev) {
      dz.addEventListener(ev, function (e) { e.preventDefault(); dz.classList.remove("dragover"); });
    });
    dz.addEventListener("drop", function (e) {
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
        setStatus(ev.status ? "DONE" : "DONE", "ok");
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
