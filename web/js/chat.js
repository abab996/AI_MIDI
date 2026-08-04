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
  var thinkingTrack = { reasoning: "" };      /* 推理增长跟踪 */
  var userToggledStream = false;         /* 用户手动操作过思考块后，自动逻辑让位 */
  var lastMessages = [];                 /* 最近一次 SSE chat 事件的完整消息列表（结束时重渲染用） */
  var chatEpoch = 0;                     /* 会话代数：延迟重建等异步回调据此判断是否过期 */

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

  /* 形态动画几何参数：--vt-x1/y1/w1/h1 = 旧快照矩形
     （打开流程=卡片，返回流程=工作台面板）。
     必须在对应视图可见、且与 VT 快照相同的滚动位置下测量；
     新快照矩形由浏览器作为 group 基准态提供，无需测量 */
  function writeMorphVars(rect) {
    var s = document.documentElement.style;
    s.setProperty("--vt-x1", rect.left + "px");
    s.setProperty("--vt-y1", rect.top + "px");
    s.setProperty("--vt-w1", rect.width + "px");
    s.setProperty("--vt-h1", rect.height + "px");
  }

  function clearMorphVars() {
    var s = document.documentElement.style;
    s.removeProperty("--vt-x1");
    s.removeProperty("--vt-y1");
    s.removeProperty("--vt-w1");
    s.removeProperty("--vt-h1");
  }

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

      /* 捕获卡片中心（涟漪原点）+ 形态动画几何（旧快照矩形）——
         必须在 archive 可见时取 rect，隐藏后 getBoundingClientRect 返回 0；
         保存为绝对值（不随滚动变化） */
      var origin = null;
      if (card) {
        var cr = card.getBoundingClientRect();
        origin = { x: cr.left + cr.width / 2, y: cr.top + cr.height / 2 };
        writeMorphVars(cr);
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
        clearMorphVars();
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

      /* 返回流程旧快照 = 工作台面板：在 archive 隐藏、studio 可见的当前布局下
         测量（与 VT 旧快照一致），写入形态动画几何 */
      var sr = studio.getBoundingClientRect();
      writeMorphVars(sr);

      /* 波纹几何（注入 VT 层）：邻居卡片在 archive 可见的同一同步窗口内测量。
         VT 运行期间真实 DOM 被快照层覆盖，真实 DOM 波纹无论如何提前触发都
         要等 VT 结束才可见——所以波纹改由 VT 伪元素播放，从 card-arrive 的
         首次撞击点（0.5s × 40% + 0.02s ≈ 220ms）起可见 */
      var waveData = null;
      if (card) {
        /* 关键：archive 隐藏时 getBoundingClientRect 返回 0。
           同步任务内短暂显示 archive 以触发布局并测量卡片真实位置，
           再立刻隐藏——浏览器未在两次赋值间渲染，无闪烁。
           studio 仍可见（springOut 即将作用其上） */
        archive.hidden = false;
        var cr = card.getBoundingClientRect();
        var cx = cr.left + cr.width / 2;
        var cy = cr.top + cr.height / 2;
        var pr = studio.getBoundingClientRect();
        var dx = cx - (pr.left + pr.width / 2);
        var dy = cy - (pr.top + pr.height / 2);
        /* 限制最大飞回距离，避免极端布局下位移过大 */
        var dist = Math.hypot(dx, dy);
        var max = 130;
        if (dist > max) { dx = dx / dist * max; dy = dy / dist * max; }
        card.style.setProperty("--land-dx", dx.toFixed(1) + "px");
        card.style.setProperty("--land-dy", dy.toFixed(1) + "px");

        if (!reducedMotion) {
          var list = [];
          /* 邻居矩形必须在 archive 仍可见的同一同步窗口内测量（隐藏后为 0） */
          UI.qsa(".proj-card", archive).forEach(function (n) {
            if (n === card) return;
            var nr = n.getBoundingClientRect();
            var nx = nr.left + nr.width / 2 - cx;
            var ny = nr.top + nr.height / 2 - cy;
            var d = Math.hypot(nx, ny);
            if (d < 1) { nx = 0; ny = 1; d = 1; }
            var push = 12;
            list.push({
              el: n,
              px: (nx / d * push).toFixed(1),
              py: (ny / d * push).toFixed(1),
              /* 延迟 = 撞击点基准 220ms + 距离 × 0.35ms/px（近的先推） */
              delay: Math.round(220 + d * 0.35)
            });
          });
          if (list.length) {
            var maxDelay = 0;
            list.forEach(function (w) { if (w.delay > maxDelay) maxDelay = w.delay; });
            /* 波纹最晚在 maxDelay+450ms 播完；vt-hold 把 VT 生命期延长到该时刻，
               保证波纹全程在 VT 层内可见、结束时真实 DOM 已静止 */
            waveData = { list: list, holdMs: maxDelay + 450 + 60 };
          }
        }
        archive.hidden = true;
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
          /* 快照前把滚动归零：VT 期间展示的 archive 即为顶部视图，
             避免 VT 结束时滚动位置跳变 */
          window.scrollTo(0, 0);
          if (waveData) {
            var css = "";
            waveData.list.forEach(function (w, i) {
              w.el.style.viewTransitionName = "vt-wave-" + i;
              css += "@keyframes vt-wave-" + i +
                " { 0% { transform: translate(0,0); }" +
                " 30% { transform: translate(" + w.px + "px," + w.py + "px); }" +
                " 100% { transform: translate(0,0); } }";
              css += "html[data-vt-flow='back']::view-transition-new(vt-wave-" + i + ")" +
                " { animation: vt-wave-" + i + " 0.45s cubic-bezier(0.18,1.1,0.3,1) both;" +
                " animation-delay: " + w.delay + "ms; }";
            });
            var holdS = (waveData.holdMs / 1000).toFixed(3);
            css += "@keyframes vt-hold { from { translate: 0 0; } to { translate: 0 0; } }";
            css += "html[data-vt-flow='back']::view-transition-new(project-panel)" +
              " { animation: card-arrive 0.5s cubic-bezier(0.34,1.45,0.64,1) 0.02s both," +
              " vt-hold " + holdS + "s linear 0.02s both; }";
            var st = document.createElement("style");
            st.id = "vt-wave-styles";
            st.textContent = css;
            document.head.appendChild(st);
          }
          clearTimeout(morphGuard);
          isTransitioning = false;
        });

        function finishBack() {
          if (card) {
            card.style.viewTransitionName = "";
            card.style.removeProperty("--land-dx");
            card.style.removeProperty("--land-dy");
          }
          if (waveData) {
            waveData.list.forEach(function (w) { w.el.style.viewTransitionName = ""; });
            var st = document.getElementById("vt-wave-styles");
            if (st) st.remove();
          }
          clearSprings();
          delete document.documentElement.dataset.vtFlow;
          isTransitioning = false;
          window.scrollTo(0, 0);
          /* 无 VT 支持的浏览器：退回真实 DOM 涟漪 */
          if (!vt && card && !reducedMotion) applyNeighborRipple(card, archive);
          clearMorphVars();
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

  /* 邻居卡片波纹（真实 DOM 回退）：仅在浏览器不支持 View Transitions 时使用。
     支持 VT 时波纹由 VT 层内的 vt-wave-* 伪元素动画承担（见 backToArchive），
     因为 VT 运行期间真实 DOM 被快照层覆盖，真实 DOM 波纹不可见 */
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

  /* 解析服务端格式化内容：<details> 思考块（摘要+推理）+ 正文 */
  function parseStreamContent(content) {
    var m = /^<details>\s*<summary>([\s\S]*?)<\/summary>([\s\S]*?)<\/details>([\s\S]*)$/.exec(content);
    if (m) return { summary: m[1], reasoning: m[2], answer: m[3] };
    return { summary: null, reasoning: null, answer: content };
  }

  /* 单帧渐显动画的最大字符数：超过部分直接以纯文本追加（不建动画节点），
     避免大块文本一次性创建数千个动画元素卡死浏览器 */
  var FADE_CHAR_CAP = 200;

  /* 把文本逐字追加为渐显 span（流式期间以纯文本呈现，结束后整体走 markdown 重渲染）。
     按码点迭代，避免 emoji 等代理对字符被拆成两个坏字；单帧超过 FADE_CHAR_CAP
     后剩余部分整体作为纯文本追加。
     动画完成后由 #chat 上的 animationend 委托把 span 还原为纯文本节点，
     避免长对话积累数千个动画元素导致卡死 */
  function appendFadeChars(container, text) {
    if (!text) return;
    var frag = document.createDocumentFragment();
    var added = 0;
    var remaining = text;
    while (remaining.length) {
      if (added >= FADE_CHAR_CAP) {
        /* 超限：剩余部分整体作为纯文本追加，不做逐字动画 */
        frag.appendChild(document.createTextNode(remaining));
        break;
      }
      var cp = remaining.codePointAt(0);
      var ch = String.fromCodePoint(cp);
      remaining = remaining.slice(ch.length);
      var span = document.createElement("span");
      span.className = "char-fade";
      span.style.setProperty("--d", Math.min(added * 7, 500) + "ms");
      span.textContent = ch;
      frag.appendChild(span);
      added++;
    }
    container.appendChild(frag);
  }

  /* 构建流式消息元素：结构只建一次，字符逐字渐显；
     返回 { el, parsed }，后续帧用 updateStreamingMessage 追加增量 */
  function buildStreamingMessage(content) {
    var parsed = parseStreamContent(content);
    var el = document.createElement("div");
    el.className = "msg ai streaming";
    var label = document.createElement("div");
    label.className = "msg-label";
    label.textContent = "AI · 乐理专家";
    el.appendChild(label);
    if (parsed.reasoning != null) {
      var det = document.createElement("details");
      var sum = document.createElement("summary");
      sum.textContent = "思考过程";
      det.appendChild(sum);
      var body = document.createElement("div");
      body.className = "details-body";
      var txt = document.createElement("div");
      txt.className = "stream-text";
      body.appendChild(txt);
      det.appendChild(body);
      el.appendChild(det);
      appendFadeChars(txt, parsed.reasoning);
    }
    var ans = document.createElement("div");
    ans.className = "stream-answer";
    el.appendChild(ans);
    appendFadeChars(ans, parsed.answer);
    return { el: el, parsed: parsed };
  }

  /* 流式消息增量更新：只追加新增的推理/正文字符（渐显 span 得以存活，
     不会被整表重渲染打断——上游一次返回大量内容时逐字渐显依然生效） */
  function updateStreamingMessage(entry, content) {
    var parsed = parseStreamContent(content);
    var el = entry.el;
    if (parsed.reasoning != null && entry.parsed.reasoning != null) {
      var txt = el.querySelector(".details-body .stream-text");
      if (txt && parsed.reasoning.length > entry.parsed.reasoning.length) {
        appendFadeChars(txt, parsed.reasoning.slice(entry.parsed.reasoning.length));
      }
    }
    var ans = el.querySelector(".stream-answer");
    if (ans && parsed.answer.length > entry.parsed.answer.length) {
      appendFadeChars(ans, parsed.answer.slice(entry.parsed.answer.length));
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
             尚未播完、工具块消息就到了），先等动画播完再重建——
             否则整表重建会销毁动画，思考块表现为瞬间收起/展开 */
          if (hasRunningDetailsAnim(chat)) {
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
          /* 重建前记录已展开的 details，重建后恢复（避免每帧把用户
             展开的思考块/工具块重新收起） */
          var openIdx = [];
          chat.querySelectorAll("details").forEach(function (d, i) {
            if (d.open) openIdx.push(i);
          });
          chat.innerHTML = "";
          for (var i = 0; i < list.length - 1; i++) {
            chat.appendChild(renderMessageCached(list[i]));
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
          chat.appendChild(streamTrack.el);
          userToggledStream = false;
        }

        /* 思考块自动展开/收起：推理增长 → 展开；推理停止且正文增长 → 收起（手动操作优先） */
        var det = streamTrack.el.querySelector("details");
        if (det) {
          var reasoning = (streamTrack.parsed.reasoning || "").trim();
          if (reasoning.length > thinkingTrack.reasoning.length) {
            if (!userToggledStream) det.open = true;
          } else if (reasoning.length && reasoning.length === thinkingTrack.reasoning.length && !userToggledStream) {
            det.open = false;
          }
          thinkingTrack.reasoning = reasoning;
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
    list.forEach(function (m) {
      chat.appendChild(renderMessageCached(m));
    });
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
        '<div class="details-body">' + UI.md(m[2]) + "</div></details>";
    }).join("\n");
  }

  /* 折叠块展开/收起动画：open → 展开到内容高度；收起 → 折叠。
     由 toggle 事件（点击/键盘/程序化改变 open 属性后触发）与每次渲染后调用。
     动画用 Web Animations API 一次性驱动：状态切换才播动画；
     已展开且内容在增长（流式）时取消动画即时跟随，避免面板逐帧脉动抽搐 */
  function cancelBodyAnim(body) {
    if (body.getAnimations) {
      body.getAnimations().forEach(function (a) { a.cancel(); });
    }
  }

  /* 折叠块展开/收起动画时长：450ms，让思考块/工具块的展开与收起过程清晰可见 */
  var DETAILS_ANIM_MS = 450;
  /* 收起动画结束后再把内容标记为跳过布局（content-visibility: hidden）；
     动画播放期间标记 collapsing，防止下一帧 sync 提前掐断动画 */
  var DETAILS_CLOSE_DELAY_MS = DETAILS_ANIM_MS + 100;

  function animateBody(body, fromH, toH, fromOp, toOp) {
    if (!body.animate) return;
    cancelBodyAnim(body);
    body.animate(
      [
        { maxHeight: fromH, opacity: fromOp },
        { maxHeight: toH, opacity: toOp },
      ],
      { duration: DETAILS_ANIM_MS, easing: "cubic-bezier(0.4, 0, 0.2, 1)", fill: "both" }
    );
  }

  /* 收起动画结束后再把内容标记为跳过布局（content-visibility: hidden）；
     动画播放期间标记 collapsing，防止下一帧 sync 提前掐断动画 */
  function scheduleClose(body) {
    if (body.dataset.collapsing) return;
    body.dataset.collapsing = "1";
    setTimeout(function () {
      delete body.dataset.collapsing;
      if (!body.classList.contains("open")) body.classList.add("closed");
    }, DETAILS_CLOSE_DELAY_MS);
  }

  /* 检测聊天内是否有正在播放展开/收起动画的 details。
     结构变化整表重建会销毁旧元素上正在播放的动画（表现为思考块
     "啪"地瞬间收起/展开），重建前用它判断是否需要等动画播完。
     注意：WAAPI 动画在 .details-body 上（animateBody 用 body.animate），
     必须查 body.getAnimations()，details 元素自身没有动画 */
  function hasRunningDetailsAnim(root) {
    var found = false;
    root.querySelectorAll("details").forEach(function (d) {
      if (found) return;
      var body = d.querySelector(":scope > .details-body");
      if (!body) return;
      if (body.dataset.collapsing) { found = true; return; }
      if (body.getAnimations && body.getAnimations().some(function (a) {
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

  function syncDetailsBodies(root) {
    root.querySelectorAll("details").forEach(function (d) {
      var body = d.querySelector(":scope > .details-body");
      if (!body) return;
      var wasOpen = body.classList.contains("open");
      if (d.open) {
        /* 展开前恢复布局参与（content-visibility 生效期间测量无效） */
        body.classList.remove("closed");
        /* 仅在展开时测量（关闭态测量会触发无效布局，且值不可靠） */
        var h = body.scrollHeight;
        var prev = parseFloat(body.style.getPropertyValue("--body-h")) || 0;
        body.style.setProperty("--body-h", h + "px");
        body.classList.add("open");
        if (!wasOpen) {
          /* 收起 → 展开：一次性过渡动画 */
          animateBody(body, "0px", h + "px", 0, 1);
        } else if (Math.abs(h - prev) > 2) {
          /* 已展开且内容增长（流式）：仅在无运行中动画时才取消旧动画并即时跟随。
             否则流式帧（25ms 一帧）会在展开动画刚启动时就把它掐断，
             导致后续展开/收起全部"啪"地跳变、看不到动画 */
          var hasRunning = body.getAnimations().some(function (a) {
            return a.playState === "running";
          });
          if (!hasRunning) cancelBodyAnim(body);
        }
      } else {
        if (wasOpen) {
          /* 展开 → 收起：一次性过渡动画（动画结束前不跳过布局） */
          animateBody(
            body,
            (parseFloat(body.style.getPropertyValue("--body-h")) || 0) + "px",
            "0px", 1, 0
          );
          scheduleClose(body);
        } else if (!body.dataset.collapsing) {
          /* 早已收起（且无收起动画在播）：直接跳过布局 */
          body.classList.add("closed");
        }
        body.classList.remove("open");
      }
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
      /* 服务端生成的折叠标签（受信任 HTML）：思考块与工具调用块统一
         渲染为卡片样式的折叠块（.tool-call，虚线边框 + 控制台底色） */
      var tool = document.createElement("div");
      tool.className = "tool-call";
      tool.innerHTML = renderDetailsContent(content);
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

  var abortController = null;   /* 当前回复的停止控制器 */

  function stopReply() {
    if (abortController) abortController.abort();
  }

  function sendMessage() {
    if (chatBusy || !currentProjectId) return;
    var input = $("#msgInput");
    var message = input.value.trim();
    if (!message) return;
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
    thinkingTrack = { reasoning: "" };
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
    var typingEl = document.createElement("div");
    typingEl.className = "msg ai typing";
    typingEl.innerHTML = '<div class="msg-label">AI · 乐理专家</div>' +
      '<div class="typing-dots"><span></span><span></span><span></span></div>';
    chat.appendChild(typingEl);
    chat.scrollTop = chat.scrollHeight;

    function removeTyping() {
      if (typingEl.parentNode) typingEl.remove();
    }

    UI.ssePost("/api/chat", { project_id: currentProjectId, message: message }, function (ev) {
      if (ev.type === "chat") {
        messages = ev.messages || [];
        lastMessages = messages;
        renderMessages(messages);
      } else if (ev.type === "files") {
        applyFiles(ev.files);
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
      /* 若聊天内仍有正在播放的动画（思考块自动收起/手动收起尚未播完），
         先等动画播完再整表重建——否则重建会销毁正在播放的动画，
         表现为思考块瞬间消失（折叠无动画） */
      if (hasRunningDetailsAnim(chat)) {
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
      /* 输入框随内容自动增高（最多 140px），换行后不至于挤在单行里 */
      var ta = this;
      ta.style.height = "auto";
      ta.style.height = Math.min(ta.scrollHeight, 140) + "px";
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
  }

  document.addEventListener("DOMContentLoaded", init);
})();
