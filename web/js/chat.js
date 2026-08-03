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

  var reducedMotion = typeof window.matchMedia === "function"
    && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* 按"距原点的方向"依次浮现（涟漪）：
     originX/originY = 涟漪中心（通常是点击的卡片中心）；
     无原点时（新建项目）传 null，落到面板左上角。
     每个元素从"背离卡片"的径向方向浮现：--spring-tx/ty =
     径向单位向量 × 12px；延迟按距离 0.5ms/px（近的先浮现） */
  function springIn(originX, originY) {
    var token = ++springToken;
    var studio = $("#studioView");
    var els = springEls();
    var maxDelay = 0;
    if (els.length) {
      var ox = (originX == null) ? 0 : originX;
      var oy = (originY == null) ? 0 : originY;
      var push = 12;
      els.forEach(function (el) {
        var r = el.getBoundingClientRect();
        var ex = r.left + r.width / 2;
        var ey = r.top + r.height / 2;
        var dx = ex - ox;
        var dy = ey - oy;
        var dist = Math.hypot(dx, dy);
        /* 元素恰在原点时兜底为向下方向 */
        if (dist < 1) { dx = 0; dy = 1; dist = 1; }
        var dirX = dx / dist;
        var dirY = dy / dist;
        /* reduced-motion：忽略距离延迟，避免内容长时间不可见 */
        var delay = reducedMotion ? 0 : Math.round(dist * 0.5);
        if (delay > maxDelay) maxDelay = delay;
        el.style.setProperty("--spring-tx", (dirX * push).toFixed(1) + "px");
        el.style.setProperty("--spring-ty", (dirY * push).toFixed(1) + "px");
        el.style.setProperty("--spring-delay", delay + "ms");
        el.classList.add("spring-el", "pre-reveal");
      });
    }
    studio.classList.add("enter");
    /* 涟漪启动一帧后移除 pre-reveal，动画从 opacity 0 接管（防止内容卡透明） */
    requestAnimationFrame(function () {
      if (springToken !== token) return;
      els.forEach(function (el) { el.classList.remove("pre-reveal"); });
    });
    setTimeout(function () {
      if (springToken !== token) return;
      els.forEach(function (el) {
        el.classList.remove("spring-el");
        el.style.removeProperty("--spring-delay");
        el.style.removeProperty("--spring-tx");
        el.style.removeProperty("--spring-ty");
      });
      studio.classList.remove("enter");
    }, maxDelay + 550);
  }

  /* 返回档案库前：内部元素朝卡片方向"吸气"汇聚——
     --spring-ltx/lty 指向卡片（径向单位向量反向 × 8px）；
     延迟 --spring-leave-delay 按距离反向：远的先收（delay 小）、
     近的后收（delay 大），形成向卡片坍缩的视觉 */
  function springOut(targetX, targetY) {
    springToken++;
    var studio = $("#studioView");
    var els = springEls();
    if (els.length && targetX != null && targetY != null) {
      var maxDelay = 0;
      var maxDist = 0;
      els.forEach(function (el) {
        var r = el.getBoundingClientRect();
        var dx = (r.left + r.width / 2) - targetX;
        var dy = (r.top + r.height / 2) - targetY;
        var dist = Math.hypot(dx, dy);
        if (dist > maxDist) maxDist = dist;
      });
      els.forEach(function (el) {
        var r = el.getBoundingClientRect();
        var ex = r.left + r.width / 2;
        var ey = r.top + r.height / 2;
        var dx = ex - targetX;
        var dy = ey - targetY;
        var dist = Math.hypot(dx, dy);
        /* 指向卡片的单位向量 */
        var pull = 8;
        var dirX = dist < 1 ? 0 : dx / dist;
        var dirY = dist < 1 ? 0 : dy / dist;
        /* 远的先收：delay = (1 - dist/maxDist) × 120ms */
        var delay = reducedMotion ? 0 : Math.round((1 - (maxDist ? dist / maxDist : 0)) * 120);
        if (delay > maxDelay) maxDelay = delay;
        el.style.setProperty("--spring-ltx", (-dirX * pull).toFixed(1) + "px");
        el.style.setProperty("--spring-lty", (-dirY * pull).toFixed(1) + "px");
        el.style.setProperty("--spring-leave-delay", delay + "ms");
        el.classList.add("spring-el");
      });
      /* 留出收拢完成的时间再清理 */
      setTimeout(function () {
        springToken++;
        studio.classList.remove("leaving", "enter");
        els.forEach(function (el) {
          el.classList.remove("spring-el");
          el.style.removeProperty("--spring-leave-delay");
          el.style.removeProperty("--spring-ltx");
          el.style.removeProperty("--spring-lty");
        });
      }, maxDelay + 260);
    } else {
      studio.classList.add("leaving");
    }
  }

  function clearSprings() {
    springToken++;
    var studio = $("#studioView");
    studio.classList.remove("enter", "leaving");
    springEls().forEach(function (el) {
      el.classList.remove("spring-el", "pre-reveal");
      el.style.removeProperty("--spring-delay");
      el.style.removeProperty("--spring-tx");
      el.style.removeProperty("--spring-ty");
      el.style.removeProperty("--spring-leave-delay");
      el.style.removeProperty("--spring-ltx");
      el.style.removeProperty("--spring-lty");
    });
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

      /* 打开流程：确保 data-vt-flow 不为 "back"（否则 card-arrive 会套到
         新快照工作台面板上，导致面板额外缩放与旧卡片内容重叠） */
      delete document.documentElement.dataset.vtFlow;

      /* 捕获卡片中心（涟漪原点）——必须在 archive 可见时取 rect，
         隐藏后 getBoundingClientRect 返回 0；保存为绝对值（不随滚动变化） */
      var origin = null;
      if (card) {
        var cr = card.getBoundingClientRect();
        origin = { x: cr.left + cr.width / 2, y: cr.top + cr.height / 2 };
      }

      if (card) card.style.viewTransitionName = "project-panel";
      var vt = startViewTransitionSafe(function () {
        archive.hidden = true;
        studio.hidden = false;
        if (card) card.style.viewTransitionName = "";
        studio.style.viewTransitionName = "project-panel";
        /* VT 快照前隐藏内容——防止"先出现→又重影浮现"——
           springIn 启动一帧后会自动移除该 class */
        if (!reducedMotion) {
          springEls().forEach(function (el) { el.classList.add("pre-reveal"); });
        }
        clearTimeout(morphGuard);
        isTransitioning = false;
      });

      /* spring-in 与 VT 形态动画在时间上重叠（VT 走到中段时 springIn 已开始），
         避免"VT 结束→停顿→springIn 开始"的换场断裂 */
      var springStarted = false;
      function startSpringOnce() {
        if (springStarted) return;
        springStarted = true;
        springIn(origin ? origin.x : null, origin ? origin.y : null);
      }
      setTimeout(startSpringOnce, 200);

      function finishOpen() {
        studio.style.viewTransitionName = "";
        /* 若 VT 未触发（如不支持或异常），回退为直接启动 spring-in */
        startSpringOnce();
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

    UI.getJSON("/api/projects").then(function (projects) {
      renderProjects(projects);
      var card = pid ? UI.qs('.proj-card[data-id="' + pid + '"]') : null;

      /* 关键：archive 隐藏时 getBoundingClientRect 返回 0。
         同步任务内短暂显示 archive 以触发布局并测量卡片真实位置，
         再立刻隐藏——浏览器未在两次赋值间渲染，无闪烁。
         studio 仍可见（springOut 即将作用其上） */
      if (card) {
        archive.hidden = false;
        var cr = card.getBoundingClientRect();
        archive.hidden = true;
        var cx = cr.left + cr.width / 2;
        var cy = cr.top + cr.height / 2;
        var pr = studio.getBoundingClientRect();
        var px = pr.left + pr.width / 2;
        var py = pr.top + pr.height / 2;
        var dx = cx - px;
        var dy = cy - py;
        /* 限制最大飞回距离，避免极端布局下位移过大 */
        var dist = Math.hypot(dx, dy);
        var max = 130;
        if (dist > max) { dx = dx / dist * max; dy = dy / dist * max; }
        card.style.setProperty("--land-dx", dx.toFixed(1) + "px");
        card.style.setProperty("--land-dy", dy.toFixed(1) + "px");
      }

      /* 工作台朝卡片方向"吸气"汇聚（用真实卡片中心算距离延迟） */
      springOut(card ? cx : null, card ? cy : null);

      setTimeout(function () {
        if (!card) {
          /* 无目标卡片时直接收拢再开始 VT */
          studio.classList.add("leaving");
        }
        /* 形态动画：工作台 → 缩回卡片原位（弹弓轨迹 + 邻居波纹在撞击时刻触发） */
        if (card) card.style.viewTransitionName = "project-panel";
        studio.style.viewTransitionName = "project-panel";
        document.documentElement.dataset.vtFlow = "back";
        var vt = startViewTransitionSafe(function () {
          studio.hidden = true;
          archive.hidden = false;
          studio.style.viewTransitionName = "";
          clearTimeout(morphGuard);
          isTransitioning = false;
        });

        /* 关键时序：涟漪在卡片"首次撞击原位"时（VT 启动 ~250ms，
           即 card-arrive 40% 过冲点前后）触发，而不是等 vt.finished
           （0.5s+）——消除"卡片落地后干等"的空档 */
        if (card && !reducedMotion) {
          setTimeout(function () {
            /* VT 回调已执行（~16ms），archive 可见、卡片已布局 */
            applyNeighborRipple(card, archive);
          }, 250);
        }

        function finishBack() {
          if (card) {
            card.style.viewTransitionName = "";
            card.style.removeProperty("--land-dx");
            card.style.removeProperty("--land-dy");
          }
          clearSprings();
          delete document.documentElement.dataset.vtFlow;
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
      delete document.documentElement.dataset.vtFlow;
      studio.hidden = true;
      archive.hidden = false;
      isTransitioning = false;
      reloadProjects();
    });
  }

  /* 邻居卡片波纹：落点卡片弹回瞬间，按"远离落点"方向推开其他卡片，
     距离越远延迟越大，形成向四周扩散的水波。
     在 VT 启动 ~250ms（卡片首次撞击原位）触发，与卡片自身弹弓
     回弹同步进行——无"落地后干等"空档 */
  function applyNeighborRipple(landingCard, archive) {
    var lr = landingCard.getBoundingClientRect();
    var lx = lr.left + lr.width / 2;
    var ly = lr.top + lr.height / 2;
    var neighbors = [];
    UI.qsa(".proj-card", archive).forEach(function (n) {
      if (n === landingCard) return;
      var nr = n.getBoundingClientRect();
      var nx = nr.left + nr.width / 2;
      var ny = nr.top + nr.height / 2;
      var dx = nx - lx;
      var dy = ny - ly;
      var d = Math.hypot(dx, dy);
      if (d === 0) d = 1;
      /* 推开 12px 沿远离落点方向归一化 */
      var push = 12;
      n.style.setProperty("--push-x", (dx / d * push).toFixed(1) + "px");
      n.style.setProperty("--push-y", (dy / d * push).toFixed(1) + "px");
      /* 距离越远延迟越大：~0.35ms/px，波纹更快扩散（最远 ~400ms） */
      n.style.setProperty("--ripple-delay", Math.round(d * 0.35) + "ms");
      neighbors.push(n);
    });
    if (!neighbors.length) return;
    requestAnimationFrame(function () {
      neighbors.forEach(function (n) { n.classList.add("rippled"); });
    });
    var maxDelay = 0;
    neighbors.forEach(function (n) {
      var d = parseInt(n.style.getPropertyValue("--ripple-delay")) || 0;
      if (d > maxDelay) maxDelay = d;
    });
    /* 动画完成后清理 class 与变量 */
    setTimeout(function () {
      neighbors.forEach(function (n) {
        n.classList.remove("rippled");
        n.style.removeProperty("--push-x");
        n.style.removeProperty("--push-y");
        n.style.removeProperty("--ripple-delay");
      });
    }, maxDelay + 500);
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
          delete document.documentElement.dataset.vtFlow;
          var vt = startViewTransitionSafe(function () {
            $("#archiveView").hidden = true;
            $("#studioView").hidden = false;
            /* VT 快照前隐藏内容（无卡片时 origin 为面板左上角，涟漪从那里扩散） */
            if (!reducedMotion) {
              springEls().forEach(function (el) { el.classList.add("pre-reveal"); });
            }
          });
          function fin() {
            isTransitioning = false;
            /* 与打开流程同样的 200ms 重叠启动 + fallback */
            var started = false;
            function once() { if (!started) { started = true; springIn(null, null); } }
            setTimeout(once, 200);
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
