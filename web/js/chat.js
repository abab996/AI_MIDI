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

  /* 流式逐字渐显与思考块自动展开/收起状态 */
  var liveStreaming = false;             /* 是否处于实时流式输出（仅此时做逐字动画） */
  var streamTrack = { el: null, norm: "", parsed: null };  /* 流式消息元素 + 归一化内容 + 解析结构 */
  var thinkingTrack = { reasoning: "", det: null, lastGrowAt: 0 };  /* 推理增长跟踪（文本长度/块元素/末次增长时间） */
  var userToggledStream = false;         /* 用户手动操作过思考块后，自动逻辑让位 */
  var lastMessages = [];                 /* 最近一次 SSE chat 事件的完整消息列表（结束时重渲染用） */
  var chatEpoch = 0;                     /* 会话代数：延迟重建等异步回调据此判断是否过期 */
  var currentMessages = [];              /* 最近一次渲染的完整消息列表（消息操作按钮按索引取数） */
  var editingIndex = -1;                 /* 修改模式：正在编辑的用户消息索引（-1 = 未编辑） */
  var editMenuIndex = -1;                /* 修改菜单：当前显示「取消/撤回修改/撤回消息」的消息索引 */
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
          return UI.putJSON("/api/projects/" + p.id, { name: name }).then(reloadProjects);
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
      setTimeout(function () { input.focus(); }, 60);
    }).catch(function (e) {
      UI.toast("✗ 打开项目失败: " + e.message, "err");
      isTransitioning = false;
    });
  }

  function backToArchive() {
    /* 返回按钮始终可点：不检查 isTransitioning，避免动画卡住时无法返回 */
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
      row.className = "file-dir" + (isOpen ? " open" : "");
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
      label.title = "拖动可移动到其他文件夹";
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
    /* 文件与空文件夹都为空才显示占位；只有空文件夹时照常渲染目录树 */
    if (!files.length && !dirsList.length) {
      list.innerHTML = '<div style="padding:14px;font-size:12px;color:var(--color-ink-faint);text-align:center">（暂无文件）</div>';
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

  /* 单帧渐显动画的最大字符数：超过部分直接以纯文本追加（不建动画节点），
     避免大块文本一次性创建数千个动画元素卡死浏览器 */
  var FADE_CHAR_CAP = 200;
  /* 渐显分组大小：每 FADE_GROUP 个字符一组（一个 span 一个动画）。
     逐字建 span 在长回复时会累积上千个并发 CSS 动画导致卡顿；
     分组后每帧最多 20 个动画，视觉仍保留「打字渐显」感 */
  var FADE_GROUP = 10;

  function makeFadeSpan(text, groupIdx) {
    var span = document.createElement("span");
    span.className = "char-fade";
    span.style.setProperty("--d", Math.min(groupIdx * 40, 400) + "ms");
    span.textContent = text;
    return span;
  }

  /* 把文本分组追加为渐显 span（流式期间以纯文本呈现，结束后整体走 markdown 重渲染）。
     按码点迭代，避免 emoji 等代理对字符被拆成两个坏字；单帧超过 FADE_CHAR_CAP
     后剩余部分整体作为纯文本追加。
     动画完成后由 #chat 上的 animationend 委托把 span 还原为纯文本节点，
     避免长对话积累数千个动画元素导致卡死 */
  function appendFadeChars(container, text) {
    if (!text) return;
    var frag = document.createDocumentFragment();
    var added = 0;
    var groupIdx = 0;
    var group = "";
    var remaining = text;
    while (remaining.length) {
      if (added >= FADE_CHAR_CAP) {
        if (group) frag.appendChild(makeFadeSpan(group, groupIdx++));
        /* 超限：剩余部分整体作为纯文本追加，不做渐显动画 */
        frag.appendChild(document.createTextNode(remaining));
        break;
      }
      var cp = remaining.codePointAt(0);
      var ch = String.fromCodePoint(cp);
      remaining = remaining.slice(ch.length);
      group += ch;
      added++;
      if (group.length >= FADE_GROUP) {
        frag.appendChild(makeFadeSpan(group, groupIdx++));
        group = "";
      }
    }
    if (group) frag.appendChild(makeFadeSpan(group, groupIdx));
    container.appendChild(frag);
  }

  /* 构建流式消息元素：结构只建一次，字符逐字渐显；
     每个 <details> 折叠块独立成块（默认折叠），正文独立；
     返回 { el, parsed }，后续帧用 updateStreamingMessage 追加增量 */
  function buildStreamingMessage(content) {
    var parsed = parseStreamContent(content);
    var el = document.createElement("div");
    el.className = "msg ai streaming";
    var label = document.createElement("div");
    label.className = "msg-label";
    label.textContent = "AI · 乐理专家";
    el.appendChild(label);
    parsed.blocks.forEach(function (b) {
      var det = document.createElement("details");
      var sum = document.createElement("summary");
      sum.textContent = b.summary;
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
      el.appendChild(det);
      appendFadeChars(txt, b.body);
    });
    var ans = document.createElement("div");
    ans.className = "stream-answer";
    el.appendChild(ans);
    appendFadeChars(ans, parsed.answer);
    return { el: el, parsed: parsed };
  }

  /* 流式消息增量更新：按块索引追加新增的推理/正文字符（渐显 span 得以存活，
     不会被整表重渲染打断——上游一次返回大量内容时逐字渐显依然生效）；
     折叠块数量增长时新建对应块 */
  function updateStreamingMessage(entry, content) {
    var parsed = parseStreamContent(content);
    var el = entry.el;
    var answerEl = el.querySelector(".stream-answer");
    /* 块数增长：在正文前插入新块 */
    while (el.querySelectorAll(".details-body .stream-text").length < parsed.blocks.length) {
      var idx = el.querySelectorAll(".details-body .stream-text").length;
      var b = parsed.blocks[idx];
      var det = document.createElement("details");
      var sum = document.createElement("summary");
      sum.textContent = b.summary;
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
      el.insertBefore(det, answerEl);
      appendFadeChars(txt, b.body);
    }
    var texts = el.querySelectorAll(".details-body .stream-text");
    for (var i = 0; i < parsed.blocks.length; i++) {
      var old = entry.parsed.blocks[i];
      if (!old) continue;
      var txt = texts[i];
      if (txt && parsed.blocks[i].body.length > old.body.length) {
        appendFadeChars(txt, parsed.blocks[i].body.slice(old.body.length));
      }
    }
    if (answerEl && parsed.answer.length > entry.parsed.answer.length) {
      appendFadeChars(answerEl, parsed.answer.slice(entry.parsed.answer.length));
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

    /* ── 实时流式：最后一条消息用增量结构（逐字渐显不被打断） ── */
    if (liveStreaming && list.length) {
      var last = list[list.length - 1];
      if (last && last.role === "assistant") {
        var content = last.content || "";
        var norm = normalizeThinking(content);
        var same = !!streamTrack.el && !!streamTrack.norm
          && (norm === streamTrack.norm || norm.indexOf(streamTrack.norm) === 0);

        if (same && chat.lastElementChild === streamTrack.el) {
          /* 同一消息持续流式：只追加新增字符 */
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
                renderMessages(lastMessages.length ? lastMessages : messages);
              }, DETAILS_CLOSE_DELAY_MS);
            }
            return;
          }
          rebuildDeferred = false;
          /* 本条 AI 消息是否首次出现（首帧）：是 → 播放入场动画；
             工具块/新思考块触发的同消息重建 → 跳过（已入场过） */
          var aiIndex = list.length - 1;
          var isNewAiMsg = !streamTrack.el ||
            streamTrack.el.dataset.index !== String(aiIndex);
          /* 重建前记录已展开的 details，重建后恢复（避免每帧把用户
             展开的思考块/工具块重新收起） */
          var openIdx = [];
          chat.querySelectorAll("details").forEach(function (d, i) {
            if (d.open) openIdx.push(i);
          });
          chat.innerHTML = "";
          for (var i = 0; i < list.length - 1; i++) {
            var mEl = renderMessageCached(list[i]);
            mEl.dataset.index = i;
            chat.appendChild(mEl);
          }
          chat.querySelectorAll("details").forEach(function (d, i) {
            if (openIdx.indexOf(i) !== -1) {
              d.open = true;
              var b = d.querySelector(":scope > .details-body");
              if (b) b.classList.add("open");
            }
          });
          streamTrack = buildStreamingMessage(content);
          streamTrack.norm = norm;
          streamTrack.el.dataset.index = aiIndex;
          chat.appendChild(streamTrack.el);
          /* AI 消息入场动画：从下往上渐显飞入（仅首次出现时） */
          if (isNewAiMsg) animateMsgEnter(streamTrack.el, false);
          userToggledStream = false;
        }

        /* 思考块自动展开/收起：仅针对「思考过程」块（工具调用块保持折叠、
           不参与自动逻辑）；推理增长 → 展开；推理停顿超过 THINK_SETTLE_MS
           才收起——「思考→正文→再思考」交错流式时推理会在帧间短暂不变，
           立即收起会在推理恢复时立刻展开，反复展开/收起动画 = 聊天框抽搐。
           跟踪最后一个思考块：元素引用（区分同段重建/新段落）+ 文本长度 */
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
          var st = thinkDet.querySelector(".stream-text");
          var reasoning = (st ? st.textContent : "").trim();
          if (thinkDet !== thinkingTrack.det) {
            /* 思考块元素变化：整表重建（同段，推理未断）或多段思考的新段落。
               同段重建沿用原基线不强行展开；新段落重置基线并展开跟随 */
            var sameSegment = thinkingTrack.reasoning.length > 0 &&
              reasoning.length >= thinkingTrack.reasoning.length &&
              reasoning.indexOf(thinkingTrack.reasoning.slice(0, 40)) === 0;
            thinkingTrack.det = thinkDet;
            thinkingTrack.reasoning = reasoning;
            thinkingTrack.lastGrowAt = Date.now();
            if (!sameSegment && !userToggledStream) thinkDet.open = true;
          } else if (reasoning.length > thinkingTrack.reasoning.length) {
            thinkingTrack.reasoning = reasoning;
            thinkingTrack.lastGrowAt = Date.now();
            if (!userToggledStream) thinkDet.open = true;
          } else if (reasoning.length && reasoning.length === thinkingTrack.reasoning.length && !userToggledStream) {
            /* 推理停顿：静默超过 THINK_SETTLE_MS 才收起 */
            if (Date.now() - thinkingTrack.lastGrowAt >= THINK_SETTLE_MS) {
              thinkDet.open = false;
            }
          }
        } else {
          thinkingTrack.det = null;
          thinkingTrack.reasoning = "";
          thinkingTrack.lastGrowAt = 0;
        }

        /* 折叠块展开/收起动画同步（自动展开/收起与手动点击后都生效） */
        syncDetailsBodies(chat);
        chat.scrollTop = chat.scrollHeight;
        return;
      }
    }

    /* ── 非流式：全量渲染 ── */
    /* 流式重渲染时保持已展开的 details（工具调用等）不收起 */
    var openIdx = [];
    chat.querySelectorAll("details").forEach(function (d, i) {
      if (d.open) openIdx.push(i);
    });
    chat.innerHTML = "";
    for (var i = 0; i < list.length; i++) {
      var mEl = renderMessageCached(list[i]);
      mEl.dataset.index = i;
      chat.appendChild(mEl);
    }
    /* 恢复上一帧的展开态：details 的 open 属性 + body 的 .open 类都要恢复，
       否则重渲染后 sync 会把每个展开块误判为"刚展开"而重播动画 */
    chat.querySelectorAll("details").forEach(function (d, i) {
      if (openIdx.indexOf(i) !== -1) {
        d.open = true;
        var b = d.querySelector(":scope > .details-body");
        if (b) b.classList.add("open");
      }
    });
    streamTrack.el = null;
    streamTrack.norm = "";

    /* 折叠块展开/收起动画同步 */
    syncDetailsBodies(chat);

    chat.scrollTop = chat.scrollHeight;
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
  /* 思考块自动收起前的推理静默期：正文/工具帧交错时推理会短暂不变，
     立即收起会在推理恢复时立刻展开（反复动画 = 聊天框抽搐）；
     静默超过该时长才判定思考真正结束并收起 */
  var THINK_SETTLE_MS = 1200;
  /* 开合动画结束后的重建等待时长（grid 过渡 300ms + 余量） */
  var DETAILS_CLOSE_DELAY_MS = DETAILS_ANIM_MS + 100;

  /* 检测聊天内是否有正在播放展开/收起过渡的 details。
     结构变化整表重建会销毁旧元素上正在播放的动画（表现为思考块
     "啪"地瞬间收起/展开），重建前用它判断是否需要等动画播完。
     注意：CSS 过渡也出现在 getAnimations() 中（Chromium） */
  function hasRunningDetailsAnim(root) {
    var found = false;
    root.querySelectorAll("details > .details-body.open").forEach(function (body) {
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

  /* 折叠块展开/收起状态同步：.open 类 ↔ details.open 属性。
     CSS 的 grid-template-rows 过渡负责动画（无 JS 测量/强制布局）。
     由渲染后的调用与 details 的 toggle 事件委托共同驱动 */
  function syncDetailsBodies(root) {
    root.querySelectorAll("details").forEach(function (d) {
      var body = d.querySelector(":scope > .details-body");
      if (!body) return;
      body.classList.toggle("open", d.open);
    });
  }

  /* 消息渲染缓存：结构帧整表重建时，未变化的历史消息直接克隆，
     跳过全量 markdown 解析（长对话/多轮工具调用收益明显）。
     renderMessage 是 (role, content) 的纯函数，缓存永远有效 */
  var msgRenderCache = {};
  var msgCacheSize = 0;
  var MSG_CACHE_MAX = 300;

  function renderMessageCached(m) {
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
    var content = m.content || "";
    if (content.indexOf("<details>") === 0) {
      /* 服务端生成的折叠标签（受信任 HTML）。「思考过程」开头的折叠块
         是带思考的 AI 回复（.msg.ai，含复制按钮）；其余为工具调用日志
         （.tool-call，日志性质不设操作按钮） */
      var isThink = /^<details>\s*<summary>思考过程<\/summary>/.test(content);
      if (isThink) {
        var think = document.createElement("div");
        think.className = "msg ai";
        think.innerHTML =
          '<div class="msg-label">AI · 乐理专家</div>' +
          renderDetailsContent(content) +
          '<div class="msg-actions">' +
          '<button class="msg-action" data-action="copy" title="复制消息">⧉ 复制</button>' +
          "</div>";
        return think;
      }
      var tool = document.createElement("div");
      tool.className = "tool-call";
      tool.innerHTML = renderDetailsContent(content);
      return tool;
    }
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
    var a = document.createElement("div");
    a.className = "msg ai";
    a.innerHTML =
      '<div class="msg-label">AI · 乐理专家</div>' +
      UI.md(content) +
      '<div class="msg-actions">' +
      '<button class="msg-action" data-action="copy" title="复制消息">⧉ 复制</button>' +
      "</div>";
    return a;
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
     无修改历史时「撤回修改」禁用（不可用且给出原因提示） */
  function buildEditMenu(hasHistory) {
    var cancel = document.createElement("button");
    cancel.className = "msg-action enter";
    cancel.dataset.action = "edit-cancel";
    cancel.title = "不做任何修改，恢复按钮";
    cancel.textContent = "取消";
    var undoAll = document.createElement("button");
    undoAll.className = "msg-action enter" + (hasHistory ? "" : " disabled");
    undoAll.dataset.action = "edit-undo-all";
    undoAll.title = hasHistory
      ? "把所有内容退回到这条消息发送之前"
      : "该消息没有可撤回的修改";
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
    swapActions(actions, buildDefaultActions());
  }

  /* 点击「✎ 修改」：先查询该消息是否有可撤回的修改历史，
     再原位显示三选项（不截断、不进入编辑） */
  function showEditMenu(idx) {
    if (chatBusy) { UI.toast("回复进行中，请稍候再修改", "warn"); return; }
    if (!currentProjectId) return;
    restoreEditMenu();
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/edit-info", { index: idx })
      .then(function (data) {
        var msgEl = $("#chat").querySelector('.msg[data-index="' + idx + '"]');
        if (!msgEl) return;
        var actions = msgEl.querySelector(".msg-actions");
        if (!actions) return;
        editMenuIndex = idx;
        swapActions(actions, buildEditMenu(data.has_edit_history));
      })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 修改模式（撤回消息）：截断该消息之后的对话，原文填入输入框待重发；
     发送前可点输入框上方的撤回按钮恢复被截断的对话 */
  function startEdit(idx) {
    if (chatBusy) { UI.toast("回复进行中，请稍候再修改", "warn"); return; }
    if (!currentProjectId) return;
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/edit", { index: idx })
      .then(function (data) { enterEditMode(idx, data); })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 撤回修改：把所有内容退回到这条消息发送之前，再进入修改模式；
     编辑条「↩ 撤回」可撤回这次撤回（恢复执行前状态） */
  function undoEdit(idx) {
    if (!currentProjectId) return;
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/undo-edit", { index: idx })
      .then(function (data) { enterEditMode(idx, data); })
      .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
  }

  /* 进入修改模式（共用）：重渲染截断列表 + 显示编辑条 + 原文填入输入框 */
  function enterEditMode(idx, data) {
    inputBeforeEdit = $("#msgInput").value;
    renderMessages(data.messages);
    editingIndex = idx;
    editMenuIndex = -1;
    $("#editBar").hidden = false;
    var input = $("#msgInput");
    input.value = data.text || "";
    autoGrowInput(input);
    draftDirty = true;
    input.focus();
  }

  /* 撤回修改：恢复被截断的对话与进入编辑前的输入框内容 */
  function recallEdit() {
    if (!currentProjectId || editingIndex < 0) return;
    UI.postJSON("/api/projects/" + currentProjectId + "/messages/recall", {})
      .then(function (data) {
        renderMessages(data.messages);
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
    var chat = $("#chat");
    var line = document.createElement("div");
    line.className = "sys-line";
    line.textContent = text;
    chat.appendChild(line);
    chat.scrollTop = chat.scrollHeight;
  }

  /* ═══════════ 发送消息（SSE） ═══════════ */

  var abortController = null;   /* 当前回复的停止控制器 */

  function stopReply() {
    if (abortController) abortController.abort();
  }

  function sendMessage() {
    if (chatBusy || !currentProjectId) return;
    var input = $("#msgInput");
    var message = input.value.trim();
    if (!message) return;
    /* 修改模式发送：替换被编辑的消息（AI 上下文止于截断点），
       发送瞬间退出编辑态，撤回条随之隐藏 */
    var isEdit = editingIndex >= 0;
    editingIndex = -1;
    $("#editBar").hidden = true;
    /* 消息发出后立即清空输入框（不等回复结束） */
    input.value = "";
    draftDirty = false;

    chatBusy = true;
    chatEpoch++;
    abortController = new AbortController();
    /* 回复期间发送按钮变为停止按钮（保持可点击） */
    var sendBtn = $("#sendBtn");
    sendBtn.textContent = "⏹ 停止";
    sendBtn.classList.add("stop");
    input.disabled = true;

    /* 进入实时流式模式：重置逐字渐显与思考块跟踪 */
    liveStreaming = true;
    streamTrack = { el: null, norm: "", parsed: null };
    thinkingTrack = { reasoning: "", det: null, lastGrowAt: 0 };
    userToggledStream = false;
    lastMessages = [];

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

    UI.ssePost("/api/chat", { project_id: currentProjectId, message: message, edit: isEdit }, function (ev) {
      if (ev.type === "chat") {
        messages = ev.messages || [];
        lastMessages = messages;
        renderMessages(messages);
      } else if (ev.type === "files") {
        applyFiles(ev.files, ev.dirs);
      } else if (ev.type === "download") {
        var link = $("#newMidiLink");
        link.href = ev.url;
        link.hidden = false;
      } else if (ev.type === "error") {
        removeTyping();
        appendSysLine("⚠ " + ev.message);
      } else if (ev.type === "done") {
        chatDone();
      }
    }, { signal: abortController.signal }).catch(function (e) {
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
  }

  function chatDone() {
    chatBusy = false;
    liveStreaming = false;
    abortController = null;
    /* 流式期间以纯文本逐字渐显，结束后整体用 markdown 重渲染；
       若结束后又追加了系统提示行（错误等），跳过重渲染以免抹掉提示 */
    var chat = $("#chat");
    var pending = lastMessages;
    lastMessages = [];
    if (pending.length && chat.lastElementChild === streamTrack.el) {
      /* 若聊天内仍有正在播放的动画（思考块自动收起/手动收起、消息入场
         动画尚未播完），先等动画播完再整表重建——否则重建会销毁正在
         播放的动画，表现为思考块瞬间消失（折叠无动画）/消息入场被掐断 */
      if (hasRunningDetailsAnim(chat) || hasRunningMsgEnter(chat)) {
        var epochAtDone = chatEpoch;
        setTimeout(function () {
          if (chatEpoch !== epochAtDone) return; /* 延迟期间已开始新会话，放弃旧重建 */
          renderMessages(pending);
        }, DETAILS_CLOSE_DELAY_MS + 150);
      } else {
        renderMessages(pending);
      }
    }
    var sendBtn = $("#sendBtn");
    sendBtn.textContent = "▶ 发送";
    sendBtn.classList.remove("stop");
    sendBtn.disabled = false;
    var input = $("#msgInput");
    input.disabled = false;
    input.value = "";          /* 消息已消费，清空输入框 */
    draftDirty = false;
    input.focus();
  }

  /* ═══════════ 弹窗 ═══════════ */

  var modalAction = null;
  var confirmAction = null;
  var modalToken = 0;

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
    $("#modalTitle").textContent = title;
    $("#modalInput").value = value || "";
    modalAction = action;
    overlayIn($("#modalOverlay"), modalToken);
    setTimeout(function () { $("#modalInput").focus(); }, 50);
  }

  function hideModal() {
    modalToken++;
    modalAction = null;
    overlayOut($("#modalOverlay"), modalToken);
  }

  function showConfirm(text, action) {
    modalToken++;
    $("#confirmText").textContent = text;
    confirmAction = action;
    overlayIn($("#confirmOverlay"), modalToken);
  }

  function hideConfirm() {
    modalToken++;
    confirmAction = null;
    overlayOut($("#confirmOverlay"), modalToken);
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
          renderFiles();
          renderMessages(payload.display_messages || []);
          resetEditUI();
          dirsList = payload.dirs || [];
          renderFiles();
          renderWorkspace(payload.workspace || { bound: false, path: "" });
          $("#msgInput").value = payload.draft || "";
          $("#sendBtn").disabled = false;
          $("#newMidiLink").hidden = true;

          /* 无卡片来源：直接切换视图 + 面板内容弹入（无 VT，立即播放） */
          isTransitioning = true;
          $("#archiveView").hidden = true;
          $("#studioView").hidden = false;
          setTimeout(function () { springIn(null, null); }, 60);
          setTimeout(function () { $("#msgInput").focus(); }, 60);
          window.scrollTo(0, 0);
          setTimeout(function () { isTransitioning = false; }, 600);
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

    /* 右键菜单：文件行 → 重命名/删除/下载；文件夹行（根目录行除外）→
       重命名/删除/下载全部。菜单项直接执行（删除走回收站，可撤销） */
    $("#fileList").addEventListener("contextmenu", function (e) {
      var fileItem = e.target.closest(".file-item");
      if (fileItem) {
        var check = fileItem.querySelector(".file-check");
        var fname = check ? check.value : "";
        if (!fname) return;
        openCtxMenu(e, [
          { label: "✏ 重命名", run: function () { renameFile(fname); } },
          { label: "🗑 删除", danger: true, run: function () { deleteFileByName(fname); } },
          { label: "↓ 下载", run: function () { downloadFileByName(fname); } },
        ]);
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
    });

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
          applyFiles(data.files, data.dirs);
          UI.toast("✓ 已上传 " + data.added + " 个文件", "ok");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); })
        .finally(function () { input.value = ""; });
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
    });
    $("#unbindBtn").addEventListener("click", function () {
      UI.postJSON("/api/projects/" + currentProjectId + "/workspace/unbind")
        .then(function (data) {
          applyFiles(data.midi_files, data.dirs);
          renderWorkspace({ bound: false, path: "" });
          UI.toast("已解绑工作区", "");
        })
        .catch(function (e) { UI.toast("✗ " + e.message, "err"); });
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
    /* 用户手动展开/收起思考块后，自动展开/收起逻辑让位 */
    $("#chat").addEventListener("click", function (e) {
      var sum = e.target.closest("summary");
      if (sum && sum.parentElement && sum.parentElement.tagName === "DETAILS"
          && sum.textContent.indexOf("思考过程") !== -1) {
        userToggledStream = true;
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
    /* 撤回修改：恢复被截断的对话与输入框内容 */
    $("#recallEditBtn").addEventListener("click", recallEdit);
    /* 折叠块 open 属性翻转后（点击/键盘/程序化）同步展开/收起动画。
       toggle 事件不冒泡，需用捕获阶段监听 */
    $("#chat").addEventListener("toggle", function (e) {
      if (e.target && e.target.tagName === "DETAILS") {
        syncDetailsBodies($("#chat"));
      }
    }, true);
    /* 渐显完成后的 span 还原为纯文本节点（事件委托，避免长文本积累动画元素卡死） */
    $("#chat").addEventListener("animationend", function (e) {
      var t = e.target;
      if (t && t.classList && t.classList.contains("char-fade") && t.parentNode) {
        var tn = document.createTextNode(t.textContent);
        t.parentNode.replaceChild(tn, t);
      }
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
    $("#clearBtn").addEventListener("click", function () {
      if (!confirm("确定清空当前项目的全部对话？")) return;
      resetEditUI();
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
  }

  document.addEventListener("DOMContentLoaded", init);
})();
