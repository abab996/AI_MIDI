/* 多轮对话逻辑（档案库 + 工作台） */
(function () {
  "use strict";
  var UI = window.UI;
  var $ = UI.qs;

  var currentProjectId = null;   /* 当前打开的项目 */
  var currentTaskId = null;      /* 当前任务（对话线程） */
  var currentName = "";
  var files = [];                /* 当前项目文件列表 */
  var chatBusy = false;
  var streamEnded = false;       /* 本次回复是否已收尾（chatDone 幂等标志） */
  var draftDirty = false;
  /* 滚动位置是否贴近底部（<40px）：流式输出只在贴近底部时自动滚到底，
     用户上翻阅读时不被每帧滚动拽回 */
  var nearBottom = true;

  /* 流式逐字渐显与思考块自动展开/收起状态 */
  var liveStreaming = false;             /* 是否处于实时流式输出（仅此时做逐字动画） */
  var streamTrack = { el: null, norm: "", parsed: null };  /* 流式消息元素 + 归一化内容 + 解析结构 */
  var thinkingTrack = { reasoning: "", det: null, lastGrowAt: 0 };  /* 推理增长跟踪（文本长度/块元素/末次增长时间） */
  var userToggledStream = false;         /* 用户手动操作过思考块后，自动逻辑让位 */
  var thinkUserCollapsed = false;        /* 用户手动收起了思考块（流式重建恢复时尊重，不撤销） */
  /* 播放中的折叠块过渡数：transitionstart/end/cancel 委托维护。
     替代 getAnimations() 检测——CSS 过渡在样式变化后的渲染更新阶段
     才启动，JS 查询存在时序窗口（WebView2 下可能漏检导致整表重建
     掐断正在播放的展开/收起动画，表现为思考块瞬间开合） */
  var detailsAnimBusy = 0;
  /* 收起动画进行中暂缓关闭的 details：Chromium 在 details 关闭瞬间
     （open 属性移除）抑制内部元素渲染，收起方向的过渡被冻结（表现为
     瞬间收起、后续展开也无动画）——收起时先撤销关闭保持内部可渲染，
     动画结束后（transitionend）再真正关闭 */
  var detailsClosing = null;
  var lastMessages = [];                 /* 最近一次 SSE chat 事件的完整消息列表（结束时重渲染用） */
  var chatEpoch = 0;                     /* 会话代数：延迟重建等异步回调据此判断是否过期 */
  var streamEpoch = -1;                  /* 当前流开始时的会话代数：chatDone 据此丢弃过期收尾 */
  var streamSysLines = [];               /* 流期间追加的系统行：最终整表重渲染后按序恢复（否则被重建抹掉） */
  var currentMessages = [];              /* 最近一次渲染的完整消息列表（消息操作按钮按索引取数） */
  var editingIndex = -1;                 /* 修改模式：正在编辑的用户消息索引（-1 = 未编辑） */
  var editMenuIndex = -1;                /* 修改菜单：当前显示「取消/撤回修改/撤回消息」的消息索引 */
  var editMenuToken = 0;                 /* 修改菜单请求令牌：连点时丢弃过期响应，防止菜单落错消息 */
  var inputBeforeEdit = "";              /* 进入修改模式前的输入框内容（撤回时还原） */

  /* ═══════════ 档案库 ═══════════ */

  function renderProjects(projects) {
    var grid = $("#projectGrid");
    grid.innerHTML = "";
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
          /* 不内嵌 reloadProjects：modalOk 对任意成功的 modalAction
             统一调用 reloadProjects，避免每次重命名发两个重复请求 */
          return UI.putJSON("/api/projects/" + p.id, { name: name });
        });
      });
      card.querySelector(".action-copy").addEventListener("click", function () {
        UI.postJSON("/api/projects/" + p.id + "/copy", {}).then(reloadProjects)
          .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
      });
      card.querySelector(".action-delete").addEventListener("click", function () {
        showConfirm("确定删除项目「" + (p.name || "") + "」？\n此操作不可恢复。", function () {
          UI.delJSON("/api/projects/" + p.id).then(reloadProjects)
            .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
        });
      });
      grid.appendChild(card);
    });
    /* 空档案库引导：无项目时展示占位提示（不渲染空网格） */
    if (!projects.length) {
      var empty = document.createElement("div");
      empty.className = "empty-hint";
      empty.textContent = "暂无档案，点击右上角「＋ 新建档案」开始创作";
      grid.appendChild(empty);
    }
    /* 任务联动：卡片重建后补上任务执行数徽标（不随轮询等待） */
    if (window.Tasks) Tasks.applyBadges();
  }

  function reloadProjects() {
    return UI.getJSON("/api/projects").then(renderProjects);
  }

  function renderSearch(rows, ids) {
    var grid = $("#projectGrid");
    grid.innerHTML = "";
    rows.forEach(function (row, i) {
      var card = document.createElement("article");
      card.className = "card draft brackets proj-card";
      card.dataset.id = ids[i];
      var id = ids[i];
      /* 无结果占位卡（id 为 __none__）：不渲染"打开"按钮、不绑定点击——
         否则点击会 openProject("__none__") → 404 报错 */
      var isPlaceholder = id === "__none__";
      card.innerHTML =
        '<div class="proj-name">' + UI.esc(row[0]) + "</div>" +
        '<div class="proj-meta"><span class="stamp">匹配内容</span></div>' +
        '<div style="font-size:12px;color:var(--color-ink-dim);line-height:1.7">' + UI.esc(row[1]) + "</div>" +
        (isPlaceholder ? "" : '<div class="proj-actions"><button class="btn btn-primary btn-sm action-open">打开</button></div>');
      if (!isPlaceholder) {
        card.querySelector(".action-open").addEventListener("click", function () {
          openProject(id);
        });
        card.addEventListener("click", function (e) {
          if (e.target.closest("button")) return;
          openProject(id);
        });
      }
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
      /* 受卡片惯性推出 24px，再弹簧回位（位移够大才看得出"飞出一段"） */
      var push = 24;
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
        /* reduced-motion：忽略距离延迟；普通模式延迟很小（涟漪扩散感），
           封顶 120ms——惯性动画必须立即响应，不能"等一会儿才播" */
        var delay = reducedMotion ? 0 : Math.min(Math.round(dist * 0.15), 120);
        if (delay > maxDelay) maxDelay = delay;
        el.style.setProperty("--spring-tx", (dirX * push).toFixed(1) + "px");
        el.style.setProperty("--spring-ty", (dirY * push).toFixed(1) + "px");
        el.style.setProperty("--spring-delay", delay + "ms");
        el.classList.add("spring-el");
      });
    }
    studio.classList.add("enter");
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

  /* 面板形态动画（DOM FLIP，替代 View Transition——VT 在 WebView2
     掉帧/阻塞）：transform-origin 左上角 + translate/scale 把面板对齐
     到卡片矩形，再过渡回自然态 = "卡片放大成面板"；反向 = 面板缩回卡片。
     纯合成层 transform，不经过 VT，流畅且保留形态动画效果 */
  function panelGrowFrom(studio, cardRect) {
    if (reducedMotion || !cardRect) return;
    var pr = studio.getBoundingClientRect();
    var sx = cardRect.width / pr.width;
    var sy = cardRect.height / pr.height;
    var tx = cardRect.left - pr.left;
    var ty = cardRect.top - pr.top;
    studio.style.transformOrigin = "top left";
    studio.style.transition = "none";
    studio.style.transform =
      "translate(" + tx.toFixed(1) + "px, " + ty.toFixed(1) + "px) scale(" +
      sx.toFixed(4) + ", " + sy.toFixed(4) + ")";
    void studio.offsetWidth;   /* 强制应用初始态后播放过渡 */
    studio.style.transition = "transform 0.4s cubic-bezier(0.22, 1, 0.36, 1)";
    studio.style.transform = "";
    setTimeout(function () {
      studio.style.transition = "";
      studio.style.transform = "";
      studio.style.transformOrigin = "";
    }, 500);
  }

  /* 面板缩回卡片矩形（返回流程），动画结束后回调切换视图 */
  function panelShrinkTo(studio, cardRect, done) {
    if (reducedMotion || !cardRect) { done(); return; }
    /* grow 动画 500ms 窗口内点返回时，studio 上残留 grow 的 inline
       transform——直接测量会得到带缩放/平移的矩形，FLIP 收拢起点错位；
       先清除残留 transform 并强制 reflow 再测量真实矩形 */
    studio.style.transition = "";
    studio.style.transform = "";
    studio.style.transformOrigin = "";
    void studio.offsetWidth;
    var pr = studio.getBoundingClientRect();
    var sx = cardRect.width / pr.width;
    var sy = cardRect.height / pr.height;
    var tx = cardRect.left - pr.left;
    var ty = cardRect.top - pr.top;
    studio.style.transformOrigin = "top left";
    studio.style.transition = "transform 0.32s cubic-bezier(0.4, 0, 0.6, 1)";
    studio.style.transform =
      "translate(" + tx.toFixed(1) + "px, " + ty.toFixed(1) + "px) scale(" +
      sx.toFixed(4) + ", " + sy.toFixed(4) + ")";
    setTimeout(function () {
      studio.style.transition = "";
      studio.style.transform = "";
      studio.style.transformOrigin = "";
      done();
    }, 340);
  }

  function clearSprings() {
    springToken++;
    var studio = $("#studioView");
    studio.classList.remove("enter");
    springEls().forEach(function (el) {
      el.classList.remove("spring-el");
      el.style.removeProperty("--spring-delay");
      el.style.removeProperty("--spring-tx");
      el.style.removeProperty("--spring-ty");
    });
  }

  /* 应用项目载荷到界面（打开项目与切换任务共用；不动视图切换动画） */
  function applyProjectPayload(projectId, payload) {
    currentProjectId = projectId;
    currentName = payload.meta.name || "未命名";
    files = payload.midi_files || [];
    draftDirty = false;

    $("#studioProjectName").textContent = "[PROJECT: " + currentName + "]";
    $("#modelStamp").textContent = "MODEL: " + (payload.settings.model || "--");

    renderMessages(payload.display_messages || []);
    resetEditUI();
    dirsList = payload.dirs || [];
    renderFiles();
    renderWorkspace(payload.workspace || { bound: false, path: "" });

    var input = $("#msgInput");
    input.value = payload.draft || "";
    input.disabled = false;
    $("#sendBtn").disabled = false;
    chatBusy = false;
    $("#newMidiLink").hidden = true;

    /* 任务选择条：当前任务 + 任务列表 */
    currentTaskId = payload.current_task_id || null;
    renderTaskSwitcher(payload.tasks || []);

    /* 任务联动：打开项目即已读该项目已完成任务；状态条跟随当前项目；
       项目任务状态变化（需要确认/已完成）时由 tasks.js 触发对话刷新 */
    if (window.Tasks) {
      Tasks.setCurrentProject(projectId);
      Tasks.markProjectRead(projectId);
    }
  }

  /* 任务切换条：下拉列出项目全部任务（显示任务名称），当前任务选中 */
  function renderTaskSwitcher(tasks) {
    var sel = $("#taskSelect");
    if (!sel) return;
    sel.innerHTML = "";
    (tasks || []).forEach(function (t) {
      var opt = document.createElement("option");
      opt.value = t.id;
      opt.textContent = t.name || "新任务";
      sel.appendChild(opt);
    });
    sel.value = currentTaskId || "";
    sel.disabled = !(tasks || []).length;
    var renameBtn = $("#renameTaskBtn");
    if (renameBtn) renameBtn.disabled = !currentTaskId;
    var delBtn = $("#deleteTaskBtn");
    if (delBtn) delBtn.disabled = !currentTaskId;
  }

  /* 切换任务（进行中任务可自由切换）：断开当前流式订阅（任务转后台续跑，
     不停止），加载目标任务的对话 */
  function switchTask(tid) {
    if (!currentProjectId || !tid || tid === currentTaskId) return;
    if (abortController) abortController.abort();
    chatEpoch++;   /* 切换任务上下文：旧流帧/收尾一律过期（与视图切换一致） */
    /* 切换任务：输入框残留文本先存入项目草稿（草稿=项目级"最近输入"语义，
       与跨项目切换 saveDraft 一致），再清空——避免旧任务文本残留进新任务，
       以及卸载时误覆盖项目草稿 */
    if (draftDirty) saveDraft();
    var swInput = $("#msgInput");
    if (swInput && swInput.value) swInput.value = "";
    draftDirty = false;
    if (editingIndex >= 0) {
      UI.postJSON("/api/projects/" + currentProjectId + "/messages/recall", {}).catch(function () {});
      resetEditUI();
    }
    UI.getJSON("/api/projects/" + currentProjectId + "?task_id=" + encodeURIComponent(tid))
      .then(function (payload) {
        if (currentProjectId !== payload.meta.id) return;
        currentTaskId = payload.current_task_id || tid;
        files = payload.midi_files || [];
        renderMessages(payload.display_messages || []);
        resetEditUI();
        dirsList = payload.dirs || [];
        renderFiles();
        renderWorkspace(payload.workspace || { bound: false, path: "" });
        $("#newMidiLink").hidden = true;
        renderTaskSwitcher(payload.tasks || []);
        if (window.Tasks) {
          Tasks.setCurrentProject(currentProjectId);
          Tasks.markProjectRead(currentProjectId);
        }
        /* 旧流已随 epoch 递增过期，chatDone 不会复位——这里显式复位忙态 */
        chatBusy = false;
        streamEnded = false;
        liveStreaming = false;
        var swSendBtn = $("#sendBtn");
        if (swSendBtn) {
          swSendBtn.textContent = "▶ 发送";
          swSendBtn.classList.remove("stop");
          swSendBtn.disabled = false;
        }
        if (swInput) swInput.disabled = false;
      })
      .catch(function (e) { UI.toast("✗ 切换任务失败: " + e.message, "err"); });
  }

  /* 新建任务：创建空对话线程并切换过去 */
  function createNewTask() {
    if (!currentProjectId) return;
    UI.postJSON("/api/projects/" + currentProjectId + "/tasks", {})
      .then(function (data) {
        var tid = data.task && data.task.id;
        if (window.Tasks) Tasks.refresh();
        if (tid) switchTask(tid);
      })
      .catch(function (e) { UI.toast("✗ 新建任务失败: " + e.message, "err"); });
  }

  /* 重命名当前任务 */
  function renameCurrentTask() {
    if (!currentProjectId || !currentTaskId) return;
    var sel = $("#taskSelect");
    var opt = sel && sel.options[sel.selectedIndex];
    var name = (opt && opt.textContent) || "新任务";
    showModal("重命名任务", name, function (newName) {
      return UI.postJSON("/api/tasks/" + encodeURIComponent(currentTaskId) + "/rename", { name: newName })
        .then(function () {
          if (opt) opt.textContent = newName;
          if (window.Tasks) Tasks.refresh();
        });
    });
  }

  /* 删除当前任务：仅删除对话记录，AI 生成的文件保留；删除后切到剩余任务 */
  function deleteCurrentTask() {
    if (!currentProjectId || !currentTaskId) return;
    var sel = $("#taskSelect");
    var opt = sel && sel.options[sel.selectedIndex];
    var name = (opt && opt.textContent) || "该任务";
    showConfirm("确定删除任务「" + name + "」？\n将删除该任务的对话记录（AI 生成的文件会保留）。", function () {
      UI.delJSON("/api/tasks/" + encodeURIComponent(currentTaskId))
        .then(function () {
          if (window.Tasks) Tasks.refresh();
          UI.getJSON("/api/projects/" + currentProjectId).then(function (payload) {
            if (currentProjectId !== payload.meta.id) return;
            currentTaskId = payload.current_task_id || null;
            files = payload.midi_files || [];
            renderMessages(payload.display_messages || []);
            resetEditUI();
            dirsList = payload.dirs || [];
            renderFiles();
            renderTaskSwitcher(payload.tasks || []);
          }).catch(function () {});
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });
  }

  function openProject(projectId, taskId) {
    if (isTransitioning) return;
    /* 切换视图：断开旧流（任务转后台续跑）+ 递增会话代数，使旧流帧/收尾全部过期 */
    if (abortController) abortController.abort();
    chatEpoch++;
    if (currentProjectId && currentProjectId !== projectId) saveDraft();
    UI.getJSON("/api/projects/" + projectId + (taskId ? "?task_id=" + encodeURIComponent(taskId) : ""))
      .then(function (payload) {
        applyProjectPayload(projectId, payload);

      /* 打开项目：不用 View Transition（VT 在 WebView2 掉帧/阻塞，是
         "惯性动画要等一会儿才播"与卡顿的根源）——直接切换视图：
         面板 FLIP 从卡片矩形放大成整页工作台 + spring-in 立即播放 */
      var studio = $("#studioView");
      var archive = $("#archiveView");
      var card = UI.qs('.proj-card[data-id="' + projectId + '"]');
      isTransitioning = true;
      /* 安全兜底：异常时 1.5 秒后强制释放（避免按钮永久不可点） */
      var morphGuard = setTimeout(function () { isTransitioning = false; }, 1500);

      /* archive 可见时捕获卡片矩形 + 中心（FLIP 起点 / 惯性推开原点） */
      var cardRect = null;
      var origin = null;
      if (card) {
        var cr = card.getBoundingClientRect();
        cardRect = { left: cr.left, top: cr.top, width: cr.width, height: cr.height };
        origin = { x: cr.left + cr.width / 2, y: cr.top + cr.height / 2 };
      }

      archive.hidden = true;
      studio.hidden = false;
      window.scrollTo(0, 0);

      /* 卡片放大成面板（FLIP 0.4s，合成层 transform 流畅） */
      panelGrowFrom(studio, cardRect);

      /* 组件惯性动画立即播放（60ms 启动，无 VT 等待）：
         面板生长途中组件受卡片惯性推出再弹性回位 */
      setTimeout(function () {
        springIn(origin ? origin.x : null, origin ? origin.y : null);
      }, 60);

      /* 形态动画结束后释放过渡锁（morphGuard 兜底防卡死） */
      setTimeout(function () {
        clearTimeout(morphGuard);
        isTransitioning = false;
      }, 550);
      setTimeout(function () { $("#msgInput").focus(); }, 60);
    }).catch(function (e) {
      UI.toast("✗ 打开项目失败: " + e.message, "err");
      isTransitioning = false;
    });
  }

  /* 新建项目进入工作台：无卡片来源，直接切换视图 + 面板内容弹入。
     与 openProject 不同：不捕获卡片矩形做 FLIP，springIn 无来源点。
     视图过渡进行中时延迟重试，避免"项目已创建但界面没反应" */
  function enterProject(payload) {
    if (isTransitioning) {
      setTimeout(function () { enterProject(payload); }, 650);
      return;
    }
    /* 新建项目进入工作台：同样断开旧流并递增会话代数（与 openProject 一致） */
    if (abortController) abortController.abort();
    chatEpoch++;
    currentProjectId = null; /* 防止保存旧草稿 */
    currentProjectId = payload.meta.id;
    currentTaskId = payload.current_task_id || null;
    currentName = payload.meta.name || "未命名";
    files = payload.midi_files || [];
    $("#studioProjectName").textContent = "[PROJECT: " + currentName + "]";
    $("#modelStamp").textContent = "MODEL: " + (payload.settings.model || "--");
    renderMessages(payload.display_messages || []);
    resetEditUI();
    dirsList = payload.dirs || [];
    renderFiles();
    renderWorkspace(payload.workspace || { bound: false, path: "" });
    renderTaskSwitcher(payload.tasks || []);
    if (window.Tasks) {
      Tasks.setCurrentProject(currentProjectId);
      Tasks.markProjectRead(currentProjectId);
    }
    $("#msgInput").value = payload.draft || "";
    $("#sendBtn").disabled = false;
    /* 旧流已随上方 epoch 递增过期，chatDone 不会复位——这里显式复位忙态
       （与切换任务处同理；此前流式中途返回再新建档案，首条消息会被
       chatBusy 静默吞掉，且发送按钮停留在"停止"文案） */
    chatBusy = false;
    streamEnded = false;
    liveStreaming = false;
    var epSendBtn = $("#sendBtn");
    if (epSendBtn) {
      epSendBtn.textContent = "▶ 发送";
      epSendBtn.title = "发送 (Enter)";
      epSendBtn.classList.remove("stop");
    }
    $("#newMidiLink").hidden = true;

    /* 无卡片来源：直接切换视图 + 面板内容弹入（无 VT，立即播放） */
    isTransitioning = true;
    $("#archiveView").hidden = true;
    $("#studioView").hidden = false;
    setTimeout(function () { springIn(null, null); }, 60);
    setTimeout(function () { $("#msgInput").focus(); }, 60);
    window.scrollTo(0, 0);
    setTimeout(function () { isTransitioning = false; }, 600);
  }

  function backToArchive() {
    /* 返回按钮始终可点：不检查 isTransitioning，避免动画卡住时无法返回 */
    /* 断开旧流（任务转后台续跑）+ 递增会话代数，旧流收尾不再触碰档案库视图 */
    if (abortController) abortController.abort();
    chatEpoch++;
    /* 修改模式离开：尽力恢复被截断的对话（异步、不阻塞返回动画），
       避免下次打开同一项目时内存会话仍是截断态 */
    if (editingIndex >= 0 && currentProjectId) {
      UI.postJSON("/api/projects/" + currentProjectId + "/messages/recall", {})
        .catch(function () {});
    }
    resetEditUI();
    if (currentProjectId) saveDraft();
    var pid = currentProjectId;
    currentProjectId = null;
    if (window.Arrange) window.Arrange.setProject(null); /* 编排窗口脱离项目并收起 */
    if (window.Tasks) Tasks.setCurrentProject(null);
    isTransitioning = true;
    var morphGuard = setTimeout(function () { isTransitioning = false; }, 1500);

    var studio = $("#studioView");
    var archive = $("#archiveView");

    UI.getJSON("/api/projects").then(function (projects) {
      renderProjects(projects);
      var card = pid ? UI.qs('.proj-card[data-id="' + pid + '"]') : null;

      /* 返回不用 View Transition（VT 在 WebView2 掉帧/阻塞）——
         面板 FLIP 缩小回卡片矩形（合成层 transform），随后切换视图，
         邻居卡片涟漪与落点弹跳在真实 DOM 播放（流畅） */
      var cardRect = null;
      if (card) {
        /* archive 隐藏时 getBoundingClientRect 返回 0：
           同步窗口内短暂显示 archive 测量卡片真实矩形再隐藏（无闪烁） */
        archive.hidden = false;
        cardRect = card.getBoundingClientRect();
        archive.hidden = true;
      }

      /* 面板缩回卡片（0.32s），动画结束后切换视图 + 涟漪 */
      panelShrinkTo(studio, cardRect, function () {
        studio.hidden = true;
        archive.hidden = false;
        window.scrollTo(0, 0);
        if (card && !reducedMotion) {
          /* 邻居卡片温柔涟漪（真实 DOM：推出→回弹振荡→归位）——
             作用在开头已渲染的卡片 DOM 上，不再重复加载列表（会重建
             卡片导致涟漪动画被销毁） */
          applyNeighborRipple(card, archive);
          /* 落点卡片弹性落位 */
          applyLandPop(card);
        }
        clearSprings();
        clearTimeout(morphGuard);
        isTransitioning = false;
      });
    }).catch(function (e) {
      UI.toast("✗ 返回档案库失败: " + e.message, "err");
      clearSprings();
      studio.hidden = true;
      archive.hidden = false;
      isTransitioning = false;
      reloadProjects();
    });
  }

  /* 落点卡片弹性落位：返回档案库时卡片从缩回状态弹回自然大小 */
  function applyLandPop(card) {
    card.classList.remove("land-pop");
    void card.offsetWidth;   /* 重新触发动画 */
    card.classList.add("land-pop");
    setTimeout(function () { card.classList.remove("land-pop"); }, 600);
  }

  /* 邻居卡片涟漪（真实 DOM 动画）：以落点卡片为中心向外推开，
     带回弹振荡（推出→反向回弹→归位），合成层 transform 流畅。
     动画完成后清理 class 与变量 */
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
      /* 推开 14px 沿远离落点方向归一化（温柔力度） */
      var push = 14;
      n.style.setProperty("--push-x", (dx / d * push).toFixed(1) + "px");
      n.style.setProperty("--push-y", (dy / d * push).toFixed(1) + "px");
      /* 距离越远延迟越大：~0.2ms/px，波纹从中心温柔扩散（最远 ~250ms） */
      n.style.setProperty("--ripple-delay", Math.round(d * 0.2) + "ms");
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

  /* 文件清单目录树展开状态（跨渲染记忆：文件变更重建后保持展开） */
  var expandedDirs = {};
  /* 当前选中的文件夹层级（新建文件夹的目标位置；"" = 根目录） */
  var selectedDir = "";
  /* 空文件夹相对路径列表（后端扫描主目录得到，文件树合并显示） */
  var dirsList = [];
  /* 拖拽移动中的文件名（dragstart 记录，drop 消费） */
  var dragFileName = null;

  /* 构建目录树：按 name 的路径段组织，并合并 dirsList 中的空文件夹。
     树结构：{ 目录名: {子节点...}, 文件名: {file: 文件信息} } */
  function buildFileTree(fileList) {
    var tree = {};
    fileList.forEach(function (f) {
      var parts = f.name.split("/");
      var node = tree;
      for (var i = 0; i < parts.length - 1; i++) {
        var dir = parts[i];
        if (!node[dir]) node[dir] = {};
        node = node[dir];
      }
      node[parts[parts.length - 1]] = { file: f };
    });
    /* 空文件夹（无 midi 文件的目录）也要显示 */
    dirsList.forEach(function (d) {
      var parts = d.split("/");
      var node = tree;
      for (var i = 0; i < parts.length; i++) {
        if (!node[parts[i]]) node[parts[i]] = {};
        node = node[parts[i]];
      }
    });
    return tree;
  }

  /* 递归渲染树节点：文件夹在前、文件在后，各自字母序（与后端
     list_project_structure 的树一致）。返回 {frag, count}，count 为
     子树内的文件总数 */
  function renderTreeNode(node, path) {
    var frag = document.createDocumentFragment();
    var count = 0;
    var dirs = [];
    var leafs = [];
    Object.keys(node).forEach(function (k) {
      (node[k].file ? leafs : dirs).push(k);
    });
    dirs.sort();
    leafs.sort();
    dirs.forEach(function (d) {
      var childPath = path ? path + "/" + d : d;
      var child = renderTreeNode(node[d], childPath);
      count += child.count;
      var isOpen = !!expandedDirs[childPath];
      var row = document.createElement("div");
      row.className = "file-dir" + (isOpen ? " open" : "") +
        (selectedDir === childPath ? " selected" : "");
      row.dataset.path = childPath;
      row.innerHTML =
        '<span class="file-dir-arrow">' + (isOpen ? "▾" : "▸") + "</span>" +
        '<input type="checkbox" class="file-check file-dir-check" title="勾选 ' + UI.esc(childPath) + " 下的全部文件\">" +
        '<span class="file-dir-name" title="' + UI.esc(childPath) + '">📁 ' + UI.esc(d) + "</span>" +
        '<span class="file-size">' + child.count + "</span>";
      var body = document.createElement("div");
      body.className = "file-dir-children";
      body.hidden = !isOpen;
      body.appendChild(child.frag);
      frag.appendChild(row);
      frag.appendChild(body);
    });
    leafs.forEach(function (name) {
      var f = node[name].file;
      count++;
      var label = document.createElement("label");
      label.className = "file-item";
      label.draggable = true;   /* 拖拽移动层级 */
      label.title = "双击在钢琴卷帘打开 / 拖动移动到其他文件夹";
      label.innerHTML =
        '<input type="checkbox" class="file-check" value="' + UI.esc(f.name) + '">' +
        '<span class="file-name" title="' + UI.esc(f.name) + '">' + UI.esc(name) + "</span>" +
        '<span class="file-size">' + UI.fmtSize(f.size) + "</span>";
      frag.appendChild(label);
    });
    return { frag: frag, count: count };
  }

  /* 勾选/取消文件夹下全部文件复选框（含子文件夹；程序化设置不触发 change） */
  function setDirChecked(body, checked) {
    body.querySelectorAll(".file-check").forEach(function (c) {
      c.checked = checked;
      c.indeterminate = false;
    });
  }

  /* 由子文件勾选态向上刷新祖先文件夹的 勾选/半选 状态。
     子容器（.file-dir-children）是文件夹行的相邻兄弟节点 */
  function syncDirChecks(dirRow) {
    var self = dirRow.querySelector(":scope > .file-dir-check");
    if (!self) return;
    var body = dirRow.nextElementSibling;
    var subChecks = body ? body.querySelectorAll(".file-check") : [];
    var total = subChecks.length;
    var checked = 0;
    subChecks.forEach(function (c) {
      if (c.checked) checked++;
    });
    self.checked = total > 0 && checked === total;
    self.indeterminate = checked > 0 && checked < total;
    /* 向上递归：父文件夹行 = 本行所在子容器的前一个兄弟（行与子容器
       是相邻兄弟节点） */
    var container = dirRow.parentElement;
    var parent = container && container.classList.contains("file-dir-children")
      ? container.previousElementSibling : null;
    if (parent && parent.classList.contains("file-dir")) syncDirChecks(parent);
  }

  function renderFiles() {
    var list = $("#fileList");
    list.innerHTML = "";
    $("#fileStamp").textContent = files.length + " FILES";
    /* 编排窗口联动：项目切换（setProject 幂等）+ 左栏 MIDI 列表刷新。
       renderFiles 在打开项目 / 新建项目 / SSE files 三条路径都会执行，
       此处 currentProjectId 已就绪 */
    if (window.Arrange) {
      window.Arrange.setProject(currentProjectId);
      window.Arrange.refreshMidiList(files);
    }
    /* 文件与空文件夹都为空才显示占位；只有空文件夹时照常渲染目录树 */
    if (!files.length && !dirsList.length) {
      list.innerHTML = '<div style="padding:14px;font-size:12px;color:var(--color-ink-faint);text-align:center">（暂无文件）<br>点击下方「上传」导入 .mid，或在对话中让 AI 生成</div>';
      return;
    }
    var root = renderTreeNode(buildFileTree(files), "");
    /* 根目录行：固定渲染在顶部——目录只有文件夹时仍可把文件拖回根目录，
       也可点击选中"根目录"层级（新建文件夹的默认位置） */
    var rootRow = document.createElement("div");
    rootRow.className = "file-dir root-row" + (selectedDir === "" ? " selected" : "");
    rootRow.dataset.path = "";
    rootRow.title = "根目录（拖拽文件到这里移回根目录）";
    rootRow.innerHTML =
      '<span class="file-dir-arrow">·</span>' +
      '<span class="file-dir-name">📂 根目录</span>' +
      '<span class="file-size">' + files.length + "</span>";
    list.appendChild(rootRow);
    list.appendChild(root.frag);
  }

  /* 应用文件清单变化：同步刷新空文件夹列表（dirs），保持目录树一致 */
  function applyFiles(newFiles, dirs) {
    files = newFiles || [];
    if (dirs) dirsList = dirs;
    renderFiles();
  }

  /* 记录当前选中的文件夹层级（新建文件夹的目标位置）并高亮；
     path="" 时高亮固定的根目录行（无行参数表示点击空白区域回到根层级） */
  function selectDir(path, row) {
    if (selectedDir === path) return;
    selectedDir = path;
    UI.qsa(".file-dir.selected", $("#fileList")).forEach(function (el) {
      el.classList.remove("selected");
    });
    if (row) row.classList.add("selected");
    else if (path === "") {
      var rr = $("#fileList .root-row");
      if (rr) rr.classList.add("selected");
    }
  }

  /* 拖拽移动：把文件移到目标文件夹（"" = 根目录） */
  function moveFile(name, target) {
    if (!currentProjectId) return;
    /* 目标就是当前位置：跳过 */
    var parts = name.split("/");
    var curDir = parts.slice(0, -1).join("/");
    if (curDir === target) {
      UI.toast("文件已在该位置", "warn");
      return;
    }
    UI.postJSON("/api/projects/" + currentProjectId + "/files/move", {
      moves: [{ name: name, target: target }],
    }).then(function (data) {
      applyFiles(data.files, data.dirs);
      if (data.failed && data.failed.length) {
        UI.toast("✗ 移动失败: " + data.failed.join(", "), "err");
      } else {
        UI.toast("✓ 已移动到" + (target ? "「" + target + "」" : "根目录"), "ok");
      }
    }).catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* ═══════════ 右键菜单（文件 / 文件夹） ═══════════ */

  /* 重命名文件：同目录改名（可带路径，与移动共用 /files/move 接口） */
  function renameFile(name) {
    var curDir = name.split("/").slice(0, -1).join("/");
    var basename = name.split("/").pop();
    showModal("✏ 重命名文件", basename, function (newName) {
      return UI.postJSON("/api/projects/" + currentProjectId + "/files/move", {
        moves: [{ name: name, target: curDir, rename: newName }],
      }).then(function (data) {
        applyFiles(data.files, data.dirs);
        if (data.failed && data.failed.length) {
          throw new Error("重命名失败: " + data.failed.join(", "));
        }
        UI.toast("✓ 已重命名", "ok");
      });
    });
  }

  /* 删除文件：直接删除（无确认，可撤销），与删除按钮同一接口 */
  function deleteFileByName(name) {
    UI.delJSON("/api/projects/" + currentProjectId + "/files", { names: [name] })
      .then(function (data) {
        applyFiles(data.files, data.dirs);
        if (data.failed && data.failed.length) {
          UI.toast("✗ 删除失败（可能被占用）", "err");
        } else {
          UI.toast("✓ 已删除（可撤销）", "ok");
        }
      })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 重命名文件夹：目录级重命名，清单内文件前缀同步更新 */
  function renameFolder(path) {
    showModal("✏ 重命名文件夹", path.split("/").pop(), function (newName) {
      return UI.postJSON("/api/projects/" + currentProjectId + "/folders/rename", {
        old: path,
        name: newName,
      }).then(function (data) {
        applyFiles(data.files, data.dirs);
        /* 重命名的是当前选中层级：选中态跟随新路径 */
        if (selectedDir === path) {
          var parentDir = path.split("/").slice(0, -1).join("/");
          selectDir(parentDir ? parentDir + "/" + newName : newName, null);
          renderFiles();
        }
        UI.toast("✓ 已重命名文件夹", "ok");
      });
    });
  }

  /* 删除文件夹：内部文件全部入回收站，目录删除，可撤销 */
  function deleteFolder(path) {
    UI.postJSON("/api/projects/" + currentProjectId + "/folders/delete", { folder: path })
      .then(function (data) {
        applyFiles(data.files, data.dirs);
        if (selectedDir === path) selectDir("", null);
        UI.toast("✓ 已删除文件夹（可撤销）", "ok");
      })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 下载：文件单文件 / 文件夹内全部文件（多文件后端自动打包 zip） */
  function downloadByNames(names) {
    if (!names.length) { UI.toast("文件夹内没有文件", "warn"); return; }
    window.location.href = "/api/projects/" + currentProjectId + "/download?names=" +
      encodeURIComponent(names.join(","));
  }
  function downloadFileByName(name) { downloadByNames([name]); }
  function downloadFolder(path) {
    var prefix = path + "/";
    var names = files.filter(function (f) {
      return f.name.indexOf(prefix) === 0;
    }).map(function (f) { return f.name; });
    downloadByNames(names);
  }

  /* 当前右键菜单 DOM（body 下动态创建，点击外部 / Esc 关闭） */
  var ctxMenu = null;

  function closeCtxMenu() {
    if (ctxMenu) {
      ctxMenu.remove();
      ctxMenu = null;
    }
  }

  /* 在鼠标位置打开右键菜单：视口边缘自动翻转（菜单在 body 下，
     菜单内点击已在 capture 阶段被跳过，不会被外部点击关闭） */
  function openCtxMenu(e, actions) {
    e.preventDefault();
    closeCtxMenu();
    var menu = document.createElement("div");
    menu.className = "ctx-menu menu-in";
    actions.forEach(function (a) {
      if (a === "-") {
        var sep = document.createElement("div");
        sep.className = "ctx-sep";
        menu.appendChild(sep);
        return;
      }
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "ctx-item" + (a.danger ? " danger" : "");
      btn.textContent = a.label;
      btn.addEventListener("click", function () {
        closeCtxMenu();
        a.run();
      });
      menu.appendChild(btn);
    });
    document.body.appendChild(menu);
    ctxMenu = menu;
    var w = menu.offsetWidth, h = menu.offsetHeight;
    var x = Math.min(e.clientX, window.innerWidth - w - 8);
    var y = Math.min(e.clientY, window.innerHeight - h - 8);
    menu.style.left = Math.max(8, x) + "px";
    menu.style.top = Math.max(8, y) + "px";
  }

  function selectedNames() {
    return UI.qsa(".file-check:checked", $("#fileList")).map(function (c) { return c.value; });
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

  /* 解析服务端格式化内容：多个 <details> 折叠块（思考过程/工具调用）+ 正文。
     返回 { blocks: [{summary, body}], answer }；summary 文本用于区分
     思考块（自动展开）与工具调用块（保持折叠） */
  function parseStreamContent(content) {
    var blocks = [];
    var rest = content || "";
    var re = /<details>\s*<summary>([\s\S]*?)<\/summary>([\s\S]*?)<\/details>/;
    var m;
    while ((m = re.exec(rest))) {
      blocks.push({ summary: m[1], body: m[2] });
      rest = rest.slice(0, m.index) + rest.slice(m.index + m[0].length);
    }
    return { blocks: blocks, answer: rest };
  }

  /* 流式渲染节流：delta 到达即累积、按固定间隔合并渲染一次。
     80ms（≈12fps）高于人眼对"流式输出连续性"的感知阈值，
     且显著低于逐 delta 渲染的 DOM/解析开销 */
  var STREAM_RENDER_MS = 80;
  /* 增量累计器 + 渲染定时器：每个流式回合由后端 msg_index 区分 */
  var streamAccum = null;          /* { msgIndex, reasoning, content } */
  var streamRenderTimer = null;

  /* 把原始推理/正文两股流包成与后端 FormatDisplayMessage 同构的串
     （<details> 思考块 + 正文），复用既有的 parseStreamContent 解析路径 */
  function formatRawStream(reasoning, content) {
    var s = "";
    if (reasoning) {
      s += "<details>\n<summary>思考过程</summary>\n\n" + reasoning + "\n</details>\n\n";
    }
    return s + content;
  }

  /* 收到一条增量事件：累积到当前回合的累计器并安排一次节流渲染 */
  function applyStreamDeltaEvent(ev) {
    if (!liveStreaming || chatEpoch !== streamEpoch) return;
    var idx = typeof ev.msg_index === "number" ? ev.msg_index : currentMessages.length - 1;
    if (!streamAccum || streamAccum.msgIndex !== idx) {
      streamAccum = { msgIndex: idx, reasoning: "", content: "" };
    }
    var r = ev.reasoning_delta || "";
    var c = ev.content_delta || "";
    if (!r && !c) return;
    streamAccum.reasoning += r;
    streamAccum.content += c;
    if (streamRenderTimer) return;
    streamRenderTimer = setTimeout(function () {
      streamRenderTimer = null;
      if (!streamAccum || !liveStreaming || chatEpoch !== streamEpoch) return;
      renderStreamingTail(
        formatRawStream(streamAccum.reasoning, streamAccum.content),
        streamAccum.msgIndex,
        lastMessages.length
      );
    }, STREAM_RENDER_MS);
  }

  function resetStreamDelta() {
    if (streamRenderTimer) { clearTimeout(streamRenderTimer); streamRenderTimer = null; }
    streamAccum = null;
  }

  /* 构建流式消息元素：思考块与正文全程 markdown 渲染（结束后不再有
     "纯文本→markdown"的整体跳变）；返回 { el, parsed }，
     后续帧用 updateStreamingMessage 刷新内容 */
  function buildStreamingMessage(content) {
    var parsed = parseStreamContent(content);
    var el = document.createElement("div");
    el.className = "msg ai streaming";
    var label = document.createElement("div");
    label.className = "msg-label";
    label.textContent = "AI · 乐理专家";
    el.appendChild(label);
    parsed.blocks.forEach(function (b) {
      el.appendChild(buildDetailsBlock(b.summary));
    });
    var ans = document.createElement("div");
    ans.className = "stream-answer";
    ans.innerHTML = UI.md(parsed.answer);
    ans._mdSrc = UI.md(parsed.answer);
    el.appendChild(ans);
    return { el: el, parsed: parsed };
  }

  /* 单个折叠块骨架（思考过程/工具调用共用），内文 markdown 渲染进 .stream-text */
  function buildDetailsBlock(summaryText) {
    var det = document.createElement("details");
    var sum = document.createElement("summary");
    sum.textContent = summaryText;
    det.appendChild(sum);
    var body = document.createElement("div");
    body.className = "details-body";
    var inner = document.createElement("div");
    inner.className = "details-inner";
    var txt = document.createElement("div");
    txt.className = "stream-text";
    inner.appendChild(txt);
    body.appendChild(inner);
    det.appendChild(body);
    return det;
  }

  /* 流式消息内容刷新：块数对齐 + 各块正文 markdown 全量替换。
     内容不变的部分靠 _mdSrc 缓存跳过 innerHTML 写入；
     复用既有 details 元素——用户手动开合状态自然保留 */
  function updateStreamingMessage(entry, content) {
    var parsed = parseStreamContent(content);
    var el = entry.el;
    var answerEl = el.querySelector(".stream-answer");

    /* 块数增长：在正文前插入新块 */
    var dets = [];
    el.querySelectorAll(":scope > details").forEach(function (d) { dets.push(d); });
    while (dets.length < parsed.blocks.length) {
      var b = parsed.blocks[dets.length];
      var det = buildDetailsBlock(b.summary);
      det.querySelector(".stream-text").innerHTML = UI.md(b.body);
      det.querySelector(".stream-text")._mdSrc = UI.md(b.body);
      el.insertBefore(det, answerEl);
      dets.push(det);
    }
    /* 块数减少（内容形态变化）：移除尾部多余的旧块 */
    for (var i = parsed.blocks.length; i < dets.length; i++) {
      if (detailsClosing === dets[i]) detailsClosing = null;
      dets[i].remove();
    }
    for (var j = 0; j < parsed.blocks.length && j < dets.length; j++) {
      var txt = dets[j].querySelector(".stream-text");
      if (!txt) continue;
      var html = UI.md(parsed.blocks[j].body);
      if (txt._mdSrc !== html) {
        txt.innerHTML = html;
        txt._mdSrc = html;
      }
    }
    if (answerEl) {
      var ah = UI.md(parsed.answer);
      if (answerEl._mdSrc !== ah) {
        answerEl.innerHTML = ah;
        answerEl._mdSrc = ah;
      }
    }
    entry.parsed = parsed;
  }

  /* 归一化：把思考块整体替换为占位符——推理在增长时格式化字符串并非
     前缀链（推理插在 <details> 内部），归一化后比较才能判定"同一消息" */
  function normalizeThinking(content) {
    return content.replace(
      /<details>\s*<summary>思考过程<\/summary>[\s\S]*?<\/details>/g,
      "<details>思考</details>"
    );
  }

  function renderMessages(messages) {
    var chat = $("#chat");
    var list = messages || [];
    currentMessages = list;
    editMenuIndex = -1;   /* 整表重建：修改菜单随之销毁，状态复位 */

    /* ── 实时流式：最后一条消息用增量结构 ──
       提问卡片消息（type=question，无 content）不走文本流式路径 */
    if (liveStreaming && list.length) {
      var last = list[list.length - 1];
      if (last && last.role === "assistant" && last.type !== "question") {
        renderStreamingTail(last.content || "", list.length - 1, list.length - 1);
        return;
      }
    }

    /* ── 非流式：全量渲染 ── */
    /* 流式重渲染时保持已展开的 details（工具调用等）不收起 */
    var openDetails = collectOpenDetails(chat, false);
    detailsAnimBusy = 0;   /* 旧元素即将销毁，其过渡被取消且不再触发 transitionend */
    detailsClosing = null; /* 旧元素销毁，延迟关闭状态随之失效 */
    chat.innerHTML = "";
    for (var i = 0; i < list.length; i++) {
      var mEl = renderMessageCached(list[i]);
      mEl.dataset.index = i;
      chat.appendChild(mEl);
    }
    /* 空对话占位：无消息时给出引导（流式期间不会走到这里） */
    if (!list.length) {
      var chatEmpty = document.createElement("div");
      chatEmpty.className = "chat-empty-hint";
      chatEmpty.textContent = "发送消息开始对话——让 AI 为你的 MIDI 配和弦、写旋律或讲解乐理";
      chat.appendChild(chatEmpty);
    }
    /* 恢复上一帧的展开态：details 的 open 属性 + body 的 .open 类都要恢复，
       否则重渲染后 sync 会把每个展开块误判为"刚展开"而重播动画 */
    restoreOpenDetails(chat, openDetails);
    streamTrack.el = null;
    streamTrack.norm = "";

    /* 折叠块展开/收起动画同步 */
    syncDetailsBodies(chat);

    if (nearBottom) chat.scrollTop = chat.scrollHeight;
    updateScrollBtn();
  }

  /* 流式尾部渲染：全量 chat 帧（renderMessages 委托）与增量 delta 帧
     （applyStreamDeltaEvent 的节流定时器）共用同一条路径。
     content = formatted 串（<details> 思考块 + 正文，与后端 FormatDisplayMessage
     同构）；aiIndex = 本条消息在服务端列表中的下标；histLen = 历史区长度，
     整表重建时只渲染 [0, histLen)，正在流式的这条单独构建。
     增量模式下服务端列表尚未包含本轮占位消息 → histLen 用调用方传入的
     lastMessages.length，历史区与"当前流式尾"天然分离开 */
  function renderStreamingTail(content, aiIndex, histLen) {
    var chat = $("#chat");
    var norm = normalizeThinking(content);
    var same = !!streamTrack.el && !!streamTrack.norm
      && (norm === streamTrack.norm || norm.indexOf(streamTrack.norm) === 0);

        if (same && chat.lastElementChild === streamTrack.el) {
          /* 同一消息持续流式：只刷新内容（updateStreamingMessage 内部按
             markdown 源串缓存跳过未变化部分） */
          updateStreamingMessage(streamTrack, content);
        } else {
          /* 新消息（新一轮思考/工具块/首帧）：重建全部，最后一条用流式结构。
             若聊天内有正在播放动画的 details（如上一轮思考块自动收起动画
             尚未播完、工具块消息就到了）或消息入场动画（用户消息滑入
             尚未播完），先等动画播完再重建——否则整表重建会销毁动画，
             思考块表现为瞬间收起/展开、用户消息滑入被掐断 */
          if (hasRunningDetailsAnim(chat) || hasRunningMsgEnter(chat)) {
            if (!rebuildDeferred) {
              rebuildDeferred = true;
              var epochAtRebuild = chatEpoch;
              setTimeout(function () {
                rebuildDeferred = false;
                if (chatEpoch !== epochAtRebuild) return; /* 延迟期间已开始新会话，放弃旧重建 */
                if (lastMessages.length) renderMessages(lastMessages);
              }, DETAILS_CLOSE_DELAY_MS);
            }
            return;
          }
          rebuildDeferred = false;
          /* 本条 AI 消息是否首次出现（首帧）：是 → 播放入场动画；
             工具块/新思考块触发的同消息重建 → 跳过（已入场过） */
          var isNewAiMsg = !streamTrack.el ||
            streamTrack.el.dataset.index !== String(aiIndex);
          /* 重建前记录已展开的 details，重建后恢复（避免每帧把用户
             展开的思考块/工具块重新收起）。历史消息按 (消息下标, 消息内
             相对索引) 恢复；本条流式消息的块内容随流变化（下标不可靠），
             用 summary 文本匹配恢复——否则同消息重建后思考块/工具块会
             悄悄合拢 */
          var openDetails = collectOpenDetails(chat, true);
          var streamOpenSummaries = [];
          if (streamTrack.el) {
            streamTrack.el.querySelectorAll("details").forEach(function (d) {
              if (!d.open) return;
              var s = d.querySelector("summary");
              if (s) streamOpenSummaries.push(s.textContent);
            });
          }
          detailsAnimBusy = 0;   /* 旧元素即将销毁，其过渡被取消且不再触发 transitionend */
          detailsClosing = null; /* 旧元素销毁，延迟关闭状态随之失效 */
          chat.innerHTML = "";
          for (var i = 0; i < histLen; i++) {
            var mEl = renderMessageCached(lastMessages[i]);
            mEl.dataset.index = i;
            chat.appendChild(mEl);
          }
          restoreOpenDetails(chat, openDetails);
          streamTrack = buildStreamingMessage(content);
          streamTrack.norm = norm;
          streamTrack.el.dataset.index = String(aiIndex);
          chat.appendChild(streamTrack.el);
          /* 恢复本条流式消息内仍存在的已展开块（summary 文本匹配） */
          if (streamOpenSummaries.length) {
            streamTrack.el.querySelectorAll("details").forEach(function (d) {
              var s = d.querySelector("summary");
              if (s && streamOpenSummaries.indexOf(s.textContent) !== -1) {
                /* 思考块：用户手动收起过则跳过恢复——收起动画播放期间
                   d.open 仍为 true 会被上面的收集逻辑记录，无条件恢复
                   会把用户的收起静默撤销（表现为"收不起来"） */
                if (s.textContent.indexOf("思考过程") !== -1 && thinkUserCollapsed) return;
                d.open = true;
                var b = d.querySelector(":scope > .details-body");
                if (b) {
                  b.classList.add("open");
                  b.style.maxHeight = "none";  /* 恢复展开=立即全开 */
                }
              }
            });
          }
          /* AI 消息入场动画：从下往上渐显飞入（仅首次出现时） */
          if (isNewAiMsg) animateMsgEnter(streamTrack.el, false);
        }

        /* 思考块自动展开/收起（带动画版）：推理增长 → 平滑展开；推理停顿
           超过 THINK_SETTLE_MS → 平滑收起。动画节流护栏：
           - 用户手动操作过思考块（userToggledStream）后自动逻辑完全让位；
           - 同一段推理持续增长期间保持展开不重复播动画（开合振荡防线），
             只有"新段落出现"或"收起后再增长"才重新播展开动画；
           - 收起方向统一走 collapseDetails（含 Chromium 冻结补丁与
             detailsClosing 防重入），手动/自动共用同一套动画路径 */
        var thinkDet = null;
        var dets = streamTrack.el.querySelectorAll("details");
        for (var di = dets.length - 1; di >= 0; di--) {
          var sum = dets[di].querySelector("summary");
          if (sum && sum.textContent.indexOf("思考过程") !== -1) {
            thinkDet = dets[di];
            break;
          }
        }
        if (thinkDet) {
          /* 推理文本与长度一律取原始流数据（entry.parsed 的 body），
             不用渲染后的 textContent——markdown 会增删字符破坏
             长度单调性，导致停顿判定失灵 */
          var reasoning = "";
          if (streamTrack.parsed) {
            for (var pi = 0; pi < streamTrack.parsed.blocks.length; pi++) {
              var psum = streamTrack.parsed.blocks[pi].summary;
              if (psum && psum.indexOf("思考过程") !== -1) {
                reasoning = (streamTrack.parsed.blocks[pi].body || "").trim();
              }
            }
          }
          if (thinkDet !== thinkingTrack.det) {
            /* 思考块元素变化：整表重建（同段，推理未断）或多段思考的新段落。
               新段落重置基线并播展开动画跟随 */
            var sameSegment = thinkingTrack.reasoning.length > 0 &&
              reasoning.length >= thinkingTrack.reasoning.length &&
              reasoning.indexOf(thinkingTrack.reasoning.slice(0, 40)) === 0;
            thinkingTrack.det = thinkDet;
            thinkingTrack.reasoning = reasoning;
            thinkingTrack.lastGrowAt = Date.now();
            if (!sameSegment && !userToggledStream) autoExpand(thinkDet, true);
          } else if (reasoning.length > thinkingTrack.reasoning.length) {
            var wasSettled = Date.now() - thinkingTrack.lastGrowAt >= THINK_SETTLE_MS;
            thinkingTrack.reasoning = reasoning;
            thinkingTrack.lastGrowAt = Date.now();
            /* 持续增长且仍展开时无需动作（autoExpand 对已开块是幂等 no-op）；
               只有"收起停顿后恢复增长"才平滑重新展开 */
            if (!userToggledStream && wasSettled) {
              autoExpand(thinkDet, true);
            }
          } else if (reasoning.length && reasoning.length === thinkingTrack.reasoning.length && !userToggledStream) {
            /* 推理停顿：静默超过 THINK_SETTLE_MS 才平滑收起 */
            if (Date.now() - thinkingTrack.lastGrowAt >= THINK_SETTLE_MS &&
                thinkDet !== detailsClosing) {
              autoCollapse(thinkDet);
            }
          }
        } else {
          thinkingTrack.det = null;
          thinkingTrack.reasoning = "";
          thinkingTrack.lastGrowAt = 0;
        }

        /* 折叠块展开/收起动画同步（自动展开/收起与手动点击后都生效） */
        syncDetailsBodies(chat);
        if (nearBottom) chat.scrollTop = chat.scrollHeight;
        updateScrollBtn();
  }

  /* 一键滚动到底部按钮显隐：仅在滚动离开底部(>40px)时浮现，
     位于底部即隐藏（渲染路径/滚动事件都会刷新） */
  function updateScrollBtn() {
    var btn = $("#scrollBottomBtn");
    if (!btn) return;
    btn.classList.toggle("visible", !nearBottom);
  }

  /* ═══════════ 一键滚动到底部：1 秒平滑动画 ═══════════ */
  var SCROLL_ANIM_MS = 1000;        /* 固定 1 秒，无论距底部多远 */
  var scrollAnimToken = 0;          /* 动画令牌：新动画/用户滚轮打断都会作废旧动画 */
  var scrollAnimating = false;      /* 动画进行中：期间 scroll 事件不刷新按钮显隐
                                       （否则按钮会在滚动中途闪烁） */

  /* 平滑滚动到底部：ease-out cubic 缓动，固定时长；动画结束恢复自动跟随。
     流式输出期间点击时，动画结束后 nearBottom=true 由流式帧继续自动滚到底 */
  function smoothScrollToBottom(el) {
    var token = ++scrollAnimToken;
    var startY = el.scrollTop;
    var endY = el.scrollHeight - el.clientHeight;
    if (endY <= startY) {
      nearBottom = true;
      updateScrollBtn();
      return;
    }
    var startTime = null;
    scrollAnimating = true;
    function step(ts) {
      if (token !== scrollAnimToken) { scrollAnimating = false; return; }
      if (startTime === null) startTime = ts;
      var p = Math.min((ts - startTime) / SCROLL_ANIM_MS, 1);
      var eased = 1 - Math.pow(1 - p, 3);   /* ease-out cubic：先快后慢 */
      el.scrollTop = startY + (endY - startY) * eased;
      if (p < 1) {
        requestAnimationFrame(step);
      } else {
        el.scrollTop = el.scrollHeight - el.clientHeight;
        scrollAnimating = false;
        nearBottom = true;
        updateScrollBtn();
      }
    }
    requestAnimationFrame(step);
  }

  /* 服务端展示文本 = <details> 折叠块（受信任 HTML）+ markdown 正文。
     折叠块的摘要与内部内容（工具参数/结果、推理文本）也是 markdown，
     解析后重包；正文用完整 markdown 渲染。内部内容包进 .details-body，
     供展开/收起动画（max-height 过渡）使用 */
  function renderDetailsContent(content) {
    var parts = content.split(/(<details>[\s\S]*?<\/details>)/g);
    return parts.map(function (part) {
      if (part.indexOf("<details>") !== 0) return UI.md(part);
      var m = /^<details>\s*<summary>([\s\S]*?)<\/summary>([\s\S]*?)<\/details>$/.exec(part);
      if (!m) return UI.esc(part);
      return "<details><summary>" + UI.mdInline(m[1]) + "</summary>" +
        '<div class="details-body"><div class="details-inner">' + UI.md(m[2]) +
        "</div></div></details>";
    }).join("\n");
  }

  /* 折叠块展开/收起：CSS grid-template-rows 0fr↔1fr 原生过渡（.open 类驱动）。
     不再用 WAAPI + max-height（每帧测量 scrollHeight 强制重排是开合卡顿根源）；
     grid 过渡无需测量高度。流式内容增长时已展开的块自然撑开、无动画 */
  var DETAILS_ANIM_MS = 300;
  /* 思考块自动收起前的推理静默期：推理停止增长超过该时长才瞬时收起。
     旧值 0（"思考完毕立刻收起"）在「思考→组装→再思考」交错流式中，
     模型组装下一段推理的短暂停顿也会触发收起，推理恢复又展开——
     自动开合动画来回播 = 思考块抽搐。500ms 吸收这类短停顿；
     自动收起/展开本身是瞬时的（见 autoExpand/autoCollapse），
     动画只保留给用户手动开合 */
  var THINK_SETTLE_MS = 500;
  /* 开合动画结束后的重建等待时长（grid 过渡 300ms + 余量） */
  var DETAILS_CLOSE_DELAY_MS = DETAILS_ANIM_MS + 100;

  /* 检测聊天内是否有正在播放展开/收起过渡的 details。
     结构变化整表重建会销毁旧元素上正在播放的动画（表现为思考块
     "啪"地瞬间收起/展开），重建前用它判断是否需要等动画播完。
     优先用 detailsAnimBusy 计数（transitionstart/end 委托维护，
     过渡在渲染更新阶段启动即登记，无 getAnimations 的时序窗口）；
     getAnimations 仅作兜底。
     注意：必须统计所有 .details-body 而不只是 .open——收起方向动画
     发生时 .open 类已被 syncDetailsBodies 立即移除，只查 .open 会漏检，
     重建仍会掐断收起动画（表现为思考块瞬间收起） */
  function hasRunningDetailsAnim(root) {
    if (detailsAnimBusy > 0) return true;
    var found = false;
    root.querySelectorAll("details > .details-body").forEach(function (body) {
      if (found) return;
      if (body.getAnimations && body.getAnimations().some(function (a) {
        return a.playState === "running";
      })) {
        found = true;
      }
    });
    return found;
  }

  /* 消息入场动画：用户消息从输入框位置滑入（fromInput=true 时按实测
     位移注入 --enter-dy），AI 消息从下往上渐显飞入。整表重建会销毁
     正在播放的动画，hasRunningMsgEnter 供重建前判断是否需等待 */
  function animateMsgEnter(el, fromInput) {
    if (reducedMotion) return;
    if (fromInput) {
      var inputRect = $("#msgInput").getBoundingClientRect();
      var elRect = el.getBoundingClientRect();
      el.style.setProperty("--enter-dy",
        Math.max(24, inputRect.top - elRect.top) + "px");
    }
    el.classList.add("msg-enter");
  }

  /* 检测聊天内是否有正在播放的消息入场动画（.msg-enter 的 WAAPI 动画） */
  function hasRunningMsgEnter(root) {
    var found = false;
    root.querySelectorAll(".msg-enter").forEach(function (el) {
      if (found) return;
      if (el.getAnimations && el.getAnimations().some(function (a) {
        return a.playState === "running";
      })) {
        found = true;
      }
    });
    return found;
  }

  /* 结构变化重建延迟标志：动画播放期间只延迟一次，期间的新消息
     由延迟回调用 lastMessages 一次性渲染（不丢帧） */
  var rebuildDeferred = false;

  /* 收起动画补偿：details 关闭瞬间（open 属性移除）Chromium 抑制内部元素
     渲染，收起方向的过渡被冻结（表现为瞬间收起、卡住的动画还会影响后续
     展开）——先撤销关闭保持内部可渲染，播放 max-height 收起动画，
     transitionend 后再真正关闭（见 trackDetailsTransition）。
     toggle 事件异步派发（Chromium 排队派发），自动收起等程序化路径
     不能依赖它，直接同步调用本函数 */
  function collapseDetails(d) {
    var body = d.querySelector(":scope > .details-body");
    if (!body) return;
    if (detailsClosing === d) return;                    /* 收起动画进行中 */
    if (!body.classList.contains("open")) return;        /* 已收起 */
    if (reducedMotion) {
      /* 降动态：无过渡，直接收起（同时关闭 open 属性保持状态一致——
         否则重建恢复逻辑会按 d.open 误判为展开态，把已收起的块
         静默重新展开） */
      body.classList.remove("open");
      d.open = false;
      return;
    }
    var h = body.offsetHeight;
    body.style.maxHeight = h + "px";
    void body.offsetWidth;      /* reflow：max-height none→px 无过渡，强制建立动画起始值 */
    body.style.maxHeight = "0px";
    detailsClosing = d;
    d.open = true;              /* 撤销关闭：保持内部可渲染直到动画播完 */
  }

  /* 思考块自动展开：推理出现/新段落/收起后恢复增长时调用。
     animate=true 播展开动画（0.3s max-height 过渡，与手动点击一致）；
     false 保留瞬时全开（同段持续增长时调用方不再触发，不会振荡）。
     手动点击的开合动画由 syncDetailsBodies/collapseDetails 提供 */
  function autoExpand(d, animate) {
    if (!d || !d.querySelector) return;
    var body = d.querySelector(":scope > .details-body");
    if (!body) return;
    if (body.classList.contains("open")) {   /* 已展开：保持即可 */
      d.open = true;
      body.style.maxHeight = "none";
      return;
    }
    d.open = true;
    if (!animate || reducedMotion) {
      /* 瞬时全开（流式增长自然撑开） */
      body.classList.add("open");
      body.style.maxHeight = "none";
      return;
    }
    /* 平滑展开：与 syncDetailsBodies 的过渡路径一致——先建立
       max-height 起始值，再测内容高触发过渡；transitionend 后由
       过渡委托解除限制 */
    body.style.maxHeight = "0px";
    void body.offsetWidth;   /* reflow 建立起始态 */
    var inner = body.querySelector(".details-inner");
    body.classList.add("open");
    body.style.maxHeight = ((inner ? inner.scrollHeight : body.scrollHeight)) + "px";
  }

  /* 思考块自动收起（推理停顿超阈值）：走 collapseDetails 的动画路径
     （含 Chromium 冻结补丁与 detailsClosing 防重入），手动/自动统一；
     直接关闭 open 属性 + 清掉 max-height 内联的瞬时版仅降动态时发生 */
  function autoCollapse(d) {
    if (!d || !d.querySelector) return;
    var body = d.querySelector(":scope > .details-body");
    if (!body) return;
    if (body.classList.contains("open") && !reducedMotion) {
      collapseDetails(d);          /* 平滑收起（动画播放期间防重入） */
      return;
    }
    if (detailsClosing === d) detailsClosing = null;   /* 打断手动收起动画的延迟关闭 */
    body.classList.remove("open");
    body.style.maxHeight = "";
    d.open = false;
  }

  /* 折叠块展开/收起状态同步：.open 类 ↔ details.open 属性。
     展开动画：max-height 0→内容高（内容高度测量一次，CSS 过渡驱动）；
     收起动画由 collapseDetails 处理（toggle 委托/自动收起调用）。
     由渲染后的调用与 details 的 toggle 事件委托共同驱动 */
  function syncDetailsBodies(root) {
    root.querySelectorAll("details").forEach(function (d) {
      var body = d.querySelector(":scope > .details-body");
      if (!body) return;
      var wasOpen = body.classList.contains("open");
      body.classList.toggle("open", d.open);
      if (!d.open || wasOpen) return;
      /* 展开转变：max-height 0 → 内容高（测量一次内容高度，流式增长时
         高度由内容自然撑开——transitionend 后解除 max-height 限制） */
      var inner = body.querySelector(".details-inner");
      body.style.maxHeight = (inner ? inner.scrollHeight : body.scrollHeight) + "px";
    });
  }

  /* 重建前记录已展开的折叠块：(消息下标, 消息内相对索引)。
     重建后按此恢复——纯全局索引在块数量变化时会错位（把别的块静默
     展开/收起，思考块表现异常）；历史消息内容不变，消息内相对索引
     精确。skipStream=true 时跳过流式消息（其块内容随流变化、索引
     不可靠，恢复走 streamOpenSummaries 的 summary 文本匹配） */
  function collectOpenDetails(chat, skipStream) {
    var opens = [];
    chat.querySelectorAll(".msg, .tool-call, .question-card").forEach(function (msgEl) {
      if (skipStream && msgEl === streamTrack.el) return;
      var msgIdx = msgEl.dataset.index;
      if (msgIdx === undefined) return;
      /* 提问卡片自身就是可折叠 details：记录其当前开合状态
         （待回答默认展开，用户手动折叠/已答展开查看都需重建后保持）。
         收起动画进行中（回答提交后自动折叠）不记录——重建后保持
         buildQuestionCard 的默认态（已答=折叠），否则会把动画中的
         卡片误记为展开、重建后被恢复成展开态（自动折叠失效） */
      if (msgEl.tagName === "DETAILS") {
        if (detailsClosing !== msgEl) {
          opens.push({ idx: msgIdx, self: true, open: msgEl.open });
        }
        return;
      }
      msgEl.querySelectorAll("details").forEach(function (d, di) {
        if (d.open) opens.push({ idx: msgIdx, di: di });
      });
    });
    return opens;
  }

  /* 重建后恢复展开态：命中 (消息下标, 消息内相对索引) 的块设回展开
     （open 属性 + .open 类同帧写入，无过渡——保持用户看到的原状
     不闪动、不重播动画；过渡能力由 transition 委托跟踪保持在线，
     用户之后的点击开合仍正常播放动画） */
  function restoreOpenDetails(chat, opens) {
    if (!opens.length) return;
    chat.querySelectorAll(".msg, .tool-call, .question-card").forEach(function (msgEl) {
      var msgIdx = msgEl.dataset.index;
      if (msgIdx === undefined) return;
      /* 提问卡片：恢复记录的开合状态（用户折叠/展开都保持） */
      if (msgEl.tagName === "DETAILS") {
        for (var s = 0; s < opens.length; s++) {
          if (opens[s].idx !== msgIdx || !opens[s].self) continue;
          msgEl.open = opens[s].open;
          if (msgEl.open) {
            var bSelf = msgEl.querySelector(":scope > .details-body");
            if (bSelf) {
              bSelf.classList.add("open");
              bSelf.style.maxHeight = "none";
            }
          }
          break;
        }
        return;
      }
      msgEl.querySelectorAll("details").forEach(function (d, di) {
        if (d.open) return;
        for (var i = 0; i < opens.length; i++) {
          if (opens[i].idx !== msgIdx || opens[i].di !== di) continue;
          d.open = true;
          var b = d.querySelector(":scope > .details-body");
          if (b) {
            b.classList.add("open");
            b.style.maxHeight = "none";  /* 恢复展开=立即全开（CSS 基态 max-height:0 需解除） */
          }
          break;
        }
      });
    });
  }

  /* 消息渲染缓存：结构帧整表重建时，未变化的历史消息直接克隆，
     跳过全量 markdown 解析（长对话/多轮工具调用收益明显）。
     renderMessage 是 (role, content) 的纯函数，缓存永远有效 */
  var msgRenderCache = {};
  var msgCacheSize = 0;
  var MSG_CACHE_MAX = 300;

  function renderMessageCached(m) {
    /* 提问卡片是交互组件（含选中状态），不参与缓存——整表重建时始终
       重建新卡片（状态由后端 answer 帧决定，前端不持久化） */
    if (m.type === "question") return renderMessage(m);
    var key = (m.role || "") + "\u0001" + (m.content || "");
    var hit = msgRenderCache[key];
    if (hit) return hit.cloneNode(true);
    var el = renderMessage(m);
    if (msgCacheSize >= MSG_CACHE_MAX) {
      /* 简单淘汰：达到上限后整表清空重建（内容寻址，命中率随后重新爬升） */
      msgRenderCache = {};
      msgCacheSize = 0;
    }
    msgRenderCache[key] = el.cloneNode(true);
    msgCacheSize++;
    return el;
  }

  function renderMessage(m) {
    /* AI 提问卡片（type=question）：结构化交互组件，DOM 构建防注入。
       状态机：pending（可作答，选项可点选）→ answered/skipped（静态展示） */
    if (m.type === "question") return buildQuestionCard(m);
    var content = m.content || "";

    if (m.role === "user") {
      var u = document.createElement("div");
      u.className = "msg user";
      u.innerHTML =
        '<div class="msg-label">你 · You</div>' +
        "<p>" + UI.esc(content).replace(/\n/g, "<br>") + "</p>" +
        '<div class="msg-actions">' +
        '<button class="msg-action" data-action="copy" title="复制消息">⧉ 复制</button>' +
        '<button class="msg-action" data-action="edit" title="修改后重新发送">✎ 修改</button>' +
        "</div>";
      return u;
    }

    /* 助手消息处理：
       1. 检查是否为纯工具执行日志块（以 <details> 开头且剔除所有折叠块后无正文）
       2. 包含回复正文、思考过程或普通文本的助手消息均渲染为标准 .msg.ai 卡片 */
    var isThink = /^<details>\s*<summary>思考过程<\/summary>/.test(content);
    var textWithoutDetails = content.replace(/<details>[\s\S]*?<\/details>/g, "").trim();
    var isPureToolCall = (content.indexOf("<details>") === 0) && !isThink && (textWithoutDetails === "");

    if (isPureToolCall) {
      var tool = document.createElement("div");
      tool.className = "tool-call";
      tool.innerHTML = renderDetailsContent(content);
      return tool;
    }

    var a = document.createElement("div");
    a.className = "msg ai";
    var htmlBody = (content.indexOf("<details>") !== -1) ? renderDetailsContent(content) : UI.md(content);
    a.innerHTML =
      '<div class="msg-label">AI · 乐理专家</div>' +
      htmlBody +
      '<div class="msg-actions">' +
      '<button class="msg-action" data-action="copy" title="复制消息">⧉ 复制</button>' +
      "</div>";
    return a;
  }

  /* ═══════════ AI 提问卡片（ask_user_question） ═══════════ */

  /* 已出现过的提问卡片 id 集合：入场动画只在首次出现时播放，
     整表重建（流式帧/chatDone 重渲染）不重播 */
  var seenQuestionIds = {};

  /* 构建提问卡片（可折叠 details，复用折叠块动画体系）：
     - 待回答：默认展开，摘要条 + 问题/选项/"其他…"输入 + 提交/跳过按钮
     - 已答/跳过：自动折叠，摘要条显示回答内容，内部只读（选中项 ✓ 高亮），
       提交/跳过按钮移除，答案锁定不可修改
     交互走 #chat 事件委托，整表重建后事件仍可用 */
  function buildQuestionCard(m) {
    var qid = m.question_id || "";
    var isAnswered = m.status === "answered" || m.status === "skipped";
    var isFirst = !seenQuestionIds[qid];

    var card = document.createElement("details");
    card.className = "question-card" + (isAnswered ? " answered" : "");
    card.dataset.questionId = qid;
    card.dataset.qStatus = m.status || "pending";
    /* 待回答默认展开；回答/跳过后自动折叠（摘要条承载状态） */
    card.open = !isAnswered;

    /* 首次出现：卡片入场动画 + 选项逐项 stagger 入场 */
    if (!isAnswered && isFirst) {
      seenQuestionIds[qid] = true;
      card.classList.add("card-enter");
    }

    /* 摘要条（summary）：等宽标签 + 状态/回答摘要；点击展开/收起 */
    var sum = document.createElement("summary");
    sum.className = "q-summary-bar";
    var sumLabel = document.createElement("span");
    sumLabel.className = "q-sum-label";
    sumLabel.textContent = "❓ AI · 向你提问";
    sum.appendChild(sumLabel);
    var sumState = document.createElement("span");
    sumState.className = "q-sum-state";
    if (isAnswered) {
      sumState.textContent = m.status === "answered"
        ? "✓ " + questionSummaryText(m)
        : "↷ 已跳过，AI 按专业判断继续";
      sumState.classList.add(m.status);
    } else {
      sumState.textContent = "待回答";
    }
    sum.appendChild(sumState);
    card.appendChild(sum);

    var body = document.createElement("div");
    body.className = "details-body";
    var inner = document.createElement("div");
    inner.className = "details-inner q-inner";

    var answersMap = {};
    (m.answers || []).forEach(function (a) {
      if (a && typeof a.question_index === "number") answersMap[a.question_index] = a;
    });

    var qlist = document.createElement("div");
    qlist.className = "q-list";
    (m.questions || []).forEach(function (q, qi) {
      var qBlock = document.createElement("div");
      qBlock.className = "q-block";
      qBlock.dataset.qIndex = qi;

      var head = document.createElement("div");
      head.className = "q-head";
      var stamp = document.createElement("span");
      stamp.className = "stamp q-stamp";
      stamp.textContent = q.header || ("Q" + (qi + 1));
      var qText = document.createElement("span");
      qText.className = "q-text";
      qText.textContent = q.question;
      head.appendChild(stamp);
      head.appendChild(qText);
      qBlock.appendChild(head);

      var opts = document.createElement("div");
      opts.className = "q-options";
      opts.dataset.multi = q.multiSelect ? "1" : "";
      (q.options || []).forEach(function (opt, oi) {
        var row = document.createElement("label");
        row.className = "q-option";
        row.dataset.optIndex = oi;
        var mark = document.createElement("span");
        mark.className = "q-mark";
        var txt = document.createElement("span");
        txt.className = "q-opt-txt";
        var name = document.createElement("span");
        name.className = "q-opt-name";
        name.textContent = opt.label;
        txt.appendChild(name);
        if (opt.description) {
          var desc = document.createElement("span");
          desc.className = "q-opt-desc";
          desc.textContent = opt.description;
          txt.appendChild(desc);
        }
        if (!isAnswered) {
          row.tabIndex = 0;
          row.setAttribute("role", q.multiSelect ? "checkbox" : "radio");
          row.setAttribute("aria-checked", "false");
        }
        row.appendChild(mark);
        row.appendChild(txt);
        opts.appendChild(row);
      });
      qBlock.appendChild(opts);

      /* "其他…"自定义输入：选中该行时展开输入框（max-height 过渡） */
      var otherRow = document.createElement("label");
      otherRow.className = "q-option q-other-toggle";
      otherRow.dataset.otherToggle = "1";
      if (!isAnswered) {
        otherRow.tabIndex = 0;
        otherRow.setAttribute("role", q.multiSelect ? "checkbox" : "radio");
        otherRow.setAttribute("aria-checked", "false");
      }
      var otherMark = document.createElement("span");
      otherMark.className = "q-mark";
      var otherTxt = document.createElement("span");
      otherTxt.className = "q-opt-name";
      otherTxt.textContent = "其他…";
      otherRow.appendChild(otherMark);
      otherRow.appendChild(otherTxt);
      var otherBox = document.createElement("div");
      otherBox.className = "q-other-box";
      var otherInput = document.createElement("input");
      otherInput.type = "text";
      otherInput.className = "q-other-input";
      otherInput.placeholder = "输入自定义回答…";
      otherBox.appendChild(otherInput);
      otherRow.appendChild(otherBox);
      qBlock.appendChild(otherRow);

      /* 已答状态：恢复选中项 ✓ 高亮与自定义输入内容（只读展示） */
      if (isAnswered) {
        var ans = answersMap[qi];
        var sel = ans ? (ans.selected || []) : [];
        var otherVal = ans ? (ans.other || "") : "";
        qBlock.querySelectorAll(".q-option").forEach(function (row) {
          var markEl = row.querySelector(".q-mark");
          if (row.dataset.otherToggle === "1") {
            if (otherVal) row.classList.add("selected");
            return;
          }
          var oi = parseInt(row.dataset.optIndex, 10);
          var lbl = ((q.options || [])[oi] || {}).label;
          if (sel.indexOf(lbl) !== -1) {
            row.classList.add("selected");
            if (markEl) markEl.textContent = "✓";
          }
        });
        if (otherVal) {
          otherBox.classList.add("open");
          otherInput.value = otherVal;
        }
      }

      qlist.appendChild(qBlock);
    });
    inner.appendChild(qlist);

    /* 历史重建的已答卡片：结构化回答不随显示列表持久化，携带原始
       工具结果文本兜底展示（展开可见完整回答内容） */
    if (isAnswered && m.result_text) {
      var rt = document.createElement("div");
      rt.className = "q-result-text";
      rt.textContent = m.result_text;
      inner.appendChild(rt);
    }

    /* 底部操作行：提交 / 跳过——仅待回答时显示，回答后移除 */
    if (!isAnswered) {
      var actions = document.createElement("div");
      actions.className = "q-actions";
      var submitBtn = document.createElement("button");
      submitBtn.type = "button";
      submitBtn.className = "btn btn-primary q-submit";
      submitBtn.textContent = "✓ 提交回答";
      var skipBtn = document.createElement("button");
      skipBtn.type = "button";
      skipBtn.className = "btn btn-secondary q-skip";
      skipBtn.textContent = "跳过，先按你的判断继续";
      actions.appendChild(submitBtn);
      actions.appendChild(skipBtn);
      inner.appendChild(actions);
    }

    body.appendChild(inner);
    card.appendChild(body);

    return card;
  }

  /* 从回答生成摘要文本（摘要条展示）：每题取第一个选中项 + 自定义输入 */
  function questionSummaryText(m) {
    var answersMap = {};
    (m.answers || []).forEach(function (a) {
      if (a && typeof a.question_index === "number") answersMap[a.question_index] = a;
    });
    var parts = [];
    (m.questions || []).forEach(function (q, qi) {
      var ans = answersMap[qi];
      if (!ans) return;
      var sel = ans.selected || [];
      if (sel.length) parts.push(sel[0]);
      var other = (ans.other || "").trim();
      if (other) parts.push(other);
    });
    return parts.join(" · ") || "已回答";
  }

  /* 从卡片 DOM 收集回答：[{question_index, selected: [label...], other: ""}] */
  function collectQuestionAnswers(card) {
    var answers = [];
    card.querySelectorAll(".q-block").forEach(function (qb) {
      var qi = parseInt(qb.dataset.qIndex, 10);
      var selected = [];
      var other = "";
      qb.querySelectorAll(".q-option").forEach(function (row) {
        if (row.dataset.otherToggle === "1") {
          var box = row.querySelector(".q-other-box");
          if (box && box.classList.contains("open")) {
            var inp = box.querySelector(".q-other-input");
            if (inp) other = inp.value;
          }
          return;
        }
        if (!row.classList.contains("selected")) return;
        var name = row.querySelector(".q-opt-name");
        if (name) selected.push(name.textContent);
      });
      answers.push({ question_index: qi, selected: selected, other: other });
    });
    return answers;
  }

  /* 提交/跳过提问：收集卡片回答 → POST /api/answer（SSE 续跑）。
     回答流期间复用 sendMessage 的忙态：停止按钮、禁用输入与卡片 */
  function submitAnswer(questionId, card, answers, skip) {
    if (chatBusy || !currentProjectId || !questionId) return;
    if (!skip) {
      var filled = answers.some(function (a) {
        return (a.selected && a.selected.length) ||
          (a.other && a.other.trim());
      });
      if (!filled) {
        UI.toast("请先选择至少一个选项，或点击「跳过」", "err");
        return;
      }
    }

    chatBusy = true;
    streamEnded = false;
    chatEpoch++;
    var epoch = chatEpoch;   /* 本会话代数：视图切换后旧流帧一律丢弃 */
    streamEpoch = epoch;     /* 收尾（chatDone）据此判定本流是否已过期 */
    abortController = new AbortController();
    var sendBtn = $("#sendBtn");
    sendBtn.textContent = "⏹ 停止";
    sendBtn.title = "停止生成（保留已输出的内容）";
    sendBtn.classList.add("stop");
    /* 回答流期间输入框保持可用（可打草稿，发送仍被忙态拦截） */
    /* 提交动画：选项区淡出压暗、选中项高亮；随后自动折叠卡片——
       后端首个帧到达后整表重建为折叠摘要态（"✓ 回答内容"），
       提交/跳过按钮随之移除，答案锁定 */
    card.classList.add("is-answering", "busy");
    if (!reducedMotion && card.open) {
      collapseDetails(card);   /* 播放收起动画（复用折叠块收起补偿） */
    } else {
      card.open = false;
    }
    /* 回答流与主对话一样是增量流式：初始化同一套流式状态，
       否则 chat_delta 帧会被 liveStreaming 守卫丢弃 */
    liveStreaming = true;
    resetStreamDelta();
    streamTrack = { el: null, norm: "", parsed: null };
    thinkingTrack = { reasoning: "", det: null, lastGrowAt: 0 };
    userToggledStream = false;
    thinkUserCollapsed = false;
    lastMessages = [];
    streamSysLines = [];

    UI.ssePost("/api/answer", {
      project_id: currentProjectId,
      question_id: questionId,
      answers: answers
    }, function (ev) {
      if (epoch !== chatEpoch) return;  /* 视图已切换/新会话已开始：丢弃旧流帧 */
      if (ev.type === "chat") {
        var messages = ev.messages || [];
        lastMessages = messages;
        renderMessages(messages);
      } else if (ev.type === "chat_delta") {
        applyStreamDeltaEvent(ev);
      } else if (ev.type === "files") {
        applyFiles(ev.files, ev.dirs);
      } else if (ev.type === "download") {
        var link = $("#newMidiLink");
        link.href = ev.url;
        link.hidden = false;
        /* 新文件诞生显式提示：链接缩在工具条里极易被错过 */
        link.classList.remove("pop");
        void link.offsetWidth;
        link.classList.add("pop");
        UI.toast("✓ AI 生成了新的 MIDI 文件，点上方「↓ 新生成的 MIDI」下载", "ok");
      } else if (ev.type === "error") {
        appendSysLine("⚠ " + ev.message);
        chatDone();
      } else if (ev.type === "done") {
        chatDone();
      }
    }, { signal: abortController.signal }).then(function () {
      chatDone();
    }).catch(function (e) {
      if (e && e.name === "AbortError") {
        chatDone();
        return;
      }
      appendSysLine("✗ " + e.message);
      chatDone();
    });
  }

  /* ═══════════ 消息操作（复制 / 修改 / 撤回） ═══════════ */

  /* 输入框随内容自动增高（最多 140px），换行后不至于挤在单行里 */
  function autoGrowInput(ta) {
    ta.style.height = "auto";
    ta.style.height = Math.min(ta.scrollHeight, 140) + "px";
  }

  /* 退出修改模式（仅重置前端状态，不回滚后端截断） */
  function resetEditUI() {
    editingIndex = -1;
    inputBeforeEdit = "";
    var bar = $("#editBar");
    if (bar) bar.hidden = true;
  }

  /* 复制文本：AI 消息去掉思考/工具折叠块，markdown 渲染后取纯文本；
     用户消息复制原文 */
  function messageCopyText(m) {
    var content = (m.content || "").replace(/<details>[\s\S]*?<\/details>/g, "");
    if (m.role === "user") return content;
    var tmp = document.createElement("div");
    tmp.innerHTML = UI.md(content);
    return (tmp.textContent || "").replace(/\n{3,}/g, "\n\n").trim();
  }

  /* clipboard API 不可用（如非安全上下文）时的兜底复制 */
  function copyTextFallback(text) {
    var ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand("copy"); } catch (e) {}
    ta.remove();
  }

  function copyMessage(m) {
    var text = messageCopyText(m);
    function done() { UI.toast("✓ 已复制到剪贴板", "ok"); }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(function () {
        copyTextFallback(text);
        done();
      });
    } else {
      copyTextFallback(text);
      done();
    }
  }

  /* ── 修改菜单 ──
     点击「✎ 修改」不再直接截断：气泡下按钮原位变为
     取消 / 撤回修改 / 撤回消息 三选项（带切换动画），选择后再执行。 */

  /* 按钮组原位切换：旧按钮淡出下移，新按钮逐个淡入上移（.enter 类控制 stagger） */
  function swapActions(actions, buttons) {
    actions.classList.add("swapping");
    setTimeout(function () {
      actions.innerHTML = "";
      buttons.forEach(function (b) { actions.appendChild(b); });
      actions.classList.remove("swapping");
    }, 170);
  }

  /* 默认按钮组：复制 / 修改（与 renderMessage 一致） */
  function buildDefaultActions() {
    var copy = document.createElement("button");
    copy.className = "msg-action enter";
    copy.dataset.action = "copy";
    copy.title = "复制消息";
    copy.textContent = "⧉ 复制";
    var edit = document.createElement("button");
    edit.className = "msg-action enter";
    edit.dataset.action = "edit";
    edit.title = "修改后重新发送";
    edit.textContent = "✎ 修改";
    return [copy, edit];
  }

  /* 修改菜单三选项：取消 / 撤回修改 / 撤回消息。
     「撤回修改」仅在该消息之后 AI 改变过本地文件（create/delete/folder）
     时可用——撤回的是 AI 的修改（文件回滚 + 对话回滚），
     AI 纯文字回复或后面无内容时禁用 */
  function buildEditMenu(hasAiChanges, hasHistory) {
    var cancel = document.createElement("button");
    cancel.className = "msg-action enter";
    cancel.dataset.action = "edit-cancel";
    cancel.title = "不做任何修改，恢复按钮";
    cancel.textContent = "取消";
    var undoAll = document.createElement("button");
    undoAll.className = "msg-action enter" + (hasAiChanges ? "" : " disabled");
    undoAll.dataset.action = "edit-undo-all";
    undoAll.title = hasAiChanges
      ? (hasHistory
          ? "把所有内容退回到这条消息发送之前，并撤销 AI 修改的文件"
          : "撤销这条消息之后 AI 修改的文件，从这条消息重新开始")
      : "该消息之后 AI 没有修改文件";
    undoAll.textContent = "撤回修改";
    var onlyMsg = document.createElement("button");
    onlyMsg.className = "msg-action enter";
    onlyMsg.dataset.action = "edit-message";
    onlyMsg.title = "只撤回上下文，进入修改（不撤回修改）";
    onlyMsg.textContent = "撤回消息";
    return [cancel, undoAll, onlyMsg];
  }

  /* 恢复默认按钮组（复制/修改）：点击「取消」或切换其他消息时调用 */
  function restoreEditMenu() {
    if (editMenuIndex < 0) return;
    var idx = editMenuIndex;
    editMenuIndex = -1;
    var msgEl = $("#chat").querySelector('.msg[data-index="' + idx + '"]');
    if (!msgEl) return;
    var actions = msgEl.querySelector(".msg-actions");
    if (!actions) return;
    actions.classList.remove("edit-menu");   /* 退出三选项菜单：不再错落浮现 */
    swapActions(actions, buildDefaultActions());
  }

  /* 点击「✎ 修改」：先查询该消息是否有可撤回的修改历史，
     再原位显示三选项（不截断、不进入编辑） */
  function showEditMenu(idx) {
    if (guardTasksActive("修改消息")) return;
    if (chatBusy) { UI.toast("回复进行中，请稍候再修改", "warn"); return; }
    if (!currentProjectId) return;
    restoreEditMenu();
    /* 令牌校验：连续点击两条消息时 edit-info 响应可能乱序返回，
       后点击的消息会被先返回的旧响应覆盖——过期响应直接丢弃 */
    var token = ++editMenuToken;
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/edit-info", { index: idx })
      .then(function (data) {
        if (token !== editMenuToken) return;
        var msgEl = $("#chat").querySelector('.msg[data-index="' + idx + '"]');
        if (!msgEl) return;
        var actions = msgEl.querySelector(".msg-actions");
        if (!actions) return;
        editMenuIndex = idx;
        actions.classList.add("edit-menu");   /* 三选项错落浮现（CSS 按 nth-child 延迟） */
        swapActions(actions, buildEditMenu(data.has_ai_file_changes, data.has_edit_history));
      })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 修改模式（撤回消息）：截断该消息之后的对话，原文填入输入框待重发；
     发送前可点输入框上方的撤回按钮恢复被截断的对话 */
  function startEdit(idx) {
    if (guardTasksActive("修改消息")) return;
    if (chatBusy) { UI.toast("回复进行中，请稍候再修改", "warn"); return; }
    if (!currentProjectId) return;
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/edit", { index: idx })
      .then(function (data) { enterEditMode(idx, data); })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 撤回修改：把所有内容退回到这条消息发送之前，再进入修改模式；
     编辑条「↩ 撤回」可撤回这次撤回（恢复执行前状态） */
  function undoEdit(idx) {
    if (guardTasksActive("撤回修改")) return;
    if (!currentProjectId) return;
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/undo-edit", { index: idx })
      .then(function (data) {
        if (data.note) UI.toast(data.note, "warn");
        enterEditMode(idx, data);
      })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 进入修改模式（共用）：重渲染截断列表 + 显示编辑条 + 原文填入输入框。
     撤回修改可能移除了 AI 创建的文件——返回的 files/dirs 同步文件列表 */
  function enterEditMode(idx, data) {
    inputBeforeEdit = $("#msgInput").value;
    renderMessages(data.messages);
    if (data.files || data.dirs) applyFiles(data.files, data.dirs);
    editingIndex = idx;
    editMenuIndex = -1;
    $("#editBar").hidden = false;
    var input = $("#msgInput");
    input.value = data.text || "";
    autoGrowInput(input);
    draftDirty = true;
    input.focus();
  }

  /* 撤回修改：恢复被截断的对话与进入编辑前的输入框内容；
     若撤回修改时 AI 文件移入了回收站，这里一并恢复并同步文件列表 */
  function recallEdit() {
    if (!currentProjectId || editingIndex < 0) return;
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/recall", {})
      .then(function (data) {
        renderMessages(data.messages);
        if (data.files || data.dirs) {
          applyFiles(data.files, data.dirs);
        } else {
          loadProjectFiles(currentProjectId);
        }
        editingIndex = -1;
        $("#editBar").hidden = true;
        var input = $("#msgInput");
        input.value = inputBeforeEdit;
        autoGrowInput(input);
        draftDirty = true;
        input.focus();
      })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  function appendSysLine(text) {
    /* 流期间的系统行先登记：chatDone 最终整表重建会清空聊天区，随后按序恢复 */
    if (chatBusy) streamSysLines.push(text);
    var chat = $("#chat");
    var line = document.createElement("div");
    line.className = "sys-line";
    /* 文案收口：裸 e.message 中的浏览器原文（Failed to fetch 等）换成可读中文 */
    line.textContent = UI.friendlyText ? UI.friendlyText(text) : text;
    chat.appendChild(line);
    chat.scrollTop = chat.scrollHeight;
  }

  /* ═══════════ 发送消息（SSE） ═══════════ */

  var abortController = null;   /* 当前回复的停止控制器 */

  /* 项目有未完成任务（进行中/需要确认）时的操作守卫 */
  function guardTasksActive(what) {
    if (window.Tasks && Tasks.hasActiveTasks(currentProjectId)) {
      UI.toast("⚠ 有任务尚未完成，请等待任务结束后再" + what, "warn");
      return true;
    }
    return false;
  }

  function stopReply() {
    if (!abortController) return;
    /* 先通知后端停止对应任务（否则断连会被视为"切页续跑"而非停止），
       再断开 SSE——停止语义与改造前一致：终止并保留已输出内容 */
    if (window.Tasks) {
      var t = Tasks.findRunningTask(currentProjectId);
      if (t) UI.postJSON("/api/tasks/" + encodeURIComponent(t.id) + "/stop", {}).catch(function () {});
    }
    abortController.abort();
  }

  function sendMessage() {
    if (chatBusy || !currentProjectId) return;
    var input = $("#msgInput");
    var message = input.value.trim();
    if (!message) return;
    /* 修改模式发送：替换被编辑的消息（AI 上下文止于截断点），
       发送瞬间退出编辑态，撤回条随之隐藏，并立刻移除被修改消息及其之后的所有旧消息 */
    var isEdit = editingIndex >= 0;
    var editIdx = editingIndex;
    editingIndex = -1;
    $("#editBar").hidden = true;
    if (isEdit && editIdx >= 0) {
      if (currentMessages.length > editIdx) {
        currentMessages = currentMessages.slice(0, editIdx);
      }
      var chatContainer = $("#chat");
      var oldEls = chatContainer.querySelectorAll(".msg, .tool-call, .question-card");
      oldEls.forEach(function (el) {
        var idx = parseInt(el.dataset.index, 10);
        if (!isNaN(idx) && idx >= editIdx) {
          el.remove();
        }
      });
    }
    /* 消息发出后立即清空输入框（不等回复结束） */
    input.value = "";
    draftDirty = false;

    chatBusy = true;
    streamEnded = false;
    chatEpoch++;
    var epoch = chatEpoch;   /* 本会话代数：视图切换后旧流帧一律丢弃 */
    streamEpoch = epoch;     /* 收尾（chatDone）据此判定本流是否已过期 */
    abortController = new AbortController();
    /* 回复期间发送按钮变为停止按钮（保持可点击）；
       输入框不再禁用——用户可预写下一条消息（发送被忙态拦截） */
    var sendBtn = $("#sendBtn");
    sendBtn.textContent = "⏹ 停止";
    sendBtn.title = "停止生成（保留已输出的内容）";
    sendBtn.classList.add("stop");

    /* 进入实时流式模式：重置增量累计器与思考块跟踪 */
    liveStreaming = true;
    resetStreamDelta();
    streamTrack = { el: null, norm: "", parsed: null };
    thinkingTrack = { reasoning: "", det: null, lastGrowAt: 0 };
    userToggledStream = false;
    thinkUserCollapsed = false;
    lastMessages = [];
    streamSysLines = [];

    var messages = [];
    messages.push({ role: "user", content: message });

    /* 不发系统提示：先渲染用户消息 + AI 三点加载动画，
       首个 chat 事件到达时整表重渲染，加载动画自然被取代 */
    var chat = $("#chat");
    var userDiv = document.createElement("div");
    userDiv.className = "msg user";
    userDiv.innerHTML = '<div class="msg-label">你 · You</div><p>' +
      UI.esc(message).replace(/\n/g, "<br>") + "</p>";
    chat.appendChild(userDiv);
    /* 用户消息入场动画：从输入框位置丝滑滑入聊天区（位移量按实测注入） */
    animateMsgEnter(userDiv, true);
    var typingEl = document.createElement("div");
    typingEl.className = "msg ai typing";
    typingEl.innerHTML = '<div class="msg-label">AI · 乐理专家</div>' +
      '<div class="typing-dots"><span></span><span></span><span></span></div>';
    chat.appendChild(typingEl);
    chat.scrollTop = chat.scrollHeight;

    function removeTyping() {
      if (typingEl.parentNode) typingEl.remove();
    }

    UI.ssePost("/api/chat", {
      project_id: currentProjectId,
      message: message,
      edit: isEdit,
      task_id: currentTaskId,
    }, function (ev) {
      if (epoch !== chatEpoch) return;  /* 视图已切换/新会话已开始：丢弃旧流帧 */
      if (ev.type === "chat") {
        messages = ev.messages || [];
        lastMessages = messages;
        renderMessages(messages);
      } else if (ev.type === "chat_delta") {
        /* 增量帧：LLM 每个输出 chunk 的轻量推送（绝对实时流式的主通道）。
           累积到 streamAccum，节流后走与全量帧相同的渲染路径 */
        applyStreamDeltaEvent(ev);
      } else if (ev.type === "files") {
        applyFiles(ev.files, ev.dirs);
      } else if (ev.type === "download") {
        var link = $("#newMidiLink");
        link.href = ev.url;
        link.hidden = false;
        /* 新文件诞生显式提示：链接缩在工具条里极易被错过 */
        link.classList.remove("pop");
        void link.offsetWidth;
        link.classList.add("pop");
        UI.toast("✓ AI 生成了新的 MIDI 文件，点上方「↓ 新生成的 MIDI」下载", "ok");
      } else if (ev.type === "error") {
        removeTyping();
        appendSysLine("⚠ " + ev.message);
        chatDone();
      } else if (ev.type === "done") {
        chatDone();
      }
    }, { signal: abortController.signal }).then(function () {
      /* 流结束兜底：正常路径 done 事件已调 chatDone（幂等）；
         后端异常提前断流/未发 done 时，这里保证界面不卡死 */
      chatDone();
    }).catch(function (e) {
      if (e && e.name === "AbortError") {
        /* 用户主动停止：静默收尾，保留已输出的内容 */
        removeTyping();
        chatDone();
        return;
      }
      removeTyping();
      appendSysLine("✗ " + e.message);
      chatDone();
    });

    /* 任务联动：立即刷新任务列表，让停止按钮/状态条尽快感知新任务 */
    if (window.Tasks) Tasks.refresh();
  }

  function chatDone() {
    if (streamEnded) return;   /* 幂等：error/done/兜底/catch 可能多次触发收尾 */
    streamEnded = true;
    resetStreamDelta();        /* 停掉未触发的增量渲染定时器，防止收尾后补帧 */
    /* 视图切换（openProject/enterProject/backToArchive）或新流开始
       （submitAnswer）都会递增 chatEpoch：此后旧流的收尾一律跳过，
       不清输入框/不动按钮/不重渲染——防止把上一个项目/任务/流的
       结果渲染进当前视图（此前此守卫为自比较死代码，恒不生效） */
    if (chatEpoch !== streamEpoch) return;
    chatBusy = false;
    liveStreaming = false;
    abortController = null;
    /* 思考完成（流结束）的确定性收起：自动收起依赖"推理停顿帧"判定，
       流末帧时序差异（末帧推理为空/重建重置 thinkingTrack）会导致
       autoCollapse 不触发——一会儿收起一会儿不收起。流结束=思考必然
       完成，未手动操作过思考块时平滑收起（与自动路径同一动画体系；
       降动态偏好下 collapseDetails 内部退化为瞬时）。随后整表重建的
       collectOpenDetails 不记录关闭中的块，重建后保持默认折叠 */
    if (!userToggledStream && streamTrack.el) {
      streamTrack.el.querySelectorAll("details").forEach(function (d) {
        if (!d.open) return;
        var sum = d.querySelector("summary");
        if (sum && sum.textContent.indexOf("思考过程") !== -1) {
          autoCollapse(d);
        }
      });
    }
    /* 流式期间已全程 markdown 渲染，结束后用最终内容刷新一遍即可；
       流期间追加的系统行（错误提示等）在重建后按序恢复——此前一旦
       追加过提示行就整体跳过重渲染，提问卡片等非流式消息会永远缺失 */
    var chat = $("#chat");
    var pending = lastMessages;
    lastMessages = [];
    var sysLines = streamSysLines;
    streamSysLines = [];
    if (pending.length) {
      var renderFinal = function () {
        renderMessages(pending);
        for (var i = 0; i < sysLines.length; i++) appendSysLine(sysLines[i]);
      };
      /* 若聊天内仍有正在播放的动画（思考块自动收起/手动收起、消息入场
         动画尚未播完），先等动画播完再整表重建——否则重建会销毁正在播
         放的动画，表现为思考块瞬间消失（折叠无动画）/消息入场被掐断 */
      if (hasRunningDetailsAnim(chat) || hasRunningMsgEnter(chat)) {
        var epochAtDone = chatEpoch;
        setTimeout(function () {
          if (chatEpoch !== epochAtDone) return; /* 延迟期间已开始新会话，放弃旧重建 */
          renderFinal();
        }, DETAILS_CLOSE_DELAY_MS + 150);
      } else {
        renderFinal();
      }
    }
    var sendBtn = $("#sendBtn");
    sendBtn.textContent = "▶ 发送";
    sendBtn.title = "发送 (Enter)";
    sendBtn.classList.remove("stop");
    sendBtn.disabled = false;
    var input = $("#msgInput");
    autoGrowInput(input);
    input.focus();
    updateScrollBtn();
  }

  /* ═══════════ 弹窗 ═══════════ */

  var modalAction = null;
  var confirmAction = null;
  var modalToken = 0;
  var modalFocusReturn = null;   /* 弹窗打开前的焦点元素（关闭后还原） */

  /* 遮罩弹窗入场：从下往上弹出（modal-in 类，可重复触发） */
  function overlayIn(overlay, token) {
    overlay.hidden = false;
    if (reducedMotion) return;
    overlay.classList.remove("modal-in", "modal-out");
    void overlay.offsetWidth;
    overlay.classList.add("modal-in");
    setTimeout(function () {
      if (modalToken === token) overlay.classList.remove("modal-in");
    }, 600);
  }

  /* 遮罩弹窗退场：向下收拢，收束完成后再隐藏 */
  function overlayOut(overlay, token) {
    if (reducedMotion) { overlay.hidden = true; return; }
    overlay.classList.remove("modal-in");
    overlay.classList.add("modal-out");
    setTimeout(function () {
      if (modalToken === token) {
        overlay.hidden = true;
        overlay.classList.remove("modal-out");
      }
    }, 240);
  }

  function showModal(title, value, action) {
    modalToken++;
    modalFocusReturn = document.activeElement;   /* 关闭后焦点还原到触发按钮 */
    $("#modalTitle").textContent = title;
    $("#modalInput").value = value || "";
    modalAction = action;
    overlayIn($("#modalOverlay"), modalToken);
    setTimeout(function () { $("#modalInput").focus(); }, 50);
  }

  /* ---- 快速任务弹窗（洞察报告 P0-4 步骤②）----
     结构化表单 1:1 还原快捷操作页（index.html）输入项：MIDI 上传解析/
     四类任务/歌词/语言对/音符输出/要求/BPM/拍号；提交 = 新建档案 +
     组装首条消息直接进入对话流；字段切换沿用 dyn-control 动画体系。 */
  var qtFunc = "chord";
  var qtNoteTable = [];      /* 解析出的音符表（与快捷操作页 noteTable 同源同构） */
  var qtFuncToken = 0;

  /* 各任务类型的字段显隐（对齐 workbench.js funcFields 的语义） */
  function qtFuncFields(fn) {
    var map = {
      chord: ["qtReqField"],
      translate: ["qtLyricsField", "qtLangField"],
      melisma: ["qtLyricsField", "qtReqField"],
      other: ["qtLyricsField", "qtNoteField", "qtReqField"],
    };
    return map[fn] || ["qtReqField"];
  }

  function qtClearDynField(f) {
    f.classList.remove("dyn-in", "dyn-out");
    f.style.removeProperty("--dyn-delay");
    f.style.removeProperty("--out-dx");
    f.style.removeProperty("--out-dy");
    f.style.removeProperty("--fly-dx");
    f.style.removeProperty("--fly-dy");
  }

  /* 切换任务类型：完整移植快捷操作页的"收拢→弹出"级联动画
     （dyn-out 朝触发按钮收拢 → 隐藏该隐藏的字段 → dyn-in 自弹窗
     底边中点起飞，级联 60ms；reduced-motion 直接切换） */
  function qtApplyFunc(btn, fn) {
    var allFields = [$("#qtLyricsField"), $("#qtLangField"), $("#qtNoteField"), $("#qtReqField")];

    /* 打开弹窗（无触发按钮）：无动画强制同步字段状态——上次会话的
       切换动画可能在收拢阶段被中途关闭，hidden 状态不保证一致 */
    if (!btn) {
      qtFunc = fn;
      var showNow = qtFuncFields(fn);
      allFields.forEach(function (f) { f.hidden = showNow.indexOf(f.id) === -1; });
      return;
    }
    if (fn === qtFunc) return;   /* 点击当前激活项：不重复动画 */

    qtFuncToken++;
    var token = qtFuncToken;

    var fns = $("#qtFuncSelector").querySelectorAll(".fn");
    fns.forEach(function (b) { b.classList.toggle("active", b === btn); });
    qtFunc = fn;
    var show = qtFuncFields(fn);

    var modal = $("#quickTaskOverlay").querySelector(".modal");
    var persistent = [$("#qtMeasureRow"), $("#qtSubmitRow")];
    var fields = allFields;
    var controls = persistent.concat(fields);

    if (reducedMotion) {
      fields.forEach(function (f) { f.hidden = show.indexOf(f.id) === -1; });
      return;
    }

    /* 按钮 Q 弹按压（切换撞击点） */
    if (btn) {
      btn.classList.add("pressed");
      setTimeout(function () {
        if (token === qtFuncToken) btn.classList.remove("pressed");
      }, 420);
    }

    /* 退场：可见控件朝按钮中心收拢（自下而上级联） */
    var br = btn ? btn.getBoundingClientRect()
      : $("#qtFuncSelector").getBoundingClientRect();
    var ox = br.left + br.width / 2;
    var oy = br.top + br.height / 2;
    var outList = controls.filter(function (f) { return !f.hidden; });
    outList.forEach(function (f, i) {
      qtClearDynField(f);
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
      if (token !== qtFuncToken) return;
      fields.forEach(function (f) { f.hidden = show.indexOf(f.id) === -1; });
      qtStartShow(token);
    }, hideMs);

    /* 进场：从弹窗底边中点起飞（提交行 → BPM/拍号 → 字段自下而上） */
    function qtStartShow(tok) {
      var sr = modal.getBoundingClientRect();
      var lx = sr.left + sr.width / 2;
      var ly = sr.bottom;
      var inList = persistent.slice().reverse().concat(
        fields.filter(function (f) { return show.indexOf(f.id) !== -1; }).slice().reverse()
      );
      inList.forEach(function (f, i) {
        qtClearDynField(f);
        var r = f.getBoundingClientRect();
        var fx = r.left + r.width / 2;
        var fy = r.top + r.height / 2;
        f.style.setProperty("--fly-dx", (fx - lx).toFixed(1) + "px");
        f.style.setProperty("--fly-dy", (fy - ly).toFixed(1) + "px");
        f.style.setProperty("--dyn-delay", (i * 60) + "ms");
        f.classList.add("dyn-in");
        setTimeout(function () {
          if (tok !== qtFuncToken) return;
          qtClearDynField(f);
        }, i * 60 + 700);
      });
    }
  }

  /* MIDI 上传解析（复用 /api/parse；对齐快捷操作页 handleFile） */
  function qtHandleFile(file) {
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
        qtNoteTable = data.note_table || [];
        $("#qtParseStatus").textContent = data.status;
        $("#qtParseStatus").className = "ok";
        $("#qtDzSub").textContent = file.name;
        UI.toast(data.status, "ok");
      })
      .catch(function (e) {
        $("#qtParseStatus").textContent = "✗ " + e.message;
        $("#qtParseStatus").className = "err";
        UI.toast("✗ " + e.message, "err");
      });
  }

  function resetQuickTaskForm() {
    qtNoteTable = [];
    var statusEl = $("#qtParseStatus");
    if (statusEl) {
      statusEl.textContent = "尚未解析";
      statusEl.className = "dim";
    }
    var subEl = $("#qtDzSub");
    if (subEl) {
      subEl.textContent = "*.mid / *.midi";
    }
    var fileInput = $("#qtMidiInput");
    if (fileInput) fileInput.value = "";
    var lyrics = $("#qtLyrics");
    if (lyrics) lyrics.value = "";
    var req = $("#qtReq");
    if (req) req.value = "";
    var origLang = $("#qtOrigLang");
    if (origLang) origLang.value = "";
    var targetLang = $("#qtTargetLang");
    if (targetLang) targetLang.value = "";
    var bpm = $("#qtBpm");
    if (bpm) bpm.value = "120";
    var ts = $("#qtTs");
    if (ts) ts.value = "4/4";
  }

  function openQuickTask() {
    modalToken++;
    modalFocusReturn = document.activeElement;
    qtApplyFunc(null, qtFunc);
    overlayIn($("#quickTaskOverlay"), modalToken);
    setTimeout(function () { $("#qtReq").focus(); }, 50);
  }

  function closeQuickTask() {
    modalToken++;
    overlayOut($("#quickTaskOverlay"), modalToken);
    restoreModalFocus();
    resetQuickTaskForm();
  }

  /* 表单 → 首条消息组装（喂给对话管线的结构化 prompt；
     音符表与快捷操作页 /api/run 的 note_table 同构，全量嵌入） */
  function qtComposeMessage() {
    var lines = [];
    var labels = { chord: "配和弦", translate: "翻译歌词", melisma: "设计转音", other: "其他要求" };
    lines.push("【快速任务 · " + labels[qtFunc] + "】");
    if (qtNoteTable.length) {
      lines.push("音符表（共 " + qtNoteTable.length + " 个音符）：");
      lines.push(qtNoteTable.join("\n"));
    }
    var lyrics = $("#qtLyrics").value.trim();
    if (lyrics) lines.push("歌词：\n" + lyrics);
    if (qtFunc === "translate") {
      lines.push("原语言：" + ($("#qtOrigLang").value.trim() || "（未指定）"));
      lines.push("目标语言：" + ($("#qtTargetLang").value.trim() || "（未指定）"));
    }
    var req = $("#qtReq").value.trim();
    if (req) lines.push("具体要求：" + req);
    if (qtFunc === "other" && $("#qtNoteOutput").checked) {
      lines.push("请输出音符数据（MIDI）。");
    }
    lines.push("BPM：" + ($("#qtBpm").value.trim() || "120") +
      "｜拍号：" + ($("#qtTs").value.trim() || "4/4"));
    return lines.join("\n");
  }

  /* 进入工作台后立刻发送组装好的首条消息。
     enterProject 自带 isTransitioning 延迟重试，但它之后的发送不会等它——
     这里统一守卫，避免返回动画期间提交导致消息被丢 */
  function enterProjectAndSend(payload, message) {
    if (isTransitioning) {
      setTimeout(function () { enterProjectAndSend(payload, message); }, 650);
      return;
    }
    enterProject(payload);
    $("#msgInput").value = message;
    sendMessage();
  }

  function qtSubmit() {
    /* 按任务类型做最小校验（与快捷操作页 runTask 一致：
       配和弦/设计转音依赖 MIDI 音符表；翻译歌词需歌词+语言对） */
    var lyrics = $("#qtLyrics").value.trim();
    var req = $("#qtReq").value.trim();
    var needsMidi = qtFunc === "chord" || qtFunc === "melisma";
    if (needsMidi && !qtNoteTable.length) {
      UI.toast("⚠ 请先解析 MIDI 文件", "warn");
      return;
    }
    if (qtFunc === "translate") {
      if (!lyrics) { UI.toast("翻译歌词需要先粘贴歌词文本", "warn"); return; }
      if (!$("#qtOrigLang").value.trim() || !$("#qtTargetLang").value.trim()) {
        UI.toast("请填写原语言与目标语言", "warn"); return;
      }
    } else if (qtFunc === "melisma") {
      if (!lyrics) { UI.toast("设计转音需要先粘贴歌词文本", "warn"); return; }
      if (!req) { UI.toast("请填写具体要求（转音风格/位置）", "warn"); return; }
    } else if (!req) {
      UI.toast("请填写具体要求", "warn"); return;
    }

    var labels = { chord: "配和弦", translate: "翻译歌词", melisma: "设计转音", other: "其他要求" };
    var now = new Date();
    var pad = function (n) { return String(n).padStart(2, "0"); };
    var stamp = (now.getMonth() + 1) + "-" + pad(now.getDate()) + " " + pad(now.getHours()) + ":" + pad(now.getMinutes());
    var message = qtComposeMessage();

    var btn = $("#qtSubmit");
    btn.disabled = true;
    UI.postJSON("/api/projects", { name: labels[qtFunc] + " " + stamp })
      .then(function (payload) {
        closeQuickTask();
        /* 任务已发起，音符表已嵌入消息——重置解析状态（取消关闭则保留，
           与快捷操作页"解析结果页面级持久"的行为一致） */
        qtNoteTable = [];
        $("#qtParseStatus").textContent = "尚未解析";
        $("#qtParseStatus").className = "dim";
        $("#qtDzSub").textContent = "*.mid / *.midi";
        reloadProjects();
        /* 进入工作台后以组装好的首条消息直接发起对话 */
        enterProjectAndSend(payload, message);
      })
      .catch(function (e) {
        UI.toast("✗ 创建快速任务失败: " + e.message, "err");
      })
      .finally(function () { btn.disabled = false; });
  }

  function hideModal() {
    modalToken++;
    modalAction = null;
    overlayOut($("#modalOverlay"), modalToken);
    restoreModalFocus();
  }

  function showConfirm(text, action) {
    modalToken++;
    modalFocusReturn = document.activeElement;   /* 关闭后焦点还原到触发按钮 */
    $("#confirmText").textContent = text;
    confirmAction = action;
    overlayIn($("#confirmOverlay"), modalToken);
    setTimeout(function () { $("#confirmOk").focus(); }, 50);
  }

  function hideConfirm() {
    modalToken++;
    confirmAction = null;
    overlayOut($("#confirmOverlay"), modalToken);
    restoreModalFocus();
  }

  /* 焦点还原：弹窗关闭后回到打开前的触发元素（键盘/读屏用户不丢失位置） */
  function restoreModalFocus() {
    var el = modalFocusReturn;
    modalFocusReturn = null;
    if (el && el.focus && document.contains(el)) {
      try { el.focus(); } catch (e) {}
    }
  }

  /* Esc 关闭弹窗（与右键菜单 Esc 行为一致） */
  function escCloseOverlays(e) {
    if (e.key !== "Escape") return;
    var modal = $("#modalOverlay");
    var confirm = $("#confirmOverlay");
    var quick = $("#quickTaskOverlay");
    if (modal && !modal.hidden) { hideModal(); }
    else if (confirm && !confirm.hidden) { hideConfirm(); }
    else if (quick && !quick.hidden) { closeQuickTask(); }
  }

  /* ═══════════ 初始化 ═══════════ */

  function init() {
    reloadProjects().catch(function (e) {
      UI.toast("✗ 加载项目列表失败: " + e.message, "err");
    });

    /* 首次使用引导：未配置 API Key 时在档案库顶部提示（此前新用户要
       等消息发送失败后才见到原始报错，且无设置入口）；工作台状态条
       同步显示 API 就绪状态 */
    UI.getJSON("/api/settings").then(function (s) {
      var hasKey = !!s.api_key;
      var stamp = $("#apiStamp");
      if (stamp) {
        stamp.textContent = hasKey ? "API: READY" : "API: NONE";
        stamp.classList.toggle("ok", hasKey);
        stamp.classList.toggle("warn", !hasKey);
      }
      var banner = $("#apiBanner");
      if (banner && !hasKey && sessionStorage.getItem("apiBannerDismissed") !== "1") {
        banner.hidden = false;
      }
    }).catch(function () { /* 后端不可达时横幅与状态条保持缺省，不打扰 */ });
    var bannerClose = $("#apiBannerClose");
    if (bannerClose) {
      bannerClose.addEventListener("click", function () {
        var banner = $("#apiBanner");
        if (banner) banner.hidden = true;
        sessionStorage.setItem("apiBannerDismissed", "1");
      });
    }

    /* 搜索 */
    var searchTimer = null;
    var searchToken = 0;   /* 请求令牌：慢响应到达时已被新输入取代则丢弃 */
    $("#searchInput").addEventListener("input", function () {
      clearTimeout(searchTimer);
      var q = this.value.trim();
      searchTimer = setTimeout(function () {
        var token = ++searchToken;
        if (!q) { reloadProjects(); return; }
        UI.getJSON("/api/projects/search?q=" + encodeURIComponent(q)).then(function (data) {
          if (token !== searchToken) return;
          if (!data.rows.length) {
            renderSearch([["未找到匹配内容", ""]], ["__none__"]);
            return;
          }
          renderSearch(data.rows, data.ids);
        }).catch(function (e) {
          if (token !== searchToken) return;
          UI.toast("✗ " + e.message, "err");
        });
      }, 250);
    });

    /* 新建档案 */
    $("#newProjectBtn").addEventListener("click", function () {
      showModal("＋ 新建档案", "", function (name) {
        return UI.postJSON("/api/projects", { name: name }).then(enterProject);
      });
    });

    /* 快速任务：结构化表单 → 新建档案 + 首条消息（洞察报告 P0-4 步骤②） */
    $("#quickTaskBtn").addEventListener("click", openQuickTask);
    $("#qtCancel").addEventListener("click", closeQuickTask);
    $("#qtSubmit").addEventListener("click", qtSubmit);
    $("#quickTaskOverlay").addEventListener("click", function (e) {
      if (e.target === this) closeQuickTask();
    });
    $("#qtFuncSelector").addEventListener("click", function (e) {
      var btn = e.target.closest(".fn");
      if (btn && btn.dataset.func) qtApplyFunc(btn, btn.dataset.func);
    });

    /* 快速任务 MIDI 输入区：点击选择 / 拖入（对齐快捷操作页 dropzone） */
    var qtDz = $("#qtDropzone");
    var qtInput = $("#qtMidiInput");
    if (qtDz && qtInput) {
      qtDz.addEventListener("click", function () { qtInput.click(); });
      qtInput.addEventListener("change", function () {
        if (qtInput.files && qtInput.files[0]) qtHandleFile(qtInput.files[0]);
        qtInput.value = "";
      });
      var qtDragCounter = 0;
      qtDz.addEventListener("dragenter", function (e) {
        e.preventDefault();
        qtDragCounter++;
        qtDz.classList.add("dragover");
      });
      qtDz.addEventListener("dragover", function (e) {
        e.preventDefault();
        qtDz.classList.add("dragover");
      });
      qtDz.addEventListener("dragleave", function (e) {
        e.preventDefault();
        qtDragCounter--;
        if (qtDragCounter <= 0) {
          qtDragCounter = 0;
          qtDz.classList.remove("dragover");
        }
      });
      qtDz.addEventListener("drop", function (e) {
        e.preventDefault();
        qtDragCounter = 0;
        qtDz.classList.remove("dragover");
        var f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
        if (f) qtHandleFile(f);
      });
    }

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

    /* 删除确认 */
    $("#confirmOk").addEventListener("click", function () {
      if (!confirmAction) return;
      var act = confirmAction;
      hideConfirm();
      act();
    });
    $("#confirmCancel").addEventListener("click", hideConfirm);
    $("#confirmOverlay").addEventListener("click", function (e) {
      if (e.target === this) hideConfirm();
    });

    /* 返回档案库 */
    $("#backBtn").addEventListener("click", backToArchive);

    /* 文件操作 */
    /* 文件清单：文件夹行点击展开/收起（复选框区域除外），并记录为
       当前选中层级（新建文件夹的目标位置）；点击空白区域回到根层级——
       事件委托，DOM 重建后依然生效 */
    $("#fileList").addEventListener("click", function (e) {
      if (e.target.closest("input")) return;
      var row = e.target.closest(".file-dir");
      if (!row) { selectDir("", null); return; }
      /* 根目录行：仅选中（不展开收起），作为拖回根目录的投放目标 */
      if (row.classList.contains("root-row")) { selectDir("", row); return; }
      var path = row.dataset.path;
      var body = row.nextElementSibling;
      if (!body || !body.classList.contains("file-dir-children")) return;
      expandedDirs[path] = !expandedDirs[path];
      row.classList.toggle("open", !!expandedDirs[path]);
      var arrow = row.querySelector(".file-dir-arrow");
      if (arrow) arrow.textContent = expandedDirs[path] ? "▾" : "▸";
      body.hidden = !expandedDirs[path];
      selectDir(path, row);
    });
    /* 新建文件夹：在当前选中的层级下创建 */
    $("#newFolderBtn").addEventListener("click", function () {
      if (!currentProjectId) return;
      var hint = selectedDir ? "（当前层级: " + selectedDir + "）" : "（当前层级: 根目录）";
      showModal("＋ 新建文件夹 " + hint, "", function (name) {
        return UI.postJSON("/api/projects/" + currentProjectId + "/folders", {
          name: name,
          parent: selectedDir,
        }).then(function (data) {
          dirsList = data.dirs || [];
          renderFiles();
          UI.toast("✓ 已创建文件夹", "ok");
        });
      });
    });
    /* 拖拽移动：文件行拖到文件夹行（或空白区域=根目录） */
    $("#fileList").addEventListener("dragstart", function (e) {
      var item = e.target.closest(".file-item");
      if (!item) return;
      var check = item.querySelector(".file-check");
      if (!check) return;
      dragFileName = check.value;
      e.dataTransfer.effectAllowed = "move";
      try { e.dataTransfer.setData("text/plain", check.value); } catch (err) {}
      item.classList.add("dragging");
    });
    $("#fileList").addEventListener("dragend", function () {
      dragFileName = null;
      UI.qsa(".drop-target", $("#fileList")).forEach(function (el) {
        el.classList.remove("drop-target");
      });
      $("#fileList").classList.remove("drop-root");
      var it = $("#fileList .file-item.dragging");
      if (it) it.classList.remove("dragging");
    });
    $("#fileList").addEventListener("dragover", function (e) {
      if (!dragFileName) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      var row = e.target.closest(".file-dir");
      UI.qsa(".drop-target", $("#fileList")).forEach(function (el) {
        el.classList.remove("drop-target");
      });
      $("#fileList").classList.remove("drop-root");
      if (row) row.classList.add("drop-target");
      else $("#fileList").classList.add("drop-root");
    });
    $("#fileList").addEventListener("drop", function (e) {
      if (!dragFileName) return;
      e.preventDefault();
      var row = e.target.closest(".file-dir");
      var target = row ? row.dataset.path : "";
      var name = dragFileName;
      dragFileName = null;
      UI.qsa(".drop-target", $("#fileList")).forEach(function (el) {
        el.classList.remove("drop-target");
      });
      $("#fileList").classList.remove("drop-root");
      moveFile(name, target);
    });
    /* 文件清单：文件夹勾选 = 全选其下文件；文件勾选后刷新祖先半选态 */
    $("#fileList").addEventListener("change", function (e) {
      var target = e.target;
      if (!target || !target.classList || !target.classList.contains("file-check")) return;
      var dirRow = null;
      if (target.classList.contains("file-dir-check")) {
        /* 文件夹自身：行内 checkbox，直接定位所在行 */
        dirRow = target.closest(".file-dir");
      } else {
        /* 文件 checkbox：所在子容器（.file-dir-children）的前一个兄弟是文件夹行 */
        var container = target.closest(".file-dir-children");
        if (container) dirRow = container.previousElementSibling;
      }
      if (dirRow) {
        if (target.classList.contains("file-dir-check")) {
          var body = dirRow.nextElementSibling;
          if (body) setDirChecked(body, target.checked);
        }
        syncDirChecks(dirRow);
      }
    });

    /* 文件清单双击：双击 .mid/.midi 文件在钢琴卷帘抽屉中打开 */
    $("#fileList").addEventListener("dblclick", function (e) {
      var fileItem = e.target.closest(".file-item");
      if (!fileItem) return;
      var check = fileItem.querySelector(".file-check");
      var fname = check ? check.value : "";
      if (!fname) return;
      var ext = fname.toLowerCase().split(".").pop();
      if (ext === "mid" || ext === "midi") {
        if (window.PianoRoll && currentProjectId) {
          window.PianoRoll.openFile(currentProjectId, fname, "");
        }
      }
    });

    /* 右键菜单：文件行 → 卷帘编辑/重命名/删除/下载；文件夹行（根目录行除外）→
       重命名/删除/下载全部。菜单项直接执行（删除走回收站，可撤销） */
    $("#fileList").addEventListener("contextmenu", function (e) {
      var fileItem = e.target.closest(".file-item");
      if (fileItem) {
        var check = fileItem.querySelector(".file-check");
        var fname = check ? check.value : "";
        if (!fname) return;
        var ext = fname.toLowerCase().split(".").pop();
        var menuItems = [];
        if (ext === "mid" || ext === "midi") {
          menuItems.push({
            label: "🎹 钢琴卷帘编辑",
            run: function () {
              if (window.PianoRoll && currentProjectId) {
                window.PianoRoll.openFile(currentProjectId, fname, "");
              }
            }
          });
        }
        menuItems.push(
          { label: "✏ 重命名", run: function () { renameFile(fname); } },
          { label: "🗑 删除", danger: true, run: function () { deleteFileByName(fname); } },
          { label: "↓ 下载", run: function () { downloadFileByName(fname); } }
        );
        openCtxMenu(e, menuItems);
        return;
      }
      var dirRow = e.target.closest(".file-dir");
      if (dirRow && !dirRow.classList.contains("root-row")) {
        var path = dirRow.dataset.path;
        openCtxMenu(e, [
          { label: "✏ 重命名", run: function () { renameFolder(path); } },
          { label: "🗑 删除", danger: true, run: function () { deleteFolder(path); } },
          { label: "↓ 下载全部", run: function () { downloadFolder(path); } },
        ]);
      }
    });
    /* 点击菜单外部 / Esc 关闭（capture 阶段：菜单内点击不会触发关闭） */
    document.addEventListener("click", function (e) {
      if (ctxMenu && !ctxMenu.contains(e.target)) closeCtxMenu();
    }, true);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && ctxMenu) closeCtxMenu();
      escCloseOverlays(e);
    });

    /* 音源库管理上传与关闭 */
    var sUploadBtn = $("#soundLibUploadBtn");
    var sUploadInput = $("#soundLibUploadInput");
    var sCloseBtn = $("#soundLibCloseBtn");
    var sModal = $("#soundLibModalOverlay");
    if (sUploadBtn && sUploadInput) {
      sUploadBtn.addEventListener("click", function () { sUploadInput.click(); });
      sUploadInput.addEventListener("change", function () {
        var file = this.files[0];
        if (!file) return;
        var reader = new FileReader();
        reader.onload = function () {
          var buf = reader.result;
          var presets;
          try {
            if (window.PianoRoll && window.SoundLibrary) {
              presets = window.PianoRoll.soundfont.parseSF2(buf).presets;
            } else {
              return;
            }
          } catch (err) {
            UI.toast("✗ 解析 SF2 失败: " + err.message, "err");
            return;
          }
          sUploadBtn.disabled = true;
          window.SoundLibrary.saveSoundFont(file.name, buf, presets).then(function () {
            UI.toast("✓ 成功导入音色库: " + file.name, "ok");
            window.PianoRoll.refreshSoundLibraryList();
          }).catch(function (err) {
            /* 大 SF2 常见 IndexedDB 配额错误：必须给出可见反馈 */
            UI.toast("✗ 导入失败: " + (err && err.message ? err.message : "存储空间不足"), "err");
          }).finally(function () {
            sUploadBtn.disabled = false;
          });
        };
        reader.readAsArrayBuffer(file);
      });
    }
    if (sCloseBtn && sModal) {
      var sClose = function () {
        /* 与 pianoroll.js 的绑定统一走退场动画（后者被本文件后加载覆盖） */
        if (window.PianoRoll && window.PianoRoll.closeModalAnimated) {
          window.PianoRoll.closeModalAnimated(sModal);
        } else {
          sModal.hidden = true;
        }
      };
      sCloseBtn.addEventListener("click", sClose);
      sModal.addEventListener("click", function (e) {
        if (e.target === this) sClose();
      });
    }

    $("#uploadBtn").addEventListener("click", function () { $("#uploadInput").click(); });
    $("#uploadInput").addEventListener("change", function () {
      var input = this;
      if (!input.files.length) return;
      /* 上传期间禁用入口，防止重复触发并发上传（响应乱序覆盖文件列表） */
      var btn = $("#uploadBtn");
      if (btn) btn.disabled = true;
      var form = new FormData();
      Array.prototype.forEach.call(input.files, function (f) { form.append("files", f); });
      UI.toast("正在上传 " + input.files.length + " 个文件…", "");
      fetch("/api/projects/" + currentProjectId + "/files", { method: "POST", body: form })
        .then(function (r) {
          if (!r.ok) return r.json().then(function (j) { throw new Error(j.detail || "上传失败"); });
          return r.json();
        })
        .then(function (data) {
          applyFiles(data.files, data.dirs);
          UI.toast("✓ 已上传 " + data.added + " 个文件", "ok");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); })
        .finally(function () {
          if (btn) btn.disabled = false;
          input.value = "";
        });
    });

    $("#deleteBtn").addEventListener("click", function () {
      var names = selectedNames();
      if (!names.length) { UI.toast("请先勾选要删除的文件", "warn"); return; }
      UI.delJSON("/api/projects/" + currentProjectId + "/files", { names: names })
        .then(function (data) {
          applyFiles(data.files, data.dirs);
          if (data.failed && data.failed.length) {
            UI.toast("✗ " + data.failed.length + " 个文件删除失败（可能被占用）", "err");
          }
        })
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
        applyFiles(data.files, data.dirs);
        UI.toast("↩ 已撤销上一步操作", "");
      }).catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });

    /* 工作区 */
    $("#openWsBtn").addEventListener("click", function () {
      if (!currentProjectId) return;
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/open")
        .then(function (data) {
          UI.toast("✓ 已打开: " + data.path, "ok");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });
    /* 工作区绑定入口：Wails 原生模式突出"选择目录"（选完即绑），
       手输路径折叠为次要入口；-browser 模式无原生对话框，手输为主。
       （洞察报告 §2.3：桌面应用强制手打路径属反人类，但 browser 模式必须保留） */
    var hasNativeFolderDialog = !!(window.go && window.go.app &&
      window.go.app.App && window.go.app.App.SelectFolderDialog);
    var wsManualRow = $("#wsManualRow");
    var wsManualToggle = $("#wsManualToggle");
    if (hasNativeFolderDialog) {
      if (wsManualRow) wsManualRow.style.display = "none";
      if (wsManualToggle) {
        wsManualToggle.hidden = false;
        wsManualToggle.addEventListener("click", function () {
          wsManualRow.style.display = (wsManualRow.style.display === "none") ? "flex" : "none";
        });
      }
    } else {
      if (wsManualRow) wsManualRow.style.display = "flex";
      if ($("#pickFolderBtn")) $("#pickFolderBtn").style.display = "none";
    }

    function bindWorkspace(path) {
      if (!path) { UI.toast("请输入工作区目录路径", "warn"); return; }
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/bind", { path: path })
        .then(function (data) {
          applyFiles(data.midi_files, data.dirs);
          renderWorkspace({ bound: true, path: data.path });
          $("#wsPathInput").value = "";
          if (data.renamed && data.renamed.length) {
            UI.toast("已绑定工作区。重名文件已加序号:\n" + data.renamed.map(function (r) { return r.join(" → "); }).join("\n"), "warn");
          } else {
            UI.toast("✓ 已绑定工作区", "ok");
          }
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    }

    $("#pickFolderBtn").addEventListener("click", function () {
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/pick-folder", {})
        .then(function (data) {
          if (!data.path) return; /* 用户取消选择 */
          if (hasNativeFolderDialog) {
            bindWorkspace(data.path); /* 原生模式：选完即绑，省一步 */
          } else {
            $("#wsPathInput").value = data.path;
          }
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });
    $("#bindBtn").addEventListener("click", function () {
      var path = $("#wsPathInput").value.trim();
      if (!path && hasNativeFolderDialog) {
        /* 输入框留空 = 直接弹系统目录选择器（桌面模式）；
           browser 模式无系统对话框，仍走手输提示 */
        $("#pickFolderBtn").click();
        return;
      }
      bindWorkspace(path);
    });
    $("#unbindBtn").addEventListener("click", function () {
      /* 解绑即从工作区回到项目内文件清单（界面立变），给一次确认 */
      showConfirm("解绑工作区后文件清单将恢复为项目内文件，确定解绑？", function () {
        UI.postJSON("/api/projects/" + currentProjectId + "/workspace/unbind")
          .then(function (data) {
            applyFiles(data.midi_files, data.dirs);
            renderWorkspace({ bound: false, path: "" });
            UI.toast("已解绑工作区", "");
          })
          .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
      });
    });
    $("#refreshWsBtn").addEventListener("click", function () {
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/refresh")
        .then(function (data) {
          applyFiles(data.midi_files, data.dirs);
          UI.toast("✓ 已刷新工作区文件", "ok");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
    });

    /* 对话 */
    $("#sendBtn").addEventListener("click", function () {
      if (chatBusy) stopReply();
      else sendMessage();
    });
    /* 用户手动展开/收起思考块后，自动展开/收起逻辑让位；同时记录手动
       收起方向（流式重建恢复时尊重，不撤销用户的收起）。
       注意：必须在 click 事件记录方向——toggle 事件会因 collapseDetails
       撤销关闭而二次派发，在 toggle 里记录会把"收起"标记误清掉。
       click 时 d.open 还是切换前的值：true = 用户即将收起，false = 展开 */
    $("#chat").addEventListener("click", function (e) {
      var sum = e.target.closest("summary");
      if (sum && sum.parentElement && sum.parentElement.tagName === "DETAILS"
          && sum.textContent.indexOf("思考过程") !== -1) {
        userToggledStream = true;
        thinkUserCollapsed = sum.parentElement.open;
      }
    });
    /* 消息操作按钮（复制 / 修改 / 修改菜单三选项）——事件委托，随整表重建存活。
       data-index 由 renderMessages 渲染时写入，据此取 currentMessages 中
       对应的消息数据（内容寻址缓存克隆不携带按钮监听器） */
    $("#chat").addEventListener("click", function (e) {
      var btn = e.target.closest("[data-action]");
      if (!btn) return;
      var msgEl = btn.closest(".msg");
      if (!msgEl) return;
      var idx = parseInt(msgEl.dataset.index, 10);
      var m = currentMessages[idx];
      if (!m) return;
      if (btn.dataset.action === "copy") {
        copyMessage(m);
      } else if (btn.dataset.action === "edit") {
        showEditMenu(idx);
      } else if (btn.dataset.action === "edit-cancel") {
        restoreEditMenu();
      } else if (btn.dataset.action === "edit-message") {
        startEdit(idx);
      } else if (btn.dataset.action === "edit-undo-all") {
        undoEdit(idx);
      }
    });
    /* AI 提问卡片交互：选项选择（单选互斥 / 多选切换）、"其他…"展开、
       提交 / 跳过——事件委托，随整表重建存活 */
    $("#chat").addEventListener("click", function (e) {
      var card = e.target.closest(".question-card");
      if (!card || card.classList.contains("busy")) return;
      var qOpt = e.target.closest(".q-option");
      if (qOpt) {
        var qb = qOpt.closest(".q-block");
        if (!qb) return;
        var qOpts = qb.querySelector(".q-options");
        var multi = qOpts && qOpts.dataset.multi === "1";
        var qmark = qOpt.querySelector(".q-mark");
        if (qOpt.dataset.otherToggle === "1") {
          /* "其他…"：切换选中并展开/收起输入框 */
          var box = qb.querySelector(".q-other-box");
          if (!box) return;
          var wasOpen = box.classList.contains("open");
          if (multi) {
            qOpt.classList.toggle("selected");
            box.classList.toggle("open", !wasOpen);
          } else {
            /* 单选：选中"其他"同时取消其他选项 */
            qb.querySelectorAll(".q-option").forEach(function (r) {
              r.classList.remove("selected");
              var mk = r.querySelector(".q-mark");
              if (mk) mk.textContent = "";
            });
            qOpt.classList.add("selected");
            box.classList.add("open");
          }
          if (!wasOpen) {
            var inp = box.querySelector(".q-other-input");
            if (inp) inp.focus();
          }
          return;
        }
        /* 普通选项：多选切换 / 单选互斥 */
        if (multi) {
          var isSel = qOpt.classList.toggle("selected");
          if (qmark) qmark.textContent = isSel ? "✓" : "";
          qOpt.setAttribute("aria-checked", isSel ? "true" : "false");
        } else {
          qb.querySelectorAll(".q-option").forEach(function (r) {
            r.classList.remove("selected");
            r.setAttribute("aria-checked", "false");
            var mk = r.querySelector(".q-mark");
            if (mk) mk.textContent = "";
          });
          qOpt.classList.add("selected");
          qOpt.setAttribute("aria-checked", "true");
          if (qmark) qmark.textContent = "✓";
        }
        return;
      }
      var submitBtn = e.target.closest(".q-submit");
      if (submitBtn) {
        submitAnswer(card.dataset.questionId, card, collectQuestionAnswers(card), false);
        return;
      }
      var skipBtn = e.target.closest(".q-skip");
      if (skipBtn) {
        submitAnswer(card.dataset.questionId, card, [], true);
      }
    });
    $("#chat").addEventListener("keydown", function (e) {
      if (e.key === " " || e.key === "Enter") {
        var opt = e.target.closest(".q-option");
        if (opt && !e.target.closest("input")) {
          e.preventDefault();
          opt.click();
        }
      }
      /* "其他…"输入框内 Enter：直接提交回答（Shift+Enter 才换行） */
      if (e.key === "Enter" && !e.shiftKey &&
          e.target.classList && e.target.classList.contains("q-other-input")) {
        var qc = e.target.closest(".question-card");
        if (qc && !qc.classList.contains("busy")) {
          e.preventDefault();
          submitAnswer(qc.dataset.questionId, qc, collectQuestionAnswers(qc), false);
        }
      }
    });
    /* 撤回修改：恢复被截断的对话与输入框内容 */
    $("#recallEditBtn").addEventListener("click", recallEdit);
    /* 折叠块 open 属性翻转后（点击/键盘/程序化）同步展开/收起动画。
       toggle 事件不冒泡，需用捕获阶段监听；Chromium 中 toggle 异步
       派发（排队任务），故收起补偿不依赖它——自动收起直接调用
       collapseDetails，此委托只兜底用户点击/键盘路径 */
    $("#chat").addEventListener("toggle", function (e) {
      var d = e.target;
      if (!d || d.tagName !== "DETAILS") return;
      if (!d.open) {
        if (detailsClosing === d) {
          /* 收起动画进行中再次点击：取消收起，反向展开 */
          detailsClosing = null;
          d.open = true;
          var body = d.querySelector(":scope > .details-body");
          if (body) {
            var inner = body.querySelector(".details-inner");
            body.style.maxHeight = (inner ? inner.scrollHeight : body.scrollHeight) + "px";
          }
          return;
        }
        collapseDetails(d);
        return;
      }
      /* 注意：collapseDetails 播放收起动画时会撤销关闭（d.open=true），
         Chromium 因此再次派发 toggle 事件进入本分支——此时绝不能清
         detailsClosing（transitionend 要靠它完成收起），只做状态同步 */
      syncDetailsBodies($("#chat"));
    }, true);
    /* 折叠块展开/收起过渡的状态跟踪：重建前据此判断是否需要等动画播完
       （见 hasRunningDetailsAnim 注释）。transition 事件会冒泡，
       用捕获阶段监听与 toggle 保持一致 */
    function trackDetailsTransition(e) {
      var t = e.target;
      if (!t || !t.classList || !t.classList.contains("details-body")) return;
      if (e.type === "transitionstart") detailsAnimBusy++;
      else if (detailsAnimBusy > 0) detailsAnimBusy--;
      if (e.type === "transitionend") {
        var det = t.closest("details");
        if (detailsClosing === det) {
          /* 收起动画完成：真正关闭（撤销期间保持 open 只是为了让
             Chromium 不抑制内部渲染，动画播完立即补上关闭） */
          detailsClosing = null;
          det.open = false;
          t.classList.remove("open");
        } else if (t.classList.contains("open")) {
          /* 展开动画完成：解除高度限制，流式内容增长时自然撑开 */
          t.style.maxHeight = "none";
        }
      } else if (e.type === "transitioncancel" && detailsClosing === t.closest("details")) {
        /* 收起动画被取消（如动画期间整表重建/快速操作）：取消延迟关闭 */
        detailsClosing = null;
      }
    }
    $("#chat").addEventListener("transitionstart", trackDetailsTransition, true);
    $("#chat").addEventListener("transitionend", trackDetailsTransition, true);
    $("#chat").addEventListener("transitioncancel", trackDetailsTransition, true);
    /* 手动滚动位置跟踪：离开底部 40px 以上时停止自动滚动（流式输出
       不再把用户拽回底部），滚回底部后恢复跟随；按钮只在离开底部时浮现。
       滚动动画进行中不刷新按钮（中途位置不贴底，刷新会让按钮闪烁） */
    $("#chat").addEventListener("scroll", function () {
      var c = $("#chat");
      nearBottom = c.scrollHeight - c.scrollTop - c.clientHeight < 40;
      if (!scrollAnimating) updateScrollBtn();
    });
    /* 动画期间用户滚轮滚动：立即打断 1 秒滚动动画，交还手动控制 */
    $("#chat").addEventListener("wheel", function () {
      if (!scrollAnimating) return;
      scrollAnimating = false;
      scrollAnimToken++;   /* 作废旧动画（step 检测令牌失效后退出） */
      updateScrollBtn();
    });
    /* 一键滚动到底部：1 秒平滑动画滚到最新消息，动画结束恢复流式自动跟随 */
    $("#scrollBottomBtn").addEventListener("click", function () {
      var c = $("#chat");
      if (reducedMotion) {
        c.scrollTop = c.scrollHeight;
        nearBottom = true;
        updateScrollBtn();
        return;
      }
      smoothScrollToBottom(c);
    });
    $("#msgInput").addEventListener("keydown", function (e) {
      /* Enter 发送；Shift+Enter 换行（textarea 默认行为）；isComposing 防止
         中文输入法选词回车时误发送 */
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
        e.preventDefault();
        sendMessage();
      }
    });
    $("#msgInput").addEventListener("input", function () {
      draftDirty = true;
      autoGrowInput(this);
    });
    /* 任务选择条：下拉切换 / 重命名 / 新建 / 删除（进行中任务可自由操作） */
    $("#taskSelect").addEventListener("change", function () {
      var tid = this.value;
      if (tid && tid !== currentTaskId) switchTask(tid);
    });
    $("#renameTaskBtn").addEventListener("click", renameCurrentTask);
    $("#newTaskBtn").addEventListener("click", createNewTask);
    $("#deleteTaskBtn").addEventListener("click", deleteCurrentTask);

    /* 离开页面时保存草稿 */
    window.addEventListener("beforeunload", function () {
      if (currentProjectId && draftDirty) {
        var input = $("#msgInput");
        if (navigator.sendBeacon) {
          /* sendBeacon 传字符串默认 text/plain，后端 DraftIn 需 JSON——
             用 Blob 显式声明 application/json，否则关页草稿必丢(422) */
          navigator.sendBeacon(
            "/api/projects/" + currentProjectId + "/draft",
            new Blob([JSON.stringify({ text: input.value })], { type: "application/json" })
          );
        } else {
          saveDraft();
        }
      }
    });
  }

  /* 任务系统桥接（纯加法）：
     - 任务面板点击任务行经 __openProject 打开对应项目工作台
     - 当前项目任务状态变化（需要确认/已完成）且无流式进行中时，
       刷新对话显示（提问卡片出现/任务完成结果可见） */
  window.__openProject = openProject;
  if (window.Tasks) {
    Tasks.onProjectTaskChange = function (changed) {
      if (!currentProjectId || chatBusy || editingIndex >= 0) return;
      var pid = currentProjectId;
      var tid = currentTaskId;
      var url = "/api/projects/" + pid + (tid ? "?task_id=" + encodeURIComponent(tid) : "");
      UI.getJSON(url).then(function (payload) {
        if (pid !== currentProjectId || chatBusy) return;
        files = payload.midi_files || [];
        renderMessages(payload.display_messages || []);
        dirsList = payload.dirs || [];
        renderFiles();
        renderTaskSwitcher(payload.tasks || []);
        Tasks.markProjectRead(pid);
      }).catch(function () {});
    };
  }

  document.addEventListener("DOMContentLoaded", init);
})();
