/* 任务系统（纯加法）：轮询 /api/tasks，驱动档案库卡片徽标、任务列表面板、
   工作台任务状态条。任务状态严格三态：running(进行中) / needs_confirmation(需要确认)
   / completed(已完成)。所有 UI 组件从单一 store 派生，互不直接操作；
   卡片只增删 stamp 元素，绝不重建卡片（保护涟漪/落位动画）。 */
(function () {
  "use strict";
  var UI = window.UI;
  var $ = UI.qs;

  var POLL_ACTIVE_MS = 2000;   /* 有进行中/待确认任务：保持 2s 跟踪 */
  var POLL_IDLE_MS = 10000;    /* 全部空闲：降频轮询（后端已内存缓存，进一步省） */
  var PREVIEW_MAX = 40;

  var state = {
    tasks: [],            /* 服务端任务列表（创建序） */
    sortMode: "default",  /* "default" | "time" */
    currentProjectId: null,
    offline: false,       /* 轮询失败（API 挂起/断网）时置 true，恢复后清除 */
  };
  /* 面板显隐：auto = 有未完成任务自动弹出/自动收起；
     open = 用户手动展开（保持）；closed = 用户手动收起（保持） */
  var panelOverride = "auto";
  var lastSeen = {};      /* task_id -> status（状态变化检测，驱动对话刷新） */

  /* 排序偏好本地记忆 */
  try {
    var saved = localStorage.getItem("ai-midi-task-sort");
    if (saved === "time" || saved === "default") state.sortMode = saved;
  } catch (e) {}

  function countUnfinished(list) {
    var n = 0;
    list.forEach(function (t) {
      if (t.status === "running" || t.status === "needs_confirmation") n++;
    });
    return n;
  }

  /* ---------- 状态变化检测：当前项目任务发生状态转移时回调 ---------- */
  function detectChanges(next) {
    var changed = [];
    if (state.currentProjectId) {
      next.forEach(function (t) {
        var prev = lastSeen[t.id];
        if (prev && prev !== t.status && t.project_id === state.currentProjectId) {
          changed.push(t);
        }
      });
    }
    lastSeen = {};
    next.forEach(function (t) { lastSeen[t.id] = t.status; });
    return changed;
  }

  function refresh() {
    return UI.getJSON("/api/tasks").then(function (data) {
      if (state.offline) {
        state.offline = false;
        renderOfflineIndicator();
      }
      var next = (data && data.tasks) || [];
      var changed = detectChanges(next);
      state.tasks = next;
      applyBadges();
      renderPanel();
      updatePanelVisibility();
      renderStatusBar();
      if (changed.length && window.Tasks && Tasks.onProjectTaskChange) {
        try { Tasks.onProjectTaskChange(changed); } catch (e) {}
      }
    }).catch(function () {
      /* 轮询失败（API 挂起/断网）：标记离线并在面板顶部提示，
         避免界面静默冻结在旧数据上；恢复后自动清除 */
      if (!state.offline) {
        state.offline = true;
        renderOfflineIndicator();
      }
    });
  }

  /* 连接中断指示：任务面板顶部一行（轮询失败时显示，恢复后隐藏） */
  function renderOfflineIndicator() {
    var bar = $("#taskStatusBar");
    if (bar) {
      bar.hidden = !state.offline;
      bar.textContent = "⚠ 连接中断，任务状态可能已过期";
      bar.classList.toggle("task-offline", !!state.offline);
    }
    var panel = $("#taskPanel");
    var el = panel && panel.querySelector(".task-offline-line");
    if (state.offline) {
      if (!el && panel) {
        el = document.createElement("div");
        el.className = "task-offline-line";
        el.textContent = "⚠ 连接中断，任务状态可能已过期";
        panel.insertBefore(el, panel.firstChild);
      }
    } else if (el) {
      el.remove();
    }
  }

  /* ---------- 档案库卡片徽标：只增删/更新一个 stamp，不重建卡片 ---------- */
  function applyBadges() {
    UI.qsa(".proj-card").forEach(function (card) {
      var pid = card.dataset.id;
      var n = 0;
      state.tasks.forEach(function (t) {
        if (t.project_id === pid && (t.status === "running" || t.status === "needs_confirmation")) n++;
      });
      var meta = card.querySelector(".proj-meta");
      if (!meta) return;
      var badge = meta.querySelector(".stamp.task-running-badge");
      if (n > 0) {
        if (!badge) {
          badge = document.createElement("span");
          badge.className = "stamp warn task-running-badge";
          meta.appendChild(badge);
        }
        badge.textContent = n + " 个任务执行中";
      } else if (badge) {
        badge.remove();
      }
    });
  }

  /* ---------- 任务列表面板 ---------- */
  function statusLabel(s) {
    if (s === "running") return "进行中";
    if (s === "needs_confirmation") return "需要确认";
    return "已完成";
  }

  function truncate(s, n) {
    return s.length > n ? s.slice(0, n) + "…" : s;
  }

  function fmtTime(sec) {
    if (!sec) return "";
    var d = new Date(sec * 1000);
    if (isNaN(d.getTime())) return "";
    var p = function (n) { return String(n).padStart(2, "0"); };
    var hhmm = p(d.getHours()) + ":" + p(d.getMinutes());
    var now = new Date();
    var sameDay = d.getFullYear() === now.getFullYear()
      && d.getMonth() === now.getMonth()
      && d.getDate() === now.getDate();
    if (sameDay) return hhmm;
    return p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " + hhmm;
  }

  function rowHtml(t) {
    var cls = t.status === "running" ? "running"
      : (t.status === "needs_confirmation" ? "confirm" : "done");
    var unread = (t.status === "completed" && !t.read)
      ? '<span class="task-unread" title="未读"></span>' : "";
    var preview = truncate(String(t.message || "").replace(/\s+/g, " ").trim(), PREVIEW_MAX);
    return '<div class="task-row" data-id="' + UI.esc(t.id) + '">' +
      '<span class="task-st task-' + cls + '">' + statusLabel(t.status) + "</span>" +
      '<div class="task-main">' +
      '<div class="task-name">' + UI.esc(t.name || "新任务") + "</div>" +
      '<div class="task-preview">' + UI.esc(t.project_name || "未知项目") +
        (preview ? " · " + UI.esc(preview) : "") + "</div>" +
      "</div>" +
      '<span class="task-time">' + UI.esc(fmtTime(t.created_at)) + "</span>" +
      unread +
      "</div>";
  }

  function sortedGroups() {
    var list = state.tasks.slice();
    var byTime = function (a, b) { return (a.created_at || 0) - (b.created_at || 0); };
    if (state.sortMode === "time") {
      /* 时间顺序：完全按时间先后 */
      return { groups: null, list: list.sort(byTime) };
    }
    /* 默认：需要确认 → 已完成未读 → 进行中 → 已完成已读；每个大块内按时间顺序 */
    var groups = [
      { label: "需要确认", tasks: [] },
      { label: "已完成 · 未读", tasks: [] },
      { label: "进行中", tasks: [] },
      { label: "已完成 · 已读", tasks: [] },
    ];
    list.forEach(function (t) {
      var g;
      if (t.status === "needs_confirmation") g = groups[0];
      else if (t.status === "completed") g = (t.read ? groups[3] : groups[1]);
      else g = groups[2];
      g.tasks.push(t);
    });
    groups.forEach(function (g) { g.tasks.sort(byTime); });
    return { groups: groups, list: null };
  }

  /* 面板内容变化守卫：轮询不重建未变化的列表（保护面板滚动位置） */
  var lastPanelSig = "";

  function renderPanel() {
    var listEl = $("#taskPanelList");
    if (!listEl) return;
    var sig = state.sortMode + "|" + state.tasks.map(function (t) {
      return t.id + ":" + t.status + ":" + (t.read ? 1 : 0);
    }).join(",");
    if (sig === lastPanelSig) return;
    lastPanelSig = sig;
    var s = sortedGroups();
    var html = "";
    if (s.groups) {
      s.groups.forEach(function (g) {
        if (!g.tasks.length) return;
        html += '<div class="task-group-title">' + UI.esc(g.label) + "</div>";
        html += g.tasks.map(rowHtml).join("");
      });
    } else {
      html = s.list.map(rowHtml).join("");
    }
    listEl.innerHTML = html || '<div class="task-empty">暂无任务</div>';
  }

  function updatePanelVisibility() {
    var panel = $("#taskPanel");
    if (!panel) return;
    var unfinished = countUnfinished(state.tasks) > 0;
    var show = panelOverride === "open" || (panelOverride === "auto" && unfinished);
    panel.classList.toggle("open", show);
    var btn = $("#taskPanelBtn");
    if (btn) btn.classList.toggle("active", show);
    var countEl = $("#taskPanelBtnCount");
    if (countEl) {
      var n = countUnfinished(state.tasks);
      countEl.hidden = n === 0;
      countEl.textContent = n;
    }
  }

  function togglePanel() {
    var panel = $("#taskPanel");
    if (!panel) return;
    panelOverride = panel.classList.contains("open") ? "closed" : "open";
    updatePanelVisibility();
  }

  /* ---------- 工作台任务状态条（仅显示三种状态） ---------- */
  function renderStatusBar() {
    var bar = $("#taskStatusBar");
    if (!bar) return;
    var pid = state.currentProjectId;
    var r = 0, n = 0, c = 0;
    state.tasks.forEach(function (t) {
      if (t.project_id !== pid) return;
      if (t.status === "running") r++;
      else if (t.status === "needs_confirmation") n++;
      else c++;
    });
    var parts = [];
    if (r) parts.push("进行中 " + r);
    if (n) parts.push("需要确认 " + n);
    if (c) parts.push("已完成 " + c);
    bar.hidden = !parts.length;
    bar.textContent = parts.join("  ·  ");
  }

  /* ---------- 对外接口（chat.js 使用） ---------- */
  function setCurrentProject(pid) {
    state.currentProjectId = pid || null;
    renderStatusBar();
  }

  function markProjectRead(pid) {
    var any = false;
    state.tasks.forEach(function (t) {
      if (t.project_id === pid && t.status === "completed" && !t.read) {
        any = true;
        t.read = true;  /* 本地先行，避免等下一轮询 */
        UI.postJSON("/api/tasks/" + encodeURIComponent(t.id) + "/read", {}).catch(function () {});
      }
    });
    if (any) {
      renderPanel();
      updatePanelVisibility();
    }
  }

  function hasActiveTasks(pid) {
    return state.tasks.some(function (t) {
      return t.project_id === pid && t.status !== "completed";
    });
  }

  function findRunningTask(pid) {
    var found = null;
    state.tasks.some(function (t) {
      if (t.project_id === pid && t.status === "running") { found = t; return true; }
      return false;
    });
    return found;
  }

  /* 自适应轮询：活跃 2s / 空闲 10s——空闲时 0.5Hz 的全量任务请求
     纯属浪费（此前固定 2s 永不降频） */
  var pollTimer = null;
  function schedulePoll() {
    clearTimeout(pollTimer);
    var delay = countUnfinished(state.tasks) > 0 ? POLL_ACTIVE_MS : POLL_IDLE_MS;
    pollTimer = setTimeout(function () {
      if (document.hidden) { schedulePoll(); return; }
      refresh().then(schedulePoll);
    }, delay);
  }

  function init() {
    refresh().then(schedulePoll);
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden) refresh();
    });

    var btn = $("#taskPanelBtn");
    if (btn) btn.addEventListener("click", togglePanel);
    var closeBtn = $("#taskPanelClose");
    if (closeBtn) closeBtn.addEventListener("click", function () {
      panelOverride = "closed";
      updatePanelVisibility();
    });
    var sortSel = $("#taskSortSel");
    if (sortSel) {
      sortSel.value = state.sortMode;
      sortSel.addEventListener("change", function () {
        state.sortMode = sortSel.value;
        try { localStorage.setItem("ai-midi-task-sort", state.sortMode); } catch (e) {}
        renderPanel();
      });
    }
    var listEl = $("#taskPanelList");
    if (listEl) {
      /* 点击任务行 → 打开该项目并切换到该任务（打开即已读，由 chat.js 处理） */
      listEl.addEventListener("click", function (e) {
        var row = e.target.closest(".task-row");
        if (!row) return;
        var task = null;
        state.tasks.some(function (t) {
          if (t.id === row.dataset.id) { task = t; return true; }
          return false;
        });
        if (task && window.__openProject) window.__openProject(task.project_id, task.id);
      });
    }
  }

  window.Tasks = {
    refresh: refresh,
    applyBadges: applyBadges,
    setCurrentProject: setCurrentProject,
    markProjectRead: markProjectRead,
    hasActiveTasks: hasActiveTasks,
    findRunningTask: findRunningTask,
    onProjectTaskChange: null,
  };

  document.addEventListener("DOMContentLoaded", init);
})(window);
