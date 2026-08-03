/* 多轮对话逻辑（档案库 + 工作台） */
(function () {
  "use strict";
  var UI = window.UI;
  var $ = UI.qs;

  var currentProjectId = null;   /* 当前打开的项目 */
  var currentName = "";
  var files = [];                /* 当前项目文件列表 */
  var chatBusy = false;
  var draftDirty = false;

  /* ═══════════ 档案库 ═══════════ */

  function renderProjects(projects) {
    var grid = $("#projectGrid");
    grid.innerHTML = "";
    $("#countStamp").textContent = "ARCHIVE: " + projects.length;
    projects.forEach(function (p) {
      var card = UI.projectCard(p);
      card.addEventListener("click", function (e) {
        if (e.target.closest("button")) return;
        openProject(p.id);
      });
      card.querySelector(".action-open").addEventListener("click", function () {
        openProject(p.id);
      });
      card.querySelector(".action-rename").addEventListener("click", function () {
        showModal("重命名档案", p.name, function (name) {
          return UI.putJSON("/api/projects/" + p.id, { name: name }).then(reloadProjects);
        });
      });
      card.querySelector(".action-copy").addEventListener("click", function () {
        UI.postJSON("/api/projects/" + p.id + "/copy", {}).then(reloadProjects)
          .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
      });
      card.querySelector(".action-delete").addEventListener("click", function () {
        if (!confirm("确定删除项目「" + (p.name || "") + "」？此操作不可恢复。")) return;
        UI.delJSON("/api/projects/" + p.id).then(reloadProjects)
          .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
      });
      grid.appendChild(card);
    });
  }

  function reloadProjects() {
    return UI.getJSON("/api/projects").then(renderProjects);
  }

  function renderSearch(rows, ids) {
    var grid = $("#projectGrid");
    grid.innerHTML = "";
    $("#countStamp").textContent = "SEARCH: " + rows.length;
    rows.forEach(function (row, i) {
      var card = document.createElement("article");
      card.className = "card draft brackets proj-card";
      card.dataset.id = ids[i];
      card.innerHTML =
        '<div class="proj-name">' + UI.esc(row[0]) + "</div>" +
        '<div class="proj-meta"><span class="stamp">匹配内容</span></div>' +
        '<div style="font-size:12px;color:var(--color-ink-dim);line-height:1.7">' + UI.esc(row[1]) + "</div>" +
        '<div class="proj-actions"><button class="btn btn-primary btn-sm action-open">打开</button></div>';
      card.querySelector(".action-open").addEventListener("click", function () {
        openProject(ids[i]);
      });
      card.addEventListener("click", function (e) {
        if (e.target.closest("button")) return;
        openProject(ids[i]);
      });
      grid.appendChild(card);
    });
  }

  /* ═══════════ 视图切换与草稿 ═══════════ */

  var isTransitioning = false;
  var springToken = 0;

  function saveDraft() {
    var input = $("#msgInput");
    if (!currentProjectId || !input) return;
    draftDirty = false;
    return UI.postJSON("/api/projects/" + currentProjectId + "/draft", { text: input.value })
      .catch(function () {});
  }

  function startViewTransitionSafe(callback) {
    if (document.startViewTransition) {
      try { return document.startViewTransition(callback); } catch (e) {}
    }
    callback();
    return null;
  }

  function springEls() {
    return UI.qsa(".sidebar > *", $("#studioView")).concat(
      UI.qsa(".console-wrap > *", $("#studioView"))
    );
  }

  /* 工作台内部元素错峰弹入（一次性，动画结束移除类，SSE 重渲染不重播） */
  function springIn() {
    var token = ++springToken;
    var studio = $("#studioView");
    var els = springEls();
    els.forEach(function (el, i) {
      el.style.setProperty("--spring-i", i);
      el.classList.add("spring-el");
    });
    studio.classList.add("enter");
    setTimeout(function () {
      if (springToken !== token) return;
      els.forEach(function (el) { el.classList.remove("spring-el"); });
      studio.classList.remove("enter");
    }, 950);
  }

  /* 返回档案库前：内部元素快速收拢 */
  function springOut() {
    springToken++; /* 使挂起的 enter 清理定时器失效 */
    var studio = $("#studioView");
    var els = springEls();
    els.forEach(function (el) { el.classList.add("spring-el"); });
    studio.classList.add("leaving");
  }

  function clearSprings() {
    springToken++;
    var studio = $("#studioView");
    studio.classList.remove("enter", "leaving");
    springEls().forEach(function (el) { el.classList.remove("spring-el"); });
  }

  function openProject(projectId) {
    if (isTransitioning) return;
    if (currentProjectId && currentProjectId !== projectId) saveDraft();
    UI.getJSON("/api/projects/" + projectId).then(function (payload) {
      currentProjectId = projectId;
      currentName = payload.meta.name || "未命名";
      files = payload.midi_files || [];
      draftDirty = false;

      $("#studioProjectName").textContent = "[PROJECT: " + currentName + "]";
      $("#modelStamp").textContent = "MODEL: " + (payload.settings.model || "--");
      $("#kvModel").textContent = payload.settings.model || "--";
      $("#kvEffort").textContent = payload.settings.reasoning_effort || "--";

      renderFiles();
      renderMessages(payload.display_messages || []);
      renderWorkspace(payload.workspace || { bound: false, path: "" });

      var input = $("#msgInput");
      input.value = payload.draft || "";
      input.disabled = false;
      $("#sendBtn").disabled = false;
      chatBusy = false;
      $("#newMidiLink").hidden = true;

      /* 形态动画：项目卡片 → 整面板展开 */
      var studio = $("#studioView");
      var archive = $("#archiveView");
      var card = UI.qs('.proj-card[data-id="' + projectId + '"]');
      isTransitioning = true;
      /* 安全兜底：若 VT 异常卡住，1.5 秒后强制释放（避免按钮永久不可点） */
      var morphGuard = setTimeout(function () { isTransitioning = false; }, 1500);

      if (card) card.style.viewTransitionName = "project-panel";
      var vt = startViewTransitionSafe(function () {
        archive.hidden = true;
        studio.hidden = false;
        if (card) card.style.viewTransitionName = "";
        studio.style.viewTransitionName = "project-panel";
        /* 切换一发生就立即解锁：按钮、链接可立即响应点击，
           后续的弹簧错峰动画是纯视觉装饰，不影响交互 */
        clearTimeout(morphGuard);
        isTransitioning = false;
      });

      function finishOpen() {
        studio.style.viewTransitionName = "";
        clearSprings();
        springIn();
        setTimeout(function () { input.focus(); }, 60);
        window.scrollTo(0, 0);
      }
      if (vt && vt.finished) {
        vt.finished.then(finishOpen, finishOpen);
      } else {
        finishOpen();
      }
    }).catch(function (e) {
      UI.toast("✗ 打开项目失败: " + e.message, "err");
      isTransitioning = false;
    });
  }

  function backToArchive() {
    /* 返回按钮始终可点：不检查 isTransitioning，避免动画卡住时无法返回 */
    if (currentProjectId) saveDraft();
    var pid = currentProjectId;
    currentProjectId = null;
    isTransitioning = true;
    var morphGuard = setTimeout(function () { isTransitioning = false; }, 1500);

    var studio = $("#studioView");
    var archive = $("#archiveView");

    /* 1) 内部元素快速收拢（吸气） */
    springOut();

    /* 2) 预取项目列表并渲染网格（archive 仍隐藏，卡片落点已就位） */
    UI.getJSON("/api/projects").then(function (projects) {
      renderProjects(projects);
      var card = pid ? UI.qs('.proj-card[data-id="' + pid + '"]') : null;

      setTimeout(function () {
        /* 形态动画：工作台 → 缩回卡片原位 */
        if (card) card.style.viewTransitionName = "project-panel";
        studio.style.viewTransitionName = "project-panel";
        var vt = startViewTransitionSafe(function () {
          studio.hidden = true;
          archive.hidden = false;
          studio.style.viewTransitionName = "";
          clearTimeout(morphGuard);
          isTransitioning = false;
        });
        function finishBack() {
          if (card) {
            card.style.viewTransitionName = "";
            /* 目标卡片惯性回弹落地（与形态动画无缝衔接） */
            card.classList.add("landed");
            setTimeout(function () { card.classList.remove("landed"); }, 600);
          }
          clearSprings();
          isTransitioning = false;
          window.scrollTo(0, 0);
        }
        if (vt && vt.finished) {
          vt.finished.then(finishBack, finishBack);
        } else {
          finishBack();
        }
      }, 220);
    }).catch(function (e) {
      UI.toast("✗ 返回档案库失败: " + e.message, "err");
      clearSprings();
      studio.hidden = true;
      archive.hidden = false;
      isTransitioning = false;
      reloadProjects();
    });
  }

  /* ═══════════ 文件管理 ═══════════ */

  function renderFiles() {
    var list = $("#fileList");
    list.innerHTML = "";
    $("#fileStamp").textContent = files.length + " FILES";
    if (!files.length) {
      list.innerHTML = '<div style="padding:14px;font-size:12px;color:var(--color-ink-faint);text-align:center">（暂无文件）</div>';
      return;
    }
    files.forEach(function (f) {
      var label = document.createElement("label");
      label.className = "file-item";
      label.innerHTML =
        '<input type="checkbox" class="file-check" value="' + UI.esc(f.name) + '">' +
        '<span class="file-name" title="' + UI.esc(f.name) + '">' + UI.esc(f.name) + "</span>" +
        '<span class="file-size">' + UI.fmtSize(f.size) + "</span>";
      list.appendChild(label);
    });
  }

  function selectedNames() {
    return UI.qsa(".file-check:checked", $("#fileList")).map(function (c) { return c.value; });
  }

  function applyFiles(newFiles) {
    files = newFiles || [];
    renderFiles();
  }

  /* ═══════════ 工作区 ═══════════ */

  function renderWorkspace(ws) {
    var bound = !!(ws && ws.bound);
    $("#wsBindBox").hidden = bound;
    $("#wsBoundBox").hidden = !bound;
    var state = $("#wsStateStamp");
    state.textContent = bound ? "BOUND" : "UNBOUND";
    state.className = bound ? "stamp ok" : "stamp";
    var bar = $("#wsStamp");
    bar.textContent = "WS: " + (bound ? "BOUND" : "--");
    bar.className = bound ? "stamp ok" : "stamp warn";
    if (bound) {
      $("#wsPathDisplay").textContent = ws.path || "--";
      $("#wsPathDisplay").title = ws.path || "";
    }
  }

  /* ═══════════ 对话渲染 ═══════════ */

  function renderMessages(messages) {
    var chat = $("#chat");
    chat.innerHTML = "";
    (messages || []).forEach(function (m) {
      chat.appendChild(renderMessage(m));
    });
    chat.scrollTop = chat.scrollHeight;
  }

  function renderMessage(m) {
    var content = m.content || "";
    if (content.indexOf("<details>") === 0) {
      var tool = document.createElement("div");
      tool.className = "tool-call";
      tool.innerHTML = content; /* 服务端生成的折叠标签，受信任 */
      return tool;
    }
    if (m.role === "user") {
      var u = document.createElement("div");
      u.className = "msg user";
      u.innerHTML = '<div class="msg-label">你 · You</div><p>' + UI.esc(content).replace(/\n/g, "<br>") + "</p>";
      return u;
    }
    var a = document.createElement("div");
    a.className = "msg ai";
    a.innerHTML = '<div class="msg-label">AI · 乐理专家</div>' + UI.md(content);
    return a;
  }

  function appendSysLine(text) {
    var chat = $("#chat");
    var line = document.createElement("div");
    line.className = "sys-line";
    line.textContent = text;
    chat.appendChild(line);
    chat.scrollTop = chat.scrollHeight;
  }

  /* ═══════════ 发送消息（SSE） ═══════════ */

  function sendMessage() {
    if (chatBusy || !currentProjectId) return;
    var input = $("#msgInput");
    var message = input.value.trim();
    if (!message) return;

    chatBusy = true;
    $("#sendBtn").disabled = true;
    input.disabled = true;

    var messages = [];
    messages.push({ role: "user", content: message });
    appendSysLine("[SYS] 正在发送…");

    UI.ssePost("/api/chat", { project_id: currentProjectId, message: message }, function (ev) {
      if (ev.type === "chat") {
        messages = ev.messages || [];
        renderMessages(messages);
      } else if (ev.type === "files") {
        applyFiles(ev.files);
      } else if (ev.type === "download") {
        var link = $("#newMidiLink");
        link.href = ev.url;
        link.hidden = false;
      } else if (ev.type === "error") {
        appendSysLine("⚠ " + ev.message);
      } else if (ev.type === "done") {
        chatDone();
      }
    }).catch(function (e) {
      appendSysLine("✗ " + e.message);
      chatDone();
    });
  }

  function chatDone() {
    chatBusy = false;
    $("#sendBtn").disabled = false;
    var input = $("#msgInput");
    input.disabled = false;
    input.value = "";          /* 消息已消费，清空输入框 */
    draftDirty = false;
    input.focus();
  }

  /* ═══════════ 弹窗 ═══════════ */

  var modalAction = null;

  function showModal(title, value, action) {
    $("#modalTitle").textContent = title;
    $("#modalInput").value = value || "";
    $("#modalOverlay").hidden = false;
    modalAction = action;
    setTimeout(function () { $("#modalInput").focus(); }, 50);
  }

  function hideModal() {
    $("#modalOverlay").hidden = true;
    modalAction = null;
  }

  /* ═══════════ 初始化 ═══════════ */

  function init() {
    reloadProjects().catch(function (e) {
      UI.toast("✗ 加载项目列表失败: " + e.message, "err");
    });

    /* 搜索 */
    var searchTimer = null;
    $("#searchInput").addEventListener("input", function () {
      clearTimeout(searchTimer);
      var q = this.value.trim();
      searchTimer = setTimeout(function () {
        if (!q) { reloadProjects(); return; }
        UI.getJSON("/api/projects/search?q=" + encodeURIComponent(q)).then(function (data) {
          if (!data.rows.length) {
            renderSearch([["未找到匹配内容", ""]], ["__none__"]);
            return;
          }
          renderSearch(data.rows, data.ids);
        }).catch(function (e) { UI.toast("✗ " + e.message, "err"); });
      }, 250);
    });

    /* 新建档案 */
    $("#newProjectBtn").addEventListener("click", function () {
      showModal("＋ 新建档案", "", function (name) {
        return UI.postJSON("/api/projects", { name: name }).then(function (payload) {
          if (isTransitioning) return;
          currentProjectId = null; /* 防止保存旧草稿 */
          currentProjectId = payload.meta.id;
          currentName = payload.meta.name || "未命名";
          files = payload.midi_files || [];
          $("#studioProjectName").textContent = "[PROJECT: " + currentName + "]";
          $("#modelStamp").textContent = "MODEL: " + (payload.settings.model || "--");
          $("#kvModel").textContent = payload.settings.model || "--";
          $("#kvEffort").textContent = payload.settings.reasoning_effort || "--";
          renderFiles();
          renderMessages(payload.display_messages || []);
          renderWorkspace(payload.workspace || { bound: false, path: "" });
          $("#msgInput").value = payload.draft || "";
          $("#sendBtn").disabled = false;
          $("#newMidiLink").hidden = true;

          /* 无卡片来源：默认交叉过渡 + 面板内容弹入 */
          isTransitioning = true;
          var vt = startViewTransitionSafe(function () {
            $("#archiveView").hidden = true;
            $("#studioView").hidden = false;
          });
          function fin() {
            isTransitioning = false;
            clearSprings();
            springIn();
            setTimeout(function () { $("#msgInput").focus(); }, 60);
            window.scrollTo(0, 0);
          }
          if (vt && vt.finished) vt.finished.then(fin, fin);
          else fin();
        });
      });
    });

    $("#modalOk").addEventListener("click", function () {
      if (!modalAction) return;
      var name = $("#modalInput").value.trim();
      if (!name) { UI.toast("名称不能为空", "warn"); return; }
      modalAction(name).then(function () {
        hideModal();
        reloadProjects();
      }).catch(function (e) {
        UI.toast("✗ " + e.message, "err");
      });
    });
    $("#modalCancel").addEventListener("click", hideModal);
    $("#modalOverlay").addEventListener("click", function (e) {
      if (e.target === this) hideModal();
    });
    $("#modalInput").addEventListener("keydown", function (e) {
      if (e.key === "Enter") $("#modalOk").click();
    });

    /* 返回档案库 */
    $("#backBtn").addEventListener("click", backToArchive);

    /* 文件操作 */
    $("#uploadBtn").addEventListener("click", function () { $("#uploadInput").click(); });
    $("#uploadInput").addEventListener("change", function () {
      var input = this;
      if (!input.files.length) return;
      var form = new FormData();
      Array.prototype.forEach.call(input.files, function (f) { form.append("files", f); });
      UI.toast("正在上传 " + input.files.length + " 个文件…", "");
      fetch("/api/projects/" + currentProjectId + "/files", { method: "POST", body: form })
        .then(function (r) {
          if (!r.ok) return r.json().then(function (j) { throw new Error(j.detail || "上传失败"); });
          return r.json();
        })
        .then(function (data) {
          applyFiles(data.files);
          UI.toast("✓ 已上传 " + data.added + " 个文件", "ok");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); })
        .finally(function () { input.value = ""; });
    });

    $("#deleteBtn").addEventListener("click", function () {
      var names = selectedNames();
      if (!names.length) { UI.toast("请先勾选要删除的文件", "warn"); return; }
      UI.delJSON("/api/projects/" + currentProjectId + "/files", { names: names })
        .then(function (data) { applyFiles(data.files); })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });

    $("#downloadBtn").addEventListener("click", function () {
      var names = selectedNames();
      var url = "/api/projects/" + currentProjectId + "/download" +
        (names.length ? "?names=" + encodeURIComponent(names.join(",")) : "");
      window.location.href = url;
    });

    $("#undoBtn").addEventListener("click", function () {
      UI.postJSON("/api/projects/" + currentProjectId + "/undo").then(function (data) {
        applyFiles(data.files);
        UI.toast("↩ 已撤销上一步操作", "");
      }).catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });

    /* 工作区 */
    $("#pickFolderBtn").addEventListener("click", function () {
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/pick-folder", {})
        .then(function (data) {
          if (data.path) $("#wsPathInput").value = data.path;
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });
    $("#bindBtn").addEventListener("click", function () {
      var path = $("#wsPathInput").value.trim();
      if (!path) { UI.toast("请输入工作区目录路径", "warn"); return; }
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/bind", { path: path })
        .then(function (data) {
          applyFiles(data.midi_files);
          renderWorkspace({ bound: true, path: data.path });
          $("#wsPathInput").value = "";
          if (data.renamed && data.renamed.length) {
            UI.toast("已绑定工作区。重名文件已加序号:\n" + data.renamed.map(function (r) { return r.join(" → "); }).join("\n"), "warn");
          } else {
            UI.toast("✓ 已绑定工作区", "ok");
          }
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });
    $("#unbindBtn").addEventListener("click", function () {
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/unbind")
        .then(function (data) {
          applyFiles(data.midi_files);
          renderWorkspace({ bound: false, path: "" });
          UI.toast("已解绑工作区", "");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });
    $("#refreshWsBtn").addEventListener("click", function () {
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/refresh")
        .then(function (data) {
          applyFiles(data.midi_files);
          UI.toast("✓ 已刷新工作区文件", "ok");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });

    /* 对话 */
    $("#sendBtn").addEventListener("click", sendMessage);
    $("#msgInput").addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        sendMessage();
      }
    });
    $("#clearBtn").addEventListener("click", function () {
      if (!confirm("确定清空当前项目的全部对话？")) return;
      UI.postJSON("/api/projects/" + currentProjectId + "/clear").then(function () {
        renderMessages([]);
        UI.toast("✓ 对话已清空", "ok");
      }).catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });

    /* 离开页面时保存草稿 */
    window.addEventListener("beforeunload", function () {
      if (currentProjectId && draftDirty) {
        var input = $("#msgInput");
        navigator.sendBeacon
          ? navigator.sendBeacon("/api/projects/" + currentProjectId + "/draft", JSON.stringify({ text: input.value }))
          : saveDraft();
      }
    });
    $("#msgInput").addEventListener("input", function () { draftDirty = true; });
  }

  document.addEventListener("DOMContentLoaded", init);
})();
