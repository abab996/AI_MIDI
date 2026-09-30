/* 右侧素材库（P3）：采样 / 音源 / 效果器 三页签
 *
 * 规格见《架构升级实施计划》4.4：
 *   · .mat-tabs segmented 三态，切换内容上滑淡入 + 条目 Stagger 30ms；
 *   · 搜索框跨页签保留关键词；
 *   · 采样页签保留文件夹树（展开态 localStorage 记忆）+ 波形缩略图 +
 *     时长/大小/采样率（等宽）+ 所属采样包标签；拖拽到轨道仍生成音频 clip；
 *   · 音源页签 = 注册表乐器卡片网格 + [已在架]/[＋ 入架] 状态胶囊 + AI 徽标，
 *     拖到轨道 = 分配（自动入架），双击 = 试听，右键 = 入架/移出/详情；
 *   · 效果器页签 = 注册表里 category=effect 的插件，P4/P5 未就绪时给引导空态。
 *
 * 波形缩略图：为"分析"单独解码（不参与发声）——引擎模式禁止 WebAudio 回放，
 * 但看一眼波形是纯离屏分析，不违反严格路由；解码结果与峰值缓存在内存 Map。
 */
(function (window) {
  "use strict";

  var UI = window.UI;

  var TABS = [
    { id: "sample", label: "采样" },
    { id: "source", label: "音源" },
    { id: "fx", label: "效果器" }
  ];

  var TREE_EXPANDED_KEY = "melodymate-material-tree";
  var TAB_KEY = "melodymate-material-tab";
  var THUMB_W = 48;
  var THUMB_H = 22;
  var MAX_CHILDREN = 100;   // 单目录先渲染这么多，其余折叠成"显示更多"

  /* 模块状态：页面内单例（素材库只有一个实例） */
  var state: any = {
    tab: "sample",
    query: "",
    dirs: [],
    expanded: null,
    treeCache: {},
    packs: [],
    info: {},          // 分析结果缓存：absPath → {peaks,duration,sampleRate,missing}
    infoInflight: {},
    decodeQueue: [],
    decoding: 0
  };

  function loadTab() {
    try {
      var saved = localStorage.getItem(TAB_KEY);
      for (var i = 0; i < TABS.length; i++) if (TABS[i].id === saved) return saved;
    } catch (e) {}
    return "sample";
  }

  function saveTab(tab: string) {
    try { localStorage.setItem(TAB_KEY, tab); } catch (e) {}
  }

  function loadExpanded() {
    try { return JSON.parse(localStorage.getItem(TREE_EXPANDED_KEY) || "{}") || {}; }
    catch (e) { return {}; }
  }

  function saveExpanded() {
    try { localStorage.setItem(TREE_EXPANDED_KEY, JSON.stringify(state.expanded || {})); } catch (e) {}
  }

  function nodeKey(dir: string, sub: string) {
    return sub ? dir + "/" + sub : dir;
  }

  function joinAbs(dir: string, sub: string, name: string) {
    var parts = [dir];
    if (sub) parts.push(sub);
    parts.push(name);
    return parts.filter(Boolean).join("/");
  }

  /* ═══════════ 分析（波形 / 时长 / 采样率） ═══════════ */

  /** analysisContext 只用于解码分析的 AudioContext：
      引擎模式下应用不建 WebAudio 发声链，但波形缩略图需要离线解码，
      这里单独取共享上下文（不接任何输出，不产生声音）。 */
  function analysisContext() {
    try {
      if (window.SharedAudio && window.SharedAudio.get) return window.SharedAudio.get();
    } catch (e) {}
    return null;
  }

  function computePeaks(buffer: any) {
    var ch = buffer.getChannelData(0);
    var buckets = THUMB_W;
    var step = Math.max(1, Math.floor(ch.length / buckets));
    var out = new Array(buckets);
    for (var i = 0; i < buckets; i++) {
      var start = i * step;
      var end = Math.min(ch.length, start + step);
      var peak = 0;
      for (var j = start; j < end; j += 4) {   // 每 4 个采样取一次：够画缩略图
        var v = ch[j] < 0 ? -ch[j] : ch[j];
        if (v > peak) peak = v;
      }
      out[i] = peak;
    }
    return out;
  }

  /** analyze 取（或拉取）一个音频文件的分析数据。
      并发限 2：素材树里几十个文件同时解码会把首帧拖住。 */
  function analyze(absPath: string) {
    if (state.info[absPath]) return Promise.resolve(state.info[absPath]);
    if (state.infoInflight[absPath]) return state.infoInflight[absPath];
    var p = new Promise(function (resolve) {
      state.decodeQueue.push({ path: absPath, resolve: resolve });
      pumpQueue();
    });
    state.infoInflight[absPath] = p;
    return p;
  }

  function pumpQueue() {
    while (state.decoding < 2 && state.decodeQueue.length) {
      var job = state.decodeQueue.shift();
      state.decoding++;
      decodeOne(job.path).then(function (info: any) {
        state.info[job.path] = info;
        job.resolve(info);
      }).catch(function () {
        state.info[job.path] = { missing: true };
        job.resolve(state.info[job.path]);
      }).then(function () {
        state.decoding--;
        pumpQueue();
      });
    }
  }

  function decodeOne(absPath: string) {
    var ctx = analysisContext();
    if (!ctx) return Promise.resolve({ missing: true });
    var actx: any = ctx;
    return fetch("/api/arrangement/audio?p=" + encodeURIComponent(absPath))
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.arrayBuffer();
      })
      .then(function (ab) { return actx.decodeAudioData(ab); })
      .then(function (buffer: any) {
        return {
          peaks: computePeaks(buffer),
          duration: buffer.duration,
          sampleRate: buffer.sampleRate,
          channels: buffer.numberOfChannels
        };
      });
  }

  function fmtDuration(sec: number) {
    if (!sec && sec !== 0) return "—";
    var m = Math.floor(sec / 60);
    var s = sec - m * 60;
    var str = s.toFixed(1);
    if (s < 10) str = "0" + str;
    return m + ":" + str;
  }

  /* ═══════════ 骨架：页签 / 搜索 / 提示 ═══════════ */

  function el(tag: string, cls: string, text?: string) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  function baseName(p: string) {
    return String(p || "").split(/[\\/]/).pop() || "";
  }

  /** bind 绑定页签、搜索与"＋ 文件夹"（DOM 就绪后调用一次） */
  function bind(ctrl: any) {
    state.tab = loadTab();
    var tabs = document.getElementById("matTabs");
    if (tabs) {
      UI.qsa(".mat-tab", tabs).forEach(function (btn: any) {
        btn.addEventListener("click", function () {
          setTab(ctrl, btn.getAttribute("data-tab") || "sample");
        });
        btn.addEventListener("keydown", function (e: any) {
          if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
          e.preventDefault();
          var order = TABS.map(function (t) { return t.id; });
          var idx = order.indexOf(state.tab);
          var next = order[(idx + (e.key === "ArrowRight" ? 1 : order.length - 1)) % order.length];
          setTab(ctrl, next);
          var btn2 = tabs!.querySelector('.mat-tab[data-tab="' + next + '"]');
          if (btn2) (btn2 as HTMLElement).focus();
        });
      });
    }

    var search = document.getElementById("matSearch") as HTMLInputElement | null;
    if (search) {
      var timer: any = null;
      search.addEventListener("input", function () {
        clearTimeout(timer);
        timer = setTimeout(function () {
          state.query = search!.value || "";
          render(ctrl);
        }, 160);
      });
      search.addEventListener("keydown", function (e: any) {
        if (e.key === "Escape" && search!.value) {
          e.stopPropagation();
          search!.value = "";
          state.query = "";
          render(ctrl);
        }
      });
    }

    var addBtn = document.getElementById("arrAddDirBtn");
    if (addBtn) {
      addBtn.addEventListener("click", function () { addFolder(ctrl); });
    }
    setTab(ctrl, state.tab, true);
  }

  function setTab(ctrl: any, tab: string, silent?: boolean) {
    state.tab = tab;
    if (!silent) saveTab(tab);
    var tabs = document.getElementById("matTabs");
    if (tabs) {
      UI.qsa(".mat-tab", tabs).forEach(function (btn: any) {
        var on = btn.getAttribute("data-tab") === tab;
        btn.classList.toggle("active", on);
        btn.setAttribute("aria-selected", on ? "true" : "false");
      });
    }
    var hint = document.getElementById("matHint");
    if (hint) hint.textContent = hintFor(tab);
    render(ctrl);
  }

  function hintFor(tab: string) {
    if (tab === "source") return "⇱ 拖拽音源到轨道即分配（自动加入乐器箱）· 双击试听";
    if (tab === "fx") return "效果器装载到混音台 FX 槽：混音台点空槽 →「装载插件」选择";
    return "⇱ 拖拽音频到轨道创建音频 Clip · 双击试听";
  }

  /* ═══════════ 渲染入口 ═══════════ */

  function render(ctrl: any) {
    var body = document.getElementById("matBody");
    if (!body) return;
    body.innerHTML = "";
    body.dataset.tab = state.tab;
    /* 切换动效：内容上滑淡入（0.22s Natural Spring） */
    body.classList.remove("mat-in");
    void body.offsetWidth;
    body.classList.add("mat-in");

    if (state.tab === "sample") renderSamples(ctrl, body);
    else if (state.tab === "source") renderSources(ctrl, body);
    else renderEffects(ctrl, body);
  }

  /* ═══════════ 采样页签 ═══════════ */

  function loadDirs(ctrl: any) {
    return UI.getJSON("/api/arrangement/dirs").then(function (r: any) {
      state.dirs = r.dirs || [];
      return loadPacks();
    }).then(function () {
      if (!state.expanded) state.expanded = loadExpanded();
      render(ctrl);
      refreshOpenNodes(ctrl);
    }).catch(function () {});
  }

  /** loadPacks 采样包元数据：把根目录标上"所属采样包"（P1 注册表为真源） */
  function loadPacks() {
    var reg = window.SoundRegistry;
    if (!reg) return Promise.resolve([]);
    return reg.load().then(function () {
      state.packs = reg.list("samplepack") || [];
      return state.packs;
    }).catch(function () { return []; });
  }

  /** packNameFor 目录所属采样包名。
      同一目录可能同时被"默认素材"（启动迁移按 settings.material_dirs 自动建的
      内置条目）和用户自建采样包覆盖——优先显示用户自建的那个，否则标签等于没信息。 */
  function packNameFor(dir: string) {
    var fallback = "";
    for (var i = 0; i < state.packs.length; i++) {
      var dirs = state.packs[i].dirs || [];
      var hit = false;
      for (var j = 0; j < dirs.length; j++) {
        if (String(dirs[j]).toLowerCase() === String(dir).toLowerCase()) { hit = true; break; }
      }
      if (!hit) continue;
      var name = state.packs[i].name || "";
      if (state.packs[i].origin === "builtin" || name === "默认素材") {
        if (!fallback) fallback = name;
        continue;
      }
      return name;
    }
    return fallback;
  }

  function renderSamples(ctrl: any, body: any) {
    var tree = el("div", "arr-file-tree");
    tree.id = "arrFileTree";
    body.appendChild(tree);
    if (!state.expanded) state.expanded = loadExpanded();

    var dirs = filterDirs(state.dirs, state.query);
    if (!dirs.length) {
      tree.appendChild(emptyState(
        state.dirs.length ? "没有匹配的素材目录" : "素材库还是空的",
        state.dirs.length
          ? "换个关键词，或清空搜索框"
          : "点右上角「＋ 文件夹」添加本地采样目录（wav / mp3 / ogg / flac），\n也可以在设置页「声音管理」里登记采样包"));
      return;
    }
    for (var i = 0; i < dirs.length; i++) {
      appendNode(ctrl, tree, dirs[i], "", 0);
    }
  }

  function filterDirs(dirs: any[], q: string) {
    var kw = (q || "").trim().toLowerCase();
    if (!kw) return dirs;
    return dirs.filter(function (d) {
      return String(d).toLowerCase().indexOf(kw) >= 0 || packNameFor(d).toLowerCase().indexOf(kw) >= 0;
    });
  }

  function emptyState(title: string, hint: string) {
    var box = el("div", "mat-empty");
    box.appendChild(el("div", "mat-empty-icon", "🗂"));
    box.appendChild(el("div", "mat-empty-title", title));
    var h = el("div", "mat-empty-hint", hint);
    h.style.whiteSpace = "pre-line";
    box.appendChild(h);
    return box;
  }

  function refreshOpenNodes(ctrl: any) {
    var touched = false;
    for (var i = 0; i < state.dirs.length; i++) {
      var d = state.dirs[i];
      if (state.expanded && state.expanded[d] && !state.treeCache[d]) {
        touched = true;
        state.treeCache[d] = { dirs: [], files: [], _loading: true };
        fetchNode(ctrl, d, "", function () {});
      }
    }
    if (touched) render(ctrl);
  }

  function fetchNode(ctrl: any, dir: string, sub: string, cb: any) {
    var url = "/api/arrangement/files?dir=" + encodeURIComponent(dir);
    if (sub) url += "&sub=" + encodeURIComponent(sub);
    UI.getJSON(url).then(function (r: any) {
      state.treeCache[nodeKey(dir, sub)] = {
        dirs: r.dirs || [], files: r.files || [], shown: MAX_CHILDREN, loaded: true
      };
      cb(null);
    }).catch(function (err: any) { cb(err); });
  }

  function toggleNode(ctrl: any, dir: string, sub: string) {
    if (!state.expanded) state.expanded = loadExpanded();
    var key = nodeKey(dir, sub);
    if (state.expanded[key]) {
      delete state.expanded[key];
      saveExpanded();
      render(ctrl);
      return;
    }
    state.expanded[key] = true;
    saveExpanded();
    if (state.treeCache[key]) { render(ctrl); return; }
    state.treeCache[key] = { dirs: [], files: [], _loading: true };
    render(ctrl);
    fetchNode(ctrl, dir, sub, function (err: any) {
      if (err) {
        delete state.treeCache[key];
        delete state.expanded[key];
        saveExpanded();
        UI.toast!("✗ 读取目录失败", "err");
      }
      render(ctrl);
    });
  }

  function removeDir(ctrl: any, d: string) {
    UI.delJSON("/api/arrangement/dirs", { path: d }).then(function (r: any) {
      state.dirs = r.dirs || [];
      [state.treeCache, state.expanded].forEach(function (m: any) {
        Object.keys(m || {}).forEach(function (k) {
          if (k === d || k.indexOf(d + "/") === 0) delete m[k];
        });
      });
      saveExpanded();
      loadPacks().then(function () { render(ctrl); });
      UI.toast!("✓ 已从素材库移除该目录（磁盘文件保留）", "ok");
    }).catch(function (err: any) {
      UI.toast!("✗ 移除失败: " + err.message, "err");
    });
  }

  function addFolder(ctrl: any) {
    UI.postJSON("/api/arrangement/pick-folder", {}).then(function (r: any) {
      if (!r.path) return;   // 用户取消
      return UI.postJSON("/api/arrangement/dirs", { path: r.path }).then(function (r2: any) {
        state.dirs = r2.dirs || [];
        if (!state.expanded) state.expanded = loadExpanded();
        state.expanded[r.path] = true;
        saveExpanded();
        loadPacks().then(function () { render(ctrl); });
        UI.toast!("✓ 已添加素材目录", "ok");
      });
    }).catch(function (err: any) {
      UI.toast!("✗ 添加目录失败: " + (err && err.message ? err.message : "未知错误"), "err");
    });
  }

  function matchNodeFiles(files: any[], sub: string, q: string) {
    var kw = (q || "").trim().toLowerCase();
    if (!kw) return files;
    return files.filter(function (f: any) {
      return String(f.name).toLowerCase().indexOf(kw) >= 0 ||
        String(sub || "").toLowerCase().indexOf(kw) >= 0;
    });
  }

  function appendNode(ctrl: any, container: any, dir: string, sub: string, depth: number) {
    var key = nodeKey(dir, sub);
    var expanded = !!(state.expanded && state.expanded[key]);
    var cached = state.treeCache[key];
    var pack = depth === 0 ? packNameFor(dir) : "";

    var row = el("div", "arr-file-item dir" + (expanded ? " open" : ""));
    row.style.paddingLeft = (8 + depth * 14) + "px";
    row.title = key;
    row.tabIndex = 0;
    row.setAttribute("role", "treeitem");
    row.setAttribute("aria-expanded", expanded ? "true" : "false");

    var arrow = el("span", "fi-glyph", cached && cached._loading ? "…" : (expanded ? "▾" : "▸"));
    row.appendChild(arrow);
    row.appendChild(el("span", "fi-name", baseName(sub || dir) || dir));
    if (pack) {
      var tag = el("span", "mat-pack-tag", pack);
      tag.title = "所属采样包: " + pack;
      row.appendChild(tag);
    }

    row.addEventListener("click", function () { toggleNode(ctrl, dir, sub); });
    row.addEventListener("keydown", function (e: any) {
      if (e.key === "Enter" || e.key === " " || e.key === "ArrowRight" || e.key === "ArrowLeft") {
        e.preventDefault();
        toggleNode(ctrl, dir, sub);
      }
    });

    if (depth === 0) {
      var del = el("span", "chip-del", "✕");
      del.title = "从素材库移除该目录（不删除磁盘文件）";
      del.addEventListener("click", function (e) {
        e.stopPropagation();
        UI.confirm("从素材库移除目录「" + (baseName(dir) || dir) + "」？\n磁盘文件不会被删除。").then(function (ok: any) {
          if (ok) removeDir(ctrl, dir);
        });
      });
      row.appendChild(del);
    }
    container.appendChild(row);
    if (!expanded || !cached || cached._loading) return;

    var childWrap = el("div", "arr-tree-children");
    container.appendChild(childWrap);

    var visibleFiles = matchNodeFiles(cached.files, sub, state.query);
    var visibleDirs = cached.dirs || [];
    if (!visibleDirs.length && !visibleFiles.length) {
      var empty = el("div", "arr-empty-tip", "空文件夹");
      empty.style.paddingLeft = (8 + (depth + 1) * 14) + "px";
      childWrap.appendChild(empty);
      return;
    }
    var shown = cached.shown || MAX_CHILDREN;
    for (var i = 0; i < visibleDirs.length; i++) {
      appendNode(ctrl, childWrap, dir, sub ? sub + "/" + visibleDirs[i].name : visibleDirs[i].name, depth + 1);
    }
    var fileLimit = Math.min(visibleFiles.length, shown);
    for (var j = 0; j < fileLimit; j++) {
      childWrap.appendChild(buildSampleRow(ctrl, dir, sub, visibleFiles[j], depth + 1));
    }
    if (visibleFiles.length > fileLimit) {
      var more = el("button", "mat-more-btn", "显示更多（还有 " + (visibleFiles.length - fileLimit) + " 个文件）") as HTMLButtonElement;
      more.type = "button";
      more.style.marginLeft = (8 + (depth + 1) * 14) + "px";
      more.addEventListener("click", function () {
        cached.shown = fileLimit + 200;
        render(ctrl);
      });
      childWrap.appendChild(more);
    }
  }

  /** 音频叶子行：波形缩略图 + 时长/大小 + 拖拽入轨（payload 与旧行为一致） */
  function buildSampleRow(ctrl: any, dir: string, sub: string, f: any, depth: number) {
    var absPath = joinAbs(dir, sub, f.name);
    var item = el("div", "arr-file-item sample mat-sample-row");
    item.draggable = true;
    item.style.paddingLeft = (8 + depth * 14) + "px";
    item.title = f.name + "（拖到轨道创建音频 Clip，双击试听）";
    item.tabIndex = 0;
    item.setAttribute("role", "treeitem");

    var wave = el("canvas", "mat-wave") as HTMLCanvasElement;
    wave.width = THUMB_W;
    wave.height = THUMB_H;
    wave.setAttribute("aria-hidden", "true");
    item.appendChild(wave);

    var info = el("div", "mat-item-info");
    var nameSpan = el("div", "mat-item-name", f.name);
    nameSpan.title = f.name;
    info.appendChild(nameSpan);
    var meta = el("div", "mat-item-meta", UI.fmtSize(f.size || 0));
    meta.dataset.size = UI.fmtSize(f.size || 0);
    info.appendChild(meta);
    item.appendChild(info);

    var preview = el("button", "mat-row-btn", "▷") as HTMLButtonElement;
    preview.type = "button";
    preview.title = "试听";
    preview.setAttribute("aria-label", "试听 " + f.name);
    preview.addEventListener("click", function (e) {
      e.stopPropagation();
      previewSample(ctrl, absPath, preview);
    });
    item.appendChild(preview);

    item.addEventListener("dragstart", function (e: any) {
      var payload = JSON.stringify({ kind: "arr-audio", p: absPath, name: f.name });
      e.dataTransfer.setData("application/x-arrange", payload);
      e.dataTransfer.setData("text/plain", payload);
      e.dataTransfer.effectAllowed = "copy";
      item.classList.add("dragging");
      var clear = function () {
        item.classList.remove("dragging");
        item.removeEventListener("dragend", clear);
      };
      item.addEventListener("dragend", clear);
    });
    item.addEventListener("dblclick", function () { previewSample(ctrl, absPath, preview); });
    item.addEventListener("keydown", function (e: any) {
      if (e.key === "Enter") { e.preventDefault(); previewSample(ctrl, absPath, preview); }
    });

    /* 波形与元信息：进入视口才解码（几十上百个文件不该在首帧一起解） */
    observeThumb(item, absPath, wave, meta, ctrl);
    return item;
  }

  var thumbObserver: any = null;
  var thumbPending: any[] = [];

  function observeThumb(item: any, absPath: string, canvas: any, meta: any, ctrl: any) {
    if (!thumbObserver) {
      thumbObserver = new IntersectionObserver(function (entries) {
        entries.forEach(function (en: any) {
          if (!en.isIntersecting) return;
          var job = (en.target as any)._matThumb;
          thumbObserver.unobserve(en.target);
          if (job) analyze(job.path).then(function (info: any) { job.paint(info); });
        });
      }, { root: document.getElementById("matBody"), rootMargin: "80px" });
    }
    item._matThumb = {
      path: absPath,
      paint: function (info: any) { paintThumb(canvas, meta, info, ctrl); }
    };
    thumbObserver.observe(item);
  }

  function paintThumb(canvas: any, meta: any, info: any, ctrl: any) {
    if (!info || info.missing || !info.peaks) {
      canvas.classList.add("mat-wave-missing");
      return;
    }
    canvas.title = fmtDuration(info.duration) + " · " + info.sampleRate + "Hz";
    var ctx = canvas.getContext("2d");
    if (!ctx) return;
    var w = canvas.width, h = canvas.height;
    ctx.clearRect(0, 0, w, h);
    var css = getComputedStyle(document.documentElement);
    var color = (css.getPropertyValue("--color-primary") || "#00B8CC").trim();
    ctx.fillStyle = color;
    var mid = h / 2;
    for (var i = 0; i < info.peaks.length && i < w; i++) {
      var amp = Math.max(1, info.peaks[i] * (h - 2));
      ctx.fillRect(i, mid - amp / 2, 1, amp);
    }
    var size = meta.dataset.size || "";
    meta.textContent = fmtDuration(info.duration) + " · " +
      (info.sampleRate / 1000).toFixed(1) + "kHz" + (size ? " · " + size : "");
  }

  function previewSample(ctrl: any, absPath: string, btn: any) {
    if (btn) {
      btn.classList.add("playing");
      setTimeout(function () { btn.classList.remove("playing"); }, 1200);
    }
    ctrl.engine.resume();
    ctrl.engine.previewSample(absPath).catch(function () {
      if (UI.toast) UI.toast!("✗ 音频试听失败（文件不存在或格式不支持）", "err");
    });
  }

  /* ═══════════ 音源页签 ═══════════ */

  function renderSources(ctrl: any, body: any) {
    var reg = window.SoundRegistry;
    var ArrRack = window.ArrRack;
    if (!reg) { body.appendChild(emptyState("音源注册表不可用", "请重启应用后重试")); return; }
    var kw = (state.query || "").trim().toLowerCase();
    var list = reg.enabledOf("sf2").concat(reg.enabledOf("vst3")).concat(reg.enabledOf("mm"));
    if (kw) {
      list = list.filter(function (s: any) {
        return [s.name, s.vendor, (s.tags || []).join(" "), reg.kindMeta(s.kind).label]
          .join(" ").toLowerCase().indexOf(kw) >= 0;
      });
    }
    if (!list.length) {
      body.appendChild(emptyState(
        kw ? "没有匹配的音源" : "还没有音源",
        kw ? "换个关键词试试" : "在设置页「声音管理」上传 .sf2 音色库或登记采样包"));
      return;
    }
    var grid = el("div", "mat-grid");
    list.forEach(function (src: any, i: number) {
      grid.appendChild(buildSourceCard(ctrl, src, i, ArrRack));
    });
    body.appendChild(grid);
  }

  function buildSourceCard(ctrl: any, src: any, idx: number, ArrRack: any) {
    var reg = window.SoundRegistry;
    var meta = reg.kindMeta(src.kind);
    var rackItem: any = null;
    var items = (ctrl.rack && ctrl.rack.items) || [];
    for (var i = 0; i < items.length; i++) {
      if (items[i].sourceId === src.id) { rackItem = items[i]; break; }
    }
    var inRack = !!rackItem;

    var card = el("div", "mat-source-card" + (inRack ? " in-rack" : ""));
    card.dataset.id = src.id;
    card.style.setProperty("--mat-delay", Math.min(idx, 12) * 0.03 + "s");
    card.tabIndex = 0;
    card.setAttribute("role", "option");
    card.setAttribute("aria-selected", inRack ? "true" : "false");
    card.draggable = true;

    var head = el("div", "mat-card-head");
    head.appendChild(el("span", "snd-kind-badge " + meta.cls, meta.badge));
    var nm = el("span", "mat-card-name", src.name || src.id);
    nm.title = src.name || "";
    head.appendChild(nm);
    if (reg.isAI(src)) {
      var ai = el("span", "snd-badge-ai", "✨AI");
      ai.title = "AI 生成的音源";
      head.appendChild(ai);
    }
    card.appendChild(head);

    var bits: string[] = [meta.label];
    var rt = src.runtime || {};
    if (src.kind === "sf2") {
      if (rt.presetCount) bits.push(rt.presetCount + " 预设");
      if (rt.size && UI.fmtSize) bits.push(UI.fmtSize(rt.size));
    } else if (src.vendor) {
      bits.push(src.vendor);
    }
    if (src.category) bits.push(src.category === "effect" ? "效果器" : "乐器");
    if (rt.missing) bits.push("⚠ 文件缺失");
    if (reg.isBuiltin(src)) bits.push("内置");
    card.appendChild(el("div", "mat-card-meta", bits.join(" · ")));

    var foot = el("div", "mat-card-foot");
    var pill = el("span", "mat-pill" + (inRack ? " on" : ""), inRack ? "已在架" : "＋ 入架");
    pill.title = inRack ? "已在乐器箱中" : "加入乐器箱";
    pill.addEventListener("click", function (e) {
      e.stopPropagation();
      if (inRack && rackItem) {
        ctrl.assignRackItem(rackItem, ctrl.selectedTrackIdx, rackItem.presetId || "");
      } else {
        ctrl.ensureRackItem(src.id, "", src.name).then(function (item: any) {
          if (item) ctrl.assignRackItem(item, ctrl.selectedTrackIdx, item.presetId || "");
        });
      }
    });
    foot.appendChild(pill);

    var act = el("div", "mat-card-actions");
    var prev = el("button", "mat-row-btn", "▷") as HTMLButtonElement;
    prev.type = "button";
    prev.title = "试听";
    prev.setAttribute("aria-label", "试听 " + (src.name || ""));
    prev.addEventListener("click", function (e) {
      e.stopPropagation();
      previewSource(ctrl, src, rackItem, prev);
    });
    act.appendChild(prev);
    foot.appendChild(act);
    card.appendChild(foot);

    card.addEventListener("dblclick", function () { previewSource(ctrl, src, rackItem, prev); });
    card.addEventListener("keydown", function (e: any) {
      if (e.key === "Enter") {
        e.preventDefault();
        if (!inRack) ctrl.ensureRackItem(src.id, "", src.name).then(function (item: any) {
          if (item) ctrl.assignRackItem(item, ctrl.selectedTrackIdx, item.presetId || "");
        });
      } else if (e.key === " ") {
        e.preventDefault();
        previewSource(ctrl, src, rackItem, prev);
      }
    });
    card.addEventListener("contextmenu", function (e: any) {
      e.preventDefault();
      openSourceMenu(ctrl, src, rackItem, e.clientX, e.clientY);
    });
    card.addEventListener("dragstart", function (e: any) {
      card.classList.add("dragging");
      /* 载荷带 sourceId：即使还没入架，落点也会先入架再分配 */
      var payload = rackItem
        ? { kind: "arr-source", itemId: rackItem.id, presetId: rackItem.presetId || "", label: rackItem.name }
        : { kind: "arr-source", sourceId: src.id, presetId: "", label: src.name || src.id };
      e.dataTransfer.setData("application/x-arrange", JSON.stringify(payload));
      e.dataTransfer.setData("text/plain", JSON.stringify(payload));
      e.dataTransfer.setData("application/x-arrange-source", "1");
      e.dataTransfer.effectAllowed = "copyMove";
    });
    card.addEventListener("dragend", function () { card.classList.remove("dragging"); });
    return card;
  }

  function previewSource(ctrl: any, src: any, rackItem: any, btn: any) {
    var ArrRack = window.ArrRack;
    var item = rackItem;
    if (!item) {
      // 未入架：试听走临时条目（不写机架——试听不该改工程状态）
      item = ArrRack.itemFromSourceId(src.id, "", src.name);
      if (!item) { UI.toast!("⚠ 该音源暂不支持试听", "warn"); return; }
      var resolved = ArrRack.resolve([item], { itemId: item.id, presetId: item.presetId || "" });
      if (resolved.voice.type === "sf2" && !resolved.voice.path) {
        UI.toast!("⚠ 音源文件缺失，请到设置页「声音管理」重新扫描", "warn");
        return;
      }
      ctrl.previewRackSource(resolved, btn);
      return;
    }
    ctrl.previewRackItem(item, btn, item.presetId || "");
  }

  function openSourceMenu(ctrl: any, src: any, rackItem: any, x: number, y: number) {
    var reg = window.SoundRegistry;
    var items: any[] = [{ heading: reg.kindMeta(src.kind).label + " · " + (src.name || "") }];
    items.push({
      label: "＋ 加入乐器箱",
      disabled: !!rackItem,
      action: function () {
        ctrl.ensureRackItem(src.id, "", src.name).then(function (item: any) {
          if (item) ctrl.showHUD("🎛 已加入机架: " + item.name);
        });
      }
    });
    items.push({
      label: "🎛 移出乐器箱",
      disabled: !rackItem,
      action: function () { if (rackItem) ctrl.removeRackItem(rackItem.id); }
    });
    items.push("-");
    items.push({
      label: "🔎 在声音管理中查看",
      action: function () {
        try {
          localStorage.setItem("melodymate-open-sound-manage", src.id);
          window.location.href = "/settings.html?pane=paneSound";
        } catch (e) {}
      }
    });
    items.push({
      label: "📁 打开文件位置",
      disabled: !(src.kind === "sf2" || src.kind === "samplepack"),
      action: function () {
        UI.postJSON("/api/sound/reveal", { id: src.id }).catch(function (err: any) {
          UI.toast!("✗ 打开位置失败: " + (err && err.message ? err.message : ""), "err");
        });
      }
    });
    ctrl.showMenu(items, x, y);
  }

  /* ═══════════ 效果器页签 ═══════════ */

  function renderEffects(ctrl: any, body: any) {
    var reg = window.SoundRegistry;
    if (!reg) { body.appendChild(emptyState("音源注册表不可用", "请重启应用后重试")); return; }
    var kw = (state.query || "").trim().toLowerCase();
    var list = reg.all().filter(function (s: any) {
      var plugin = s.kind === "vst3" || s.kind === "mm";
      if (!plugin) return false;
      // category 缺省视为乐器（SF2 采样包不在此页签）
      return (s.category || "instrument") === "effect";
    });
    if (kw) {
      list = list.filter(function (s: any) {
        return [s.name, s.vendor, (s.tags || []).join(" ")].join(" ").toLowerCase().indexOf(kw) >= 0;
      });
    }
    if (!list.length) {
      body.appendChild(emptyState(
        kw ? "没有匹配的效果器" : "还没有效果器",
        kw
          ? "换个关键词试试"
          : "效果器装载到混音台 FX 槽：在混音台点空槽 →「装载插件」选择"));
      return;
    }
    var wrap = el("div", "mat-fx-list");
    list.forEach(function (src: any, i: number) {
      var card = el("div", "mat-fx-card");
      card.style.setProperty("--mat-delay", Math.min(i, 12) * 0.03 + "s");
      var meta = reg.kindMeta(src.kind);
      card.appendChild(el("span", "snd-kind-badge " + meta.cls, meta.badge));
      var info = el("div", "mat-item-info");
      info.appendChild(el("div", "mat-item-name", src.name || src.id));
      var bits = [meta.label];
      if (src.vendor) bits.push(src.vendor);
      info.appendChild(el("div", "mat-item-meta", bits.join(" · ")));
      card.appendChild(info);
      card.appendChild(el("span", "mat-pill on", "可装载"));
      card.title = "在混音台点空 FX 槽 →「装载插件」选择该效果器";
      wrap.appendChild(card);
    });
    body.appendChild(wrap);
    var note = el("div", "arr-panel-hint", "效果器装载到混音台 FX 槽：在混音台点空槽 →「装载插件」选择（需先在设置页扫描插件目录）");
    body.appendChild(note);
  }

  /* ═══════════ 对外出口 ═══════════ */

  window.ArrMaterial = {
    TABS: TABS,
    TREE_EXPANDED_KEY: TREE_EXPANDED_KEY,
    MAX_CHILDREN: MAX_CHILDREN,
    state: state,
    bind: bind,
    setTab: setTab,
    render: render,
    loadDirs: loadDirs,
    refreshOpenNodes: refreshOpenNodes,
    nodeKey: nodeKey,
    joinAbs: joinAbs,
    loadExpanded: loadExpanded,
    saveExpanded: saveExpanded,
    toggleNode: toggleNode,
    removeDir: removeDir,
    addFolder: addFolder,
    appendNode: appendNode,
    buildSampleRow: buildSampleRow,
    renderSamples: renderSamples,
    renderSources: renderSources,
    renderEffects: renderEffects,
    analyze: analyze,
    computePeaks: computePeaks,
    fmtDuration: fmtDuration,
    packNameFor: packNameFor,
    hintFor: hintFor,
    _resetForTest: function () {
      state.info = {};
      state.infoInflight = {};
      state.decodeQueue = [];
      state.treeCache = {};
    }
  };
})(window);
