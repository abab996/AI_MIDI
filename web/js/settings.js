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
    return {
      api_key: $("#apiKey").value,
      base_url: $("#baseUrl").value,
      api_path: $("#apiPath").value,
      model: $("#model").value,
      max_tokens: $("#maxTokens").value === "" ? null : Number($("#maxTokens").value),
      max_completion_tokens: $("#maxCompletion").value === "" ? null : Number($("#maxCompletion").value),
      reasoning_effort: effort,
      thinking_enabled: $("#thinkingEnabled").checked,
    };
  }

  function init() {
    /* 加载设置（每次进入页面都从服务器读取，保证内容最新） */
    UI.getJSON("/api/settings").then(fillForm).catch(function (e) {
      UI.toast("✗ 加载设置失败: " + e.message, "err");
    });

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
      var s = collect();
      var status = $("#modelStatus");
      status.textContent = "正在获取模型列表…";
      status.className = "dim";
      fetch(
        "/api/models?api_key=" + encodeURIComponent(s.api_key) +
        "&base_url=" + encodeURIComponent(s.base_url) +
        "&api_path=" + encodeURIComponent(s.api_path)
      ).then(function (r) { return r.json(); }).then(function (data) {
        status.textContent = data.message;
        status.className = data.models.length ? "ok" : "err";
        /* 同步重建自定义下拉菜单 */
        renderModelMenu(data.models);
        if (data.models.length) {
          if (!s.model) $("#model").value = data.models[0];
          UI.toast("✓ 已获取 " + data.models.length + " 个模型", "ok");
        } else {
          UI.toast("✗ " + data.message, "err");
        }
      }).catch(function (e) {
        status.textContent = "✗ 获取失败: " + e.message;
        status.className = "err";
      });
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

    /* 恢复默认 */
    $("#resetBtn").addEventListener("click", function () {
      fillForm(DEFAULTS);
    });
  }

  document.addEventListener("DOMContentLoaded", init);
})();
