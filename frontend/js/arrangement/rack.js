/* 输出机架（乐器箱）与音源选择器（P2）
 *
 * 机架是"项目级的音源选集"：轨道与钢琴窗能用的音色只能来自机架条目
 * （arrangement.json 的 rack.items），机架条目的候选来自音源注册表
 * （Library/registry.json，设置页「声音管理」）与三个内置虚拟音源。
 *
 * 本模块负责：
 *   1. 条目模型：稳定 id 派生（与后端 internal/project/arrangement_ops.go
 *      的 rackItemID 同算法）、v1 轨道音源 → v2 机架引用的迁移；
 *   2. 音源解析：条目 + 预设 → 发声描述（引擎轨 / WebAudio 两条路径共用）；
 *   3. 机架窗口渲染（分类页签 / 搜索 / 条目六态 / 拖把手 / 空态）；
 *   4. 音源选择器 `.snd-picker`（机架 ＋ / 轨道头 / 钢琴窗三处共用）。
 *
 * 依赖：window.UI、window.SoundRegistry（都要在调用期而非加载期存在）。
 */
(function (window) {
  "use strict";

  var UI = window.UI;

  /* ═══════════ 常量 ═══════════ */

  /* 条目种类：与后端 project.RackKind* 一一对应 */
  var KIND_META                          = {
    synth: { label: "合成器", short: "SYNTH", badge: "SYNTH", cls: "rk-kind-synth", icon: "🎹" },
    sf2: { label: "音源 SF2", short: "SF2", badge: "SF2", cls: "rk-kind-sf2", icon: "🎹" },
    pack: { label: "采样包", short: "采样包", badge: "PACK", cls: "rk-kind-pack", icon: "📁" },
    vst3: { label: "VST 插件", short: "VST", badge: "VST3", cls: "rk-kind-vst", icon: "🎛" },
    mm: { label: "MM 插件", short: "MM", badge: "MM", cls: "rk-kind-mm", icon: "🧩" },
    sample: { label: "采样", short: "采样", badge: "WAV", cls: "rk-kind-sample", icon: "🌊" }
  };

  /* 机架分类页签（4.3 节：全部 / 合成器 / SF2 / VST / MM / 采样） */
  var RACK_FILTERS = [
    { id: "all", label: "全部" },
    { id: "synth", label: "合成器" },
    { id: "sf2", label: "SF2" },
    { id: "vst3", label: "VST" },
    { id: "mm", label: "MM" },
    { id: "sample", label: "采样" }
  ];

  /* 内置合成器波形（presetId 即波形名） */
  var SYNTH_WAVES = [
    { id: "sawtooth", name: "锯齿波" },
    { id: "square", name: "方波" },
    { id: "triangle", name: "三角波" },
    { id: "sine", name: "正弦波" }
  ];

  /* 随包 GeneralUser GS 的两个常用音色（builtin:gm 的预设） */
  var GM_PRESETS = [
    { id: "0:0", name: "温暖钢琴", bank: 0, program: 0 },
    { id: "0:48", name: "弦乐群", bank: 0, program: 48 }
  ];

  var BUILTIN_SYNTH = "builtin:synth";
  var BUILTIN_GM = "builtin:gm";
  var MAX_ITEMS = 64;
  var PACK_LABEL                          = { pack: "samplepack" };

  function kindMeta(kind        ) {
    return KIND_META[kind] || { label: kind || "音源", short: kind || "?", badge: "?", cls: "", icon: "♪" };
  }

  function kindLabel(kind        ) {
    return kindMeta(kind).label;
  }

  function isBuiltinSourceId(sourceId        ) {
    return sourceId === BUILTIN_SYNTH || sourceId === BUILTIN_GM;
  }

  /* ═══════════ 条目 id（与后端 rackItemID 同算法） ═══════════
   * FNV-1a 64 over UTF-8 bytes。前端必须能离线造出与后端一致的 id：
   * v1 工程在浏览器里迁移后整份 PUT，若两端 id 不同就会出现同一音源
   * 两条条目（AI 加一条、界面加一条）。tools/dev/rack_check.mjs 用固定
   * 向量盯住两端一致。 */

  function utf8Bytes(str        ) {
    var out           = [];
    for (var i = 0; i < str.length; i++) {
      var c = str.charCodeAt(i);
      if (c < 0x80) {
        out.push(c);
      } else if (c < 0x800) {
        out.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
      } else if (c >= 0xd800 && c <= 0xdbff && i + 1 < str.length) {
        var c2 = str.charCodeAt(i + 1);
        if (c2 >= 0xdc00 && c2 <= 0xdfff) {
          var cp = 0x10000 + ((c - 0xd800) << 10) + (c2 - 0xdc00);
          out.push(
            0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f),
            0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f)
          );
          i++;
          continue;
        }
        out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
      } else {
        out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
      }
    }
    return out;
  }

  /* 用 BigInt 保证 64 位无溢出（WebView2 / 现代浏览器均支持） */
  function fnv1a64(str        ) {
    var bytes = utf8Bytes(str);
    var h = 0xcbf29ce484222325n;
    var p = 0x100000001b3n;
    var mask = 0xffffffffffffffffn;
    for (var i = 0; i < bytes.length; i++) {
      h = ((h ^ BigInt(bytes[i])) * p) & mask;
    }
    var hex = h.toString(16);
    while (hex.length < 16) hex = "0" + hex;
    return hex;
  }

  function rackItemId(kind        , sourceId        , presetId        , path        ) {
    var key = [
      kind || "",
      String(sourceId || "").toLowerCase(),
      presetId || "",
      String(path || "").toLowerCase()
    ].join("\u0000");
    return "rk_" + fnv1a64(key).slice(0, 12);
  }

  /* ═══════════ 条目工厂 ═══════════ */

  function makeItem(kind        , sourceId        , presetId        , name        , path         ) {
    var item      = {
      id: rackItemId(kind, sourceId, presetId, path || ""),
      kind: kind,
      sourceId: sourceId,
      name: name || sourceId
    };
    if (presetId) item.presetId = presetId;
    if (path) item.path = path;
    return item;
  }

  function synthItem(wave        , name         ) {
    var w = wave || "sawtooth";
    return makeItem("synth", BUILTIN_SYNTH, w, name || ("合成器 · " + waveName(w)));
  }

  function gmItem(presetId        , name         ) {
    var p = presetId || "0:0";
    return makeItem("sf2", BUILTIN_GM, p, name || ("内置 · " + gmPresetName(p)));
  }

  function sampleItem(path        , name         ) {
    var clean = String(path || "");
    return makeItem("sample", clean, "", name || baseName(clean), clean);
  }

  /* registryItem 注册表条目 → 机架条目（预设缺省时由调用方补） */
  function registryItem(src     , presetId        , name        ) {
    var kind = registryKindToRackKind(src && src.kind);
    var id = src && src.id ? src.id : "";
    var preset = presetId || defaultPresetFor(kind);
    return makeItem(kind, id, preset, name || itemDisplayName(kind, src, preset), "");
  }

  function registryKindToRackKind(kind        ) {
    if (kind === "samplepack") return "pack";
    return kind || "";
  }

  function rackKindToRegistryKind(kind        ) {
    if (kind === "pack") return "samplepack";
    return kind;
  }

  function defaultPresetFor(kind        ) {
    if (kind === "synth") return "sawtooth";
    if (kind === "sf2") return "0:0";
    return "";
  }

  function itemDisplayName(kind        , src     , presetId        ) {
    var base = (src && src.name) || "";
    if (!presetId || kind === "synth") return base;
    return base + " › " + presetId;
  }

  /* itemFromSourceId 按 sourceId 造条目：内置 → 注册表 → 采样文件路径 */
  function itemFromSourceId(sourceId        , presetId        , name        ) {
    var id = String(sourceId || "").trim();
    if (!id) return null;
    if (id === BUILTIN_SYNTH) return synthItem(presetId || "sawtooth", name);
    if (id === BUILTIN_GM) return gmItem(presetId || "0:0", name);
    var reg = window.SoundRegistry;
    var src = reg ? reg.get(id) : null;
    if (src) return registryItem(src, presetId, name);
    if (/\.(wav|wave|mp3|ogg|oga|flac|m4a|aac)$/i.test(id)) return sampleItem(id, name);
    return null;
  }

  /* ═══════════ 条目表操作 ═══════════ */

  function find(items     , itemId        ) {
    if (!itemId) return null;
    var list = items || [];
    for (var i = 0; i < list.length; i++) {
      if (list[i] && list[i].id === itemId) return list[i];
    }
    return null;
  }

  /* upsert 写入或替换（同 id 覆盖），返回 {item, created} */
  function upsert(items     , item     ) {
    if (!item) return { item: null, created: false };
    var list = items || [];
    for (var i = 0; i < list.length; i++) {
      if (list[i] && list[i].id === item.id) {
        list[i] = item;
        return { item: item, created: false };
      }
    }
    list.push(item);
    return { item: item, created: true };
  }

  function cloneItem(item     ) {
    var out      = {};
    for (var k in item) if (Object.prototype.hasOwnProperty.call(item, k)) out[k] = item[k];
    return out;
  }

  /* ═══════════ v1 → v2 音源迁移（与后端 rackItemFromV1Source 同规则） ═══════════ */

  /* migrateV1Source v1 轨道音源 {type,wave,tone,libId,presetId,label} → 机架条目 */
  function migrateV1Source(src     ) {
    if (!src || typeof src !== "object") return null;
    if (src.itemId) return null; // 已是 v2 引用
    var type = src.type;
    if (type === "synth") {
      var wave = src.wave || "sawtooth";
      return synthItem(wave, "合成器 · " + waveName(wave));
    }
    if (type === "builtin") {
      var strings = src.tone === "strings";
      var preset = strings ? "0:48" : "0:0";
      var gu = generalUserSourceId();
      if (gu) {
        var gsrc = window.SoundRegistry ? window.SoundRegistry.get(gu) : null;
        return makeItem("sf2", gu, preset, ((gsrc && gsrc.name) || "GeneralUser GS") + " › " + (strings ? "弦乐群" : "钢琴"), "");
      }
      return gmItem(preset, "内置 · " + (strings ? "弦乐群" : "温暖钢琴"));
    }
    if (type === "sf2") {
      var sourceId = src.sourceId || src.libId || "";
      if (!sourceId) return null;
      var presetId = src.presetId || "0:0";
      var reg = window.SoundRegistry;
      var rs = reg ? reg.get(sourceId) : null;
      var kind = rs ? registryKindToRackKind(rs.kind) : "sf2";
      var label = src.label || "";
      var nm = rs ? ((rs.name || sourceId) + " › " + presetId) : (label || sourceId);
      return makeItem(kind, sourceId, presetId, nm, "");
    }
    return null;
  }

  /* generalUserSourceId 注册表里随包 GeneralUser GS 的条目 id（找不到返回空） */
  function generalUserSourceId() {
    var reg = window.SoundRegistry;
    if (!reg) return "";
    var list = reg.list("sf2") || [];
    for (var i = 0; i < list.length; i++) {
      var n = String(list[i].name || "").replace(/\s+/g, "").toLowerCase();
      if (n.indexOf("generaluser") >= 0) return list[i].id;
    }
    return "";
  }

  /* ═══════════ 音源解析：条目 + 预设 → 发声描述 ═══════════ */

  /* 机架条目模块级镜像：audio_engine 等无控制器引用的地方按 track.source 解析 */
  var mirrorItems        = [];

  function setItems(items     ) {
    mirrorItems = items || [];
  }

  function items() {
    return mirrorItems;
  }

  /* resolve(items, source) → voice
   * source = {itemId, presetId}；返回：
   *   { ok, missing, item, itemId, presetId, kind, label, detail, key, voice }
   * voice.type：synth / sf2 / builtin / sample / plugin / pack / none */
  function resolve(list     , source     ) {
    var itemId = (source && source.itemId) || "";
    var item = find(list, itemId);
    var presetId = (source && source.presetId) || (item && item.presetId) || "";
    var key = "item:" + itemId + ":" + presetId;
    if (!item) {
      return {
        ok: false, missing: true, item: null, itemId: itemId, presetId: presetId,
        kind: "", label: "音源缺失", detail: "", key: key, voice: { type: "none" }
      };
    }
    var kind = item.kind;
    var voice      = { type: "none" };
    var missing = false;

    if (kind === "synth") {
      voice = { type: "synth", wave: presetId || item.presetId || "sawtooth" };
    } else if (kind === "sf2") {
      if (item.sourceId === BUILTIN_GM) {
        voice = {
          type: "builtin",
          tone: presetId === "0:48" ? "strings" : "piano",
          presetId: presetId,
          sourceId: BUILTIN_GM
        };
      } else {
        var reg = window.SoundRegistry;
        var src = reg ? reg.get(item.sourceId) : null;
        var abs = reg ? reg.diskPathOf(src) : "";
        missing = !src || !!(src.runtime && src.runtime.missing);
        voice = {
          type: "sf2", sourceId: item.sourceId, presetId: presetId || "0:0",
          path: abs, missing: missing
        };
      }
    } else if (kind === "sample") {
      voice = { type: "sample", path: item.path || item.sourceId };
    } else if (kind === "vst3" || kind === "mm") {
      voice = { type: "plugin", pluginKind: kind, sourceId: item.sourceId, presetId: presetId };
    } else if (kind === "pack") {
      voice = { type: "pack", sourceId: item.sourceId };
    }

    return {
      ok: true,
      missing: missing,
      item: item,
      itemId: itemId,
      presetId: presetId,
      kind: kind,
      label: item.name || item.sourceId || "音源",
      detail: voiceDetail(kind, item, presetId),
      key: key,
      voice: voice
    };
  }

  function resolveSource(source     ) {
    return resolve(mirrorItems, source);
  }

  function voiceDetail(kind        , item     , presetId        ) {
    if (kind === "synth") return "合成器 · " + waveName(presetId || item.presetId);
    if (kind === "sf2") {
      if (item.sourceId === BUILTIN_GM) return "内置 GeneralUser GS · " + gmPresetName(presetId);
      return "预设 " + (presetId || "0:0");
    }
    if (kind === "sample") return baseName(item.path || item.sourceId);
    if (kind === "pack") return "采样包";
    if (kind === "vst3" || kind === "mm") return presetId ? ("预设 " + presetId) : kindMeta(kind).label;
    return "";
  }

  function waveName(wave        ) {
    for (var i = 0; i < SYNTH_WAVES.length; i++) {
      if (SYNTH_WAVES[i].id === wave) return SYNTH_WAVES[i].name;
    }
    return wave || "锯齿波";
  }

  function gmPresetName(presetId        ) {
    for (var i = 0; i < GM_PRESETS.length; i++) {
      if (GM_PRESETS[i].id === presetId) return GM_PRESETS[i].name;
    }
    return presetId || "温暖钢琴";
  }

  function baseName(path        ) {
    return String(path || "").split(/[\\/]/).pop() || "";
  }

  /* presetList 条目的可选预设（合成器=波形；builtin:gm=两条；SF2=后端预设表） */
  function presetList(item     ) {
    if (!item) return Promise.resolve([]);
    if (item.kind === "synth") {
      return Promise.resolve(SYNTH_WAVES.map(function (w) {
        return { id: w.id, name: w.name };
      }));
    }
    if (item.kind === "sf2" && item.sourceId === BUILTIN_GM) {
      return Promise.resolve(GM_PRESETS.map(function (p) {
        return { id: p.id, name: p.name };
      }));
    }
    if (item.kind === "sf2" && item.sourceId) {
      var reg = window.SoundRegistry;
      if (reg) {
        return reg.presets(item.sourceId).then(function (list     ) {
          return (list || []).map(function (p     ) {
            return { id: p.id, name: p.name || p.id };
          });
        }).catch(function () { return []; });
      }
    }
    if (item.kind === "vst3" || item.kind === "mm") {
      return Promise.resolve([]); // P5/P6：插件预设由插件提供
    }
    return Promise.resolve([]);
  }

  /* isAIItem 条目对应的注册表音源是否 AI 生成（AI 徽标五处统一） */
  function isAIItem(item     ) {
    if (!item) return false;
    var reg = window.SoundRegistry;
    if (!reg) return false;
    var src = reg.get(item.sourceId);
    return !!(src && reg.isAI(src));
  }

  function isBuiltinItem(item     ) {
    if (!item) return false;
    if (isBuiltinSourceId(item.sourceId)) return true;
    var reg = window.SoundRegistry;
    if (!reg) return false;
    var src = reg.get(item.sourceId);
    return !!(src && reg.isBuiltin(src));
  }

  /* itemMetaText 条目元信息（monospace 小字） */
  function itemMetaText(item     ) {
    if (!item) return "";
    var bits           = [];
    if (isBuiltinItem(item)) bits.push("内置");
    var reg = window.SoundRegistry;
    var src = reg ? reg.get(item.sourceId) : null;
    if (item.kind === "sf2" && src) {
      var rt = src.runtime || {};
      if (rt.missing) bits.push("⚠ 文件缺失");
      if (rt.presetCount) bits.push(rt.presetCount + " 预设");
      if (rt.size && UI && UI.fmtSize) bits.push(UI.fmtSize(rt.size));
    } else if (item.kind === "sample") {
      bits.push(baseName(item.path || item.sourceId));
    } else if (item.kind === "pack") {
      var dirs = (src && src.dirs) || [];
      if (dirs.length) bits.push(dirs.length + " 个目录");
    } else if (item.kind === "vst3" || item.kind === "mm") {
      if (src && src.vendor) bits.push(src.vendor);
    }
    var detail = voiceDetail(item.kind, item, item.presetId);
    if (detail) bits.push(detail);
    return bits.join(" · ");
  }

  /* ═══════════ 机架窗口渲染 ═══════════ */

  function rackItems(ctrl     ) {
    return (ctrl && ctrl.rack && ctrl.rack.items) || [];
  }

  /* derivedSamples 项目内音频 clip 派生出的采样条目（P2 保留原能力：
   * 机架「采样」组列出工程里已用过的采样，拖到轨道即成 clip。
   * P3 素材库接上采样包后由显式入架的条目取代）。 */
  function derivedSamples(ctrl     ) {
    var seen                           = {};
    var out        = [];
    var tracks = (ctrl && ctrl.tracks) || [];
    for (var t = 0; t < tracks.length; t++) {
      var clips = tracks[t].clips || [];
      for (var c = 0; c < clips.length; c++) {
        var clip = clips[c];
        if (clip.type !== "audio" || !clip.src || !clip.src.p) continue;
        var p = clip.src.p;
        if (seen[p]) continue;
        seen[p] = true;
        var item = sampleItem(p, clip.name || baseName(p));
        item.derived = true;
        out.push(item);
      }
    }
    return out;
  }

  /* visibleRows 过滤 + 搜索后的行列表（持久条目 + 派生采样） */
  function visibleRows(ctrl     , filter        , query        ) {
    var rows        = [];
    var items = rackItems(ctrl);
    for (var i = 0; i < items.length; i++) {
      rows.push({ item: items[i], derived: false });
    }
    var samples = derivedSamples(ctrl);
    for (var j = 0; j < samples.length; j++) {
      // 已被显式入架的同一路径不重复列出
      var dup = false;
      for (var k = 0; k < items.length; k++) {
        if (items[k].kind === "sample" && (items[k].path || items[k].sourceId) === samples[j].path) { dup = true; break; }
      }
      if (!dup) rows.push({ item: samples[j], derived: true });
    }
    if (filter && filter !== "all") {
      rows = rows.filter(function (r     ) {
        if (filter === "sample") return r.item.kind === "sample" || r.item.kind === "pack";
        return r.item.kind === filter;
      });
    }
    var q = (query || "").trim().toLowerCase();
    if (q) {
      rows = rows.filter(function (r     ) {
        var hay = [r.item.name, r.item.sourceId, r.item.presetId, r.item.path, kindLabel(r.item.kind)]
          .join(" ").toLowerCase();
        return hay.indexOf(q) >= 0;
      });
    }
    return rows;
  }

  function badgeEl(kind        ) {
    var meta = kindMeta(kind);
    var b = document.createElement("span");
    b.className = "arr-rack-item-badge " + meta.cls;
    b.textContent = meta.badge;
    b.title = meta.label;
    return b;
  }

  function aiBadge() {
    var ai = document.createElement("span");
    ai.className = "snd-badge-ai";
    ai.textContent = "✨AI";
    ai.title = "AI 生成的音源";
    return ai;
  }

  /* render 机架窗口列表（分类页签 + 搜索 + 条目六态 + 空态） */
  function render(ctrl     ) {
    var listEl = document.getElementById("arrRackList");
    if (!listEl) return;
    var filter = ctrl._currentRackFilter || "all";
    var query = ctrl._rackQuery || "";
    var rows = visibleRows(ctrl, filter, query);
    listEl.innerHTML = "";

    if (!rows.length) {
      var empty = document.createElement("div");
      empty.className = "arr-rack-empty";
      var isEmptyRack = !rackItems(ctrl).length && filter === "all" && !query;
      var icon = document.createElement("div");
      icon.className = "arr-rack-empty-icon";
      icon.textContent = isEmptyRack ? "🎛" : "🔍";
      empty.appendChild(icon);
      var title = document.createElement("div");
      title.className = "arr-rack-empty-title";
      title.textContent = isEmptyRack ? "乐器箱是空的" : "没有匹配的音源";
      empty.appendChild(title);
      var hint = document.createElement("div");
      hint.className = "arr-rack-empty-hint";
      hint.textContent = isEmptyRack
        ? "轨道与钢琴窗的音色只能来自机架：先从这里挑选音源"
        : "换个关键词或切回「全部」分类试试";
      empty.appendChild(hint);
      if (isEmptyRack) {
        var big = document.createElement("button");
        big.type = "button";
        big.className = "btn btn-primary btn-sm arr-rack-empty-btn";
        big.textContent = "从声音管理挑选音源";
        big.addEventListener("click", function () { openPickerFor(ctrl, { assign: false }); });
        empty.appendChild(big);
      }
      listEl.appendChild(empty);
      updateStatus(ctrl, rows);
      return;
    }

    rows.forEach(function (row     , idx        ) {
      listEl .appendChild(buildRow(ctrl, row.item, row.derived, idx));
    });
    updateStatus(ctrl, rows);
  }

  function updateStatus(ctrl     , rows     ) {
    var statusEl = document.getElementById("arrRackStatusText");
    if (!statusEl) return;
    var items = rackItems(ctrl);
    var aiCount = items.filter(function (it     ) { return isAIItem(it); }).length;
    var text = "已挂载 " + items.length + " 个音源";
    if (aiCount) text += " · " + aiCount + " 个 AI 生成";
    if (rows && rows.length !== items.length) text += "（显示 " + rows.length + " 条）";
    statusEl.textContent = text;
  }

  function buildRow(ctrl     , item     , derived         , idx        ) {
    var row = document.createElement("div");
    row.className = "arr-rack-item";
    row.dataset.itemId = item.id;
    row.setAttribute("role", "option");
    row.tabIndex = 0;
    row.style.setProperty("--rk-delay", Math.min(idx, 12) * 0.04 + "s");
    row.title = item.name + (derived ? "（工程内采样）" : "");

    var handle = document.createElement("span");
    handle.className = "arr-rack-item-drag-handle";
    handle.textContent = "⋮⋮";
    handle.title = "按住拖拽到轨道";
    row.appendChild(handle);

    row.appendChild(badgeEl(item.kind));

    var info = document.createElement("div");
    info.className = "arr-rack-item-info";
    var nameEl = document.createElement("div");
    nameEl.className = "arr-rack-item-name";
    nameEl.textContent = item.name || item.sourceId || "音源";
    info.appendChild(nameEl);
    if (isAIItem(item)) nameEl.appendChild(aiBadge());
    var desc = document.createElement("div");
    desc.className = "arr-rack-item-desc";
    if (derived) {
      desc.textContent = "工程内采样 · " + baseName(item.path);
    } else {
      var usage = tracksUsing(ctrl, item.id);
      var meta = itemMetaText(item);
      desc.textContent = meta + (usage ? " · " + usage : "");
    }
    info.appendChild(desc);
    row.appendChild(info);

    var actions = document.createElement("div");
    actions.className = "arr-rack-item-actions";

    var previewBtn = document.createElement("button");
    previewBtn.type = "button";
    previewBtn.className = "arr-rack-preview-btn";
    previewBtn.innerHTML = "▷";
    previewBtn.title = "试听（发出一个 C4 音）";
    previewBtn.setAttribute("aria-label", "试听 " + (item.name || ""));
    previewBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      if (ctrl.previewRackItem) ctrl.previewRackItem(item, previewBtn);
    });
    actions.appendChild(previewBtn);

    var assignBtn = document.createElement("button");
    assignBtn.type = "button";
    assignBtn.className = "arr-rack-assign-btn";
    assignBtn.textContent = derived ? "放置" : "分配";
    assignBtn.title = derived ? "在当前播放头位置放入音频 Clip" : "分配到当前选中的轨道";
    assignBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      assignToSelected(ctrl, item, derived);
    });
    actions.appendChild(assignBtn);

    var moreBtn = document.createElement("button");
    moreBtn.type = "button";
    moreBtn.className = "arr-rack-more-btn";
    moreBtn.textContent = "⋯";
    moreBtn.title = "更多操作";
    moreBtn.setAttribute("aria-label", "更多操作");
    moreBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      openItemMenu(ctrl, item, derived, e.clientX, e.clientY);
    });
    actions.appendChild(moreBtn);

    row.appendChild(actions);

    row.addEventListener("dblclick", function () {
      if (ctrl.previewRackItem) ctrl.previewRackItem(item, previewBtn);
    });
    row.addEventListener("contextmenu", function (e) {
      e.preventDefault();
      openItemMenu(ctrl, item, derived, e.clientX, e.clientY);
    });
    bindDrag(ctrl, row, item, derived);
    // 键盘可达：Enter 分配、空格试听（4.3 键盘操作）
    row.addEventListener("keydown", function (e     ) {
      if (e.key === "Enter") {
        e.preventDefault();
        assignToSelected(ctrl, item, derived);
      } else if (e.key === " ") {
        e.preventDefault();
        if (ctrl.previewRackItem) ctrl.previewRackItem(item, previewBtn);
      }
    });
    return row;
  }

  /* tracksUsing 该条目被哪些轨道使用（机架条目显示占用情况） */
  function tracksUsing(ctrl     , itemId        ) {
    var out           = [];
    var tracks = (ctrl && ctrl.tracks) || [];
    for (var i = 0; i < tracks.length; i++) {
      var src = tracks[i].source || {};
      if (src.itemId === itemId) out.push("#" + (i + 1));
    }
    return out.length ? out.join("、") + " 在用" : "";
  }

  function bindDrag(ctrl     , row     , item     , derived         ) {
    row.draggable = true;
    row.addEventListener("dragstart", function (e     ) {
      row.classList.add("dragging");
      var payload      = derived
        ? { kind: "arr-audio", p: item.path, name: item.name }
        : { kind: "arr-source", itemId: item.id, presetId: item.presetId || "", label: item.name };
      try {
        e.dataTransfer.setData("application/x-arrange", JSON.stringify(payload));
        e.dataTransfer.setData("text/plain", JSON.stringify(payload));
        if (!derived) e.dataTransfer.setData("application/x-arrange-source", "1");
        e.dataTransfer.effectAllowed = "copyMove";
        e.dataTransfer.setDragImage(dragImage(item, derived), 12, 12);
      } catch (err) { /* 老浏览器无 setDragImage：忽略，拖拽本身仍可用 */ }
    });
    row.addEventListener("dragend", function () {
      row.classList.remove("dragging");
    });
  }

  /* dragImage 拖影：音源名 + kind 徽标（4.3 节） */
  function dragImage(item     , derived         ) {
    var ghost = document.createElement("div");
    ghost.className = "arr-rack-dragghost";
    var b = document.createElement("span");
    b.className = "arr-rack-item-badge " + kindMeta(item.kind).cls;
    b.textContent = kindMeta(item.kind).badge;
    ghost.appendChild(b);
    var label = document.createElement("span");
    label.textContent = item.name || item.sourceId || "";
    ghost.appendChild(label);
    document.body.appendChild(ghost);
    // 拖影元素必须留在 DOM 里直到拖拽开始，下一次绘制后再移除
    setTimeout(function () { ghost.remove(); }, 0);
    return ghost;
  }

  function assignToSelected(ctrl     , item     , derived         ) {
    if (derived) {
      if (ctrl.addAudioClipFromDrop) {
        ctrl.addAudioClipFromDrop(ctrl.selectedTrackIdx, item.path, item.name, ctrl.snapBeat(ctrl.playheadBeat));
      }
      return;
    }
    if (ctrl.assignRackItem) ctrl.assignRackItem(item, ctrl.selectedTrackIdx, item.presetId || "");
  }

  /* openItemMenu 条目 ⋯ 菜单（复用控制器右键菜单组件） */
  function openItemMenu(ctrl     , item     , derived         , x        , y        ) {
    if (!ctrl.showMenu) return;
    var menu        = [{ heading: kindLabel(item.kind) + " · " + (item.name || "") }];
    menu.push({ label: "▷ 试听", action: function () { if (ctrl.previewRackItem) ctrl.previewRackItem(item); } });
    menu.push({
      label: derived ? "⇢ 放入当前轨道" : "⇢ 分配到选中轨道",
      action: function () { assignToSelected(ctrl, item, derived); }
    });
    if (!derived) {
      menu.push({
        label: "🎚 选择预设…",
        action: function () { openPresetPicker(ctrl, item); }
      });
      menu.push("-");
      menu.push({
        label: "🎛 打开插件界面",
        disabled: item.kind !== "vst3" && item.kind !== "mm",
        action: function () { ctrl.showHUD("插件界面将在 P5/P6 里程碑开放"); }
      });
      menu.push({
        label: "🔎 在声音管理中查看",
        disabled: isBuiltinSourceId(item.sourceId),
        action: function () {
          try {
            localStorage.setItem("melodymate-open-sound-manage", item.sourceId || "");
            window.location.href = "/settings?pane=paneSound";
          } catch (e) {}
        }
      });
      menu.push("-");
      menu.push({
        label: "🗑 移出机架",
        danger: true,
        action: function () { if (ctrl.removeRackItem) ctrl.removeRackItem(item.id); }
      });
    }
    ctrl.showMenu(menu, x, y);
  }

  function openPresetPicker(ctrl     , item     ) {
    presetList(item).then(function (presets       ) {
      if (!presets.length) {
        ctrl.showHUD("该音源没有可选预设");
        return;
      }
      var menu        = [{ heading: "预设 · " + (item.name || "") }];
      presets.slice(0, 64).forEach(function (p     ) {
        menu.push({
          label: p.name + "  (" + p.id + ")",
          action: function () { if (ctrl.assignRackItem) ctrl.assignRackItem(item, ctrl.selectedTrackIdx, p.id); }
        });
      });
      var el = document.getElementById("arrRackAddSourceBtn");
      var r = el ? el.getBoundingClientRect() : { left: 200, bottom: 200 };
      ctrl.showMenu(menu, r.left, r.bottom + 4);
    });
  }

  function openPickerFor(ctrl     , opts     ) {
    openPicker({
      title: opts.title || "选择音源",
      currentTrack: ctrl.selectedTrackIdx,
      items: function () { return rackItems(ctrl); },
      ensureItem: function (sourceId        , presetId        , name        ) {
        if (ctrl.ensureRackItem) return ctrl.ensureRackItem(sourceId, presetId, name);
        var item = itemFromSourceId(sourceId, presetId, name);
        return Promise.resolve(item);
      },
      onPick: function (item     , presetId        ) {
        if (opts.assign === false) {
          // 「＋ 加载音源」入口：只入架，不分配（拖到轨道才是分配）
          UI.toast ("✓ 已加入机架：" + (item.name || ""), "ok");
          return;
        }
        if (ctrl.assignRackItem) ctrl.assignRackItem(item, opts.trackIdx, presetId);
      },
      onPreview: function (item     , presetId        , btn     ) {
        if (ctrl.previewRackItem) ctrl.previewRackItem(item, btn, presetId);
      }
    });
  }

  /* ═══════════ 音源选择器 .snd-picker（三处共用） ═══════════ */

  var pickerEls      = null;
  var pickerState      = {
    open: false,
    opts: null,
    query: "",
    filter: "all",
    rows: [],
    active: 0,
    presetItem: null,
    presetRows: [],
    presetActive: 0,
    loading: false
  };

  function buildPicker() {
    if (pickerEls) return pickerEls;
    var overlay = document.createElement("div");
    overlay.className = "modal-overlay snd-picker-overlay";
    overlay.id = "sndPickerOverlay";
    overlay.hidden = true;
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    overlay.setAttribute("aria-label", "选择音源");

    var card = document.createElement("div");
    card.className = "modal card brackets snd-picker";

    var title = document.createElement("div");
    title.className = "modal-title";
    title.textContent = "选择音源 · PICK A SOURCE";

    var toolbar = document.createElement("div");
    toolbar.className = "snd-picker-toolbar";
    var search = document.createElement("input");
    search.type = "search";
    search.className = "snd-search snd-picker-search";
    search.placeholder = "搜索声音管理中的音源…";
    search.setAttribute("aria-label", "搜索音源");
    toolbar.appendChild(search);

    var tabs = document.createElement("div");
    tabs.className = "snd-picker-tabs";
    [["all", "全部"], ["synth", "合成器"], ["sf2", "SF2"], ["vst3", "VST"], ["mm", "MM"], ["sample", "采样"]]
      .forEach(function (pair) {
        var b = document.createElement("button");
        b.type = "button";
        b.className = "rack-filter-btn snd-picker-tab" + (pair[0] === "all" ? " active" : "");
        b.dataset.tab = pair[0];
        b.textContent = pair[1];
        b.addEventListener("click", function () {
          pickerState.filter = pair[0];
          pickerState.active = 0;
          renderPicker();
          search.focus();
        });
        tabs.appendChild(b);
      });
    toolbar.appendChild(tabs);

    var body = document.createElement("div");
    body.className = "snd-picker-body";
    var main = document.createElement("div");
    main.className = "snd-picker-main";
    main.setAttribute("role", "listbox");
    main.setAttribute("aria-label", "音源列表");
    var presets = document.createElement("div");
    presets.className = "snd-picker-presets";
    presets.hidden = true;
    body.appendChild(main);
    body.appendChild(presets);

    var foot = document.createElement("div");
    foot.className = "snd-picker-foot";
    foot.textContent = "↑↓ 选择 · Enter 确认 · Esc 关闭 · Tab 切页签";

    card.appendChild(title);
    card.appendChild(toolbar);
    card.appendChild(body);
    card.appendChild(foot);
    overlay.appendChild(card);
    document.body.appendChild(overlay);

    overlay.addEventListener("click", function (e) {
      if (e.target === overlay) closePicker();
    });
    search.addEventListener("input", function () {
      pickerState.query = search.value;
      pickerState.active = 0;
      renderPicker();
    });
    overlay.addEventListener("keydown", function (e     ) { pickerKeydown(e); });

    pickerEls = { overlay: overlay, card: card, title: title, search: search, tabs: tabs, main: main, presets: presets, foot: foot };
    return pickerEls;
  }

  function openPicker(opts     ) {
    var els = buildPicker();
    pickerState.open = true;
    pickerState.opts = opts || {};
    pickerState.query = "";
    pickerState.filter = "all";
    pickerState.active = 0;
    pickerState.presetItem = null;
    pickerState.presetRows = [];
    pickerState.loading = true;
    els.presets.hidden = true;
    els.search.value = "";
    els.search.placeholder = opts && opts.placeholder ? opts.placeholder : "搜索声音管理中的音源…";
    els.title.textContent = (opts && opts.title ? opts.title : "选择音源") + " · PICK A SOURCE";
    UI.qsa(".snd-picker-tab", els.tabs).forEach(function (t     ) {
      if (t.dataset.tab === "all") t.classList.add("active");
      else t.classList.remove("active");
    });
    els.overlay.hidden = false;
    els.card.classList.remove("modal-out");
    els.card.classList.add("modal-in");
    renderPicker();
    setTimeout(function () { try { els.search.focus(); } catch (e) {} }, 30);

    // 注册表可能还没加载过：拉到后重绘一次。
    // 已加载的情形必须同步重绘（否则骨架屏会一直挂着——loading 落了但没人重画）
    if (window.SoundRegistry && !window.SoundRegistry.all().length) {
      window.SoundRegistry.load().then(function () {
        pickerState.loading = false;
        if (pickerState.open) renderPicker();
      }).catch(function () {
        pickerState.loading = false;
        if (pickerState.open) renderPicker();
      });
    } else {
      pickerState.loading = false;
      if (pickerState.open) renderPicker();
    }
  }

  function closePicker() {
    if (!pickerEls || !pickerState.open) return;
    pickerState.open = false;
    var card = pickerEls.card;
    card.classList.remove("modal-in");
    card.classList.add("modal-out");
    var overlay = pickerEls.overlay;
    setTimeout(function () {
      if (!pickerState.open) overlay.hidden = true;
    }, 200);
  }

  function isPickerOpen() {
    return !!pickerState.open;
  }

  /** refreshPicker 机架变化时刷新已打开的选择器（"已在架 / 其他音源"两段
      会随入架结果移动；重新 open 会清空搜索词，因此只重绘不动状态） */
  function refreshPicker() {
    if (pickerState.open) renderPicker();
  }

  /* pickerCandidates 「声音管理中的其他音源」候选：内置虚拟音源 + 注册表乐器 */
  function pickerCandidates() {
    var out        = [];
    SYNTH_WAVES.forEach(function (w) {
      out.push({ kind: "synth", sourceId: BUILTIN_SYNTH, presetId: w.id, name: "合成器 · " + w.name, builtin: true });
    });
    GM_PRESETS.forEach(function (p) {
      out.push({ kind: "sf2", sourceId: BUILTIN_GM, presetId: p.id, name: "内置 · " + p.name, builtin: true });
    });
    var reg = window.SoundRegistry;
    if (reg) {
      ["sf2", "vst3", "mm"].forEach(function (kind) {
        reg.enabledOf(kind).forEach(function (src     ) {
          out.push({
            kind: registryKindToRackKind(src.kind),
            sourceId: src.id,
            presetId: defaultPresetFor(registryKindToRackKind(src.kind)),
            name: src.name || src.id,
            src: src
          });
        });
      });
    }
    return out;
  }

  /* buildPickerRows 两段式：已在机架 / 声音管理中的其他音源 */
  function buildPickerRows() {
    var opts = pickerState.opts || {};
    var items = (opts.items ? opts.items() : []) || [];
    var q = (pickerState.query || "").trim().toLowerCase();
    var filter = pickerState.filter || "all";
    var inRack        = [];
    var others        = [];

    items.forEach(function (it     ) {
      if (filter !== "all" && !matchFilter(it.kind, filter)) return;
      if (q && [it.name, it.sourceId, it.presetId, kindLabel(it.kind)].join(" ").toLowerCase().indexOf(q) < 0) return;
      inRack.push({ item: it, inRack: true, derived: false });
    });

    if (opts.allowAdd !== false) {
      pickerCandidates().forEach(function (cand     ) {
        // 同源同预设已在架 → 不重复列
        var dup = false;
        for (var i = 0; i < items.length; i++) {
          if (items[i].sourceId === cand.sourceId && (items[i].presetId || "") === (cand.presetId || "")) { dup = true; break; }
        }
        if (dup) return;
        if (filter !== "all" && !matchFilter(cand.kind, filter)) return;
        if (q && [cand.name, cand.sourceId, cand.presetId, kindLabel(cand.kind)].join(" ").toLowerCase().indexOf(q) < 0) return;
        others.push({ cand: cand, inRack: false });
      });
    }
    return { inRack: inRack, others: others };
  }

  function matchFilter(kind        , filter        ) {
    if (filter === "sample") return kind === "sample" || kind === "pack";
    return kind === filter;
  }

  function renderPicker() {
    if (!pickerState.open || !pickerEls) return;
    if (pickerState.presetItem) {
      renderPresetPane();
      return;
    }
    pickerEls.presets.hidden = true;
    pickerEls.card.classList.remove("preset-open");
    var main = pickerEls.main;
    main.innerHTML = "";

    var grouped = buildPickerRows();
    pickerState.rows = [];
    grouped.inRack.forEach(function (r     ) { pickerState.rows.push(r); });
    grouped.others.forEach(function (r     ) { pickerState.rows.push(r); });

    if (pickerState.loading) {
      var loading = document.createElement("div");
      loading.className = "snd-picker-loading";
      loading.innerHTML = '<span class="snd-picker-skel"></span><span class="snd-picker-skel"></span><span class="snd-picker-skel"></span>';
      main.appendChild(loading);
      return;
    }

    if (!pickerState.rows.length) {
      var empty = document.createElement("div");
      empty.className = "snd-picker-empty";
      empty.textContent = "没有匹配的音源。可在设置页「声音管理」添加 SF2 / 采样包。";
      main.appendChild(empty);
      return;
    }

    if (grouped.inRack.length) {
      main.appendChild(sectionHead("已在机架", grouped.inRack.length));
      grouped.inRack.forEach(function (r     , i        ) { main.appendChild(pickerRow(r, i)); });
    }
    if (grouped.others.length) {
      main.appendChild(sectionHead("声音管理中的其他音源", grouped.others.length));
      grouped.others.forEach(function (r     , i        ) { main.appendChild(pickerRow(r, grouped.inRack.length + i)); });
    }
    updatePickerActive();
  }

  function sectionHead(text        , count        ) {
    var h = document.createElement("div");
    h.className = "snd-picker-section";
    var name = document.createElement("span");
    name.textContent = text;
    h.appendChild(name);
    var cnt = document.createElement("span");
    cnt.className = "snd-picker-count";
    cnt.textContent = String(count);
    h.appendChild(cnt);
    return h;
  }

  function pickerRow(row     , idx        ) {
    var el = document.createElement("div");
    el.className = "snd-picker-row";
    el.dataset.idx = String(idx);
    el.setAttribute("role", "option");
    el.tabIndex = -1;

    var item = row.inRack ? row.item : null;
    var cand = row.cand;
    var kind = item ? item.kind : cand.kind;
    var name = item ? item.name : cand.name;
    var presetId = item ? (item.presetId || "") : (cand.presetId || "");
    var aiFlag = item ? isAIItem(item) : !!(cand.src && window.SoundRegistry && window.SoundRegistry.isAI(cand.src));

    var dot = document.createElement("span");
    dot.className = "snd-picker-dot" + (row.inRack ? " on" : "");
    dot.textContent = row.inRack ? "●" : "○";
    el.appendChild(dot);

    el.appendChild(badgeEl(kind));

    var info = document.createElement("div");
    info.className = "snd-picker-info";
    var nameLine = document.createElement("div");
    nameLine.className = "snd-picker-name";
    nameLine.textContent = name;
    if (aiFlag) nameLine.appendChild(aiBadge());
    info.appendChild(nameLine);
    var meta = document.createElement("div");
    meta.className = "snd-picker-meta";
    meta.textContent = row.inRack ? (itemMetaText(item) || kindLabel(kind)) : candidateMeta(cand);
    info.appendChild(meta);
    el.appendChild(info);

    var actions = document.createElement("div");
    actions.className = "snd-picker-actions";
    var previewBtn = document.createElement("button");
    previewBtn.type = "button";
    previewBtn.className = "snd-icon-btn action-preview";
    previewBtn.textContent = "▷";
    previewBtn.title = "试听";
    previewBtn.setAttribute("aria-label", "试听");
    previewBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      doPreview(item, cand, presetId, previewBtn);
    });
    actions.appendChild(previewBtn);

    var pill = document.createElement("span");
    pill.className = "snd-picker-pill" + (row.inRack ? " on" : "");
    pill.textContent = row.inRack ? "已在架" : "＋ 入架";
    actions.appendChild(pill);
    el.appendChild(actions);

    el.addEventListener("click", function () { confirmPickerRow(idx); });
    el.addEventListener("mouseenter", function () {
      pickerState.active = idx;
      updatePickerActive();
    });
    el.addEventListener("dblclick", function (e) {
      e.preventDefault();
      if (row.inRack) {
        var it = row.item;
        if (it.kind !== "sample" && it.kind !== "pack") openPresetPane(it);
        else confirmPickerRow(idx);
      }
    });
    return el;
  }

  function candidateMeta(cand     ) {
    var bits           = [kindLabel(cand.kind)];
    if (cand.src) {
      var rt = cand.src.runtime || {};
      if (rt.presetCount) bits.push(rt.presetCount + " 预设");
      if (rt.missing) bits.push("⚠ 文件缺失");
      if (cand.src.vendor) bits.push(cand.src.vendor);
      if (window.SoundRegistry && window.SoundRegistry.isBuiltin(cand.src)) bits.push("内置");
    } else {
      bits.push("内置");
    }
    if (cand.presetId) bits.push(cand.presetId);
    return bits.join(" · ");
  }

  function updatePickerActive() {
    if (!pickerEls) return;
    var rows = UI.qsa(".snd-picker-row", pickerEls.main);
    rows.forEach(function (el     , i        ) {
      if (i === pickerState.active) {
        el.classList.add("active");
        el.setAttribute("aria-selected", "true");
      } else {
        el.classList.remove("active");
        el.setAttribute("aria-selected", "false");
      }
    });
  }

  function scrollActiveIntoView() {
    if (!pickerEls) return;
    var el = pickerEls.main.querySelector(".snd-picker-row.active");
    if (el && el.scrollIntoView) {
      try { el.scrollIntoView({ block: "nearest" }); } catch (e) { el.scrollIntoView(); }
    }
  }

  /* confirmPickerRow 选中一行：带预设的条目 → 预设二级视图，其余直接确认 */
  function confirmPickerRow(idx        ) {
    var row = pickerState.rows[idx];
    if (!row) return;
    if (!row.inRack) {
      var cand = row.cand;
      ensureCandidate(cand).then(function (item     ) {
        if (!item) return;
        if (cand.kind === "synth" || cand.kind === "sample" || cand.kind === "pack") finishPick(item, cand.presetId || item.presetId || "");
        else openPresetPane(item);
      });
      return;
    }
    var item = row.item;
    if (item.kind === "sample" || item.kind === "pack") {
      finishPick(item, item.presetId || "");
      return;
    }
    openPresetPane(item);
  }

  function ensureCandidate(cand     ) {
    var opts = pickerState.opts || {};
    if (opts.ensureItem) return Promise.resolve(opts.ensureItem(cand.sourceId, cand.presetId, cand.name));
    return Promise.resolve(itemFromSourceId(cand.sourceId, cand.presetId, cand.name));
  }

  function finishPick(item     , presetId        ) {
    var opts = pickerState.opts || {};
    closePicker();
    if (opts.onPick) opts.onPick(item, presetId || "");
  }

  /* ── 预设二级视图（sc-in-right 滑入） ── */

  function openPresetPane(item     ) {
    pickerState.presetItem = item;
    pickerState.presetRows = [];
    pickerState.presetActive = 0;
    if (pickerEls) {
      pickerEls.presets.hidden = false;
      pickerEls.card.classList.add("preset-open");
      pickerEls.presets.classList.remove("sc-in-right");
      void pickerEls.presets.offsetWidth;
      pickerEls.presets.classList.add("sc-in-right");
    }
    renderPresetPane();
    presetList(item).then(function (presets       ) {
      pickerState.presetRows = presets;
      if (pickerState.presetItem === item) renderPresetPane();
    });
  }

  function renderPresetPane() {
    if (!pickerEls) return;
    var item = pickerState.presetItem;
    if (!item) return;
    var pane = pickerEls.presets;
    pane.innerHTML = "";

    var head = document.createElement("div");
    head.className = "snd-picker-preset-head";
    var back = document.createElement("button");
    back.type = "button";
    back.className = "btn btn-secondary btn-sm";
    back.textContent = "‹ 返回";
    back.addEventListener("click", function () {
      pickerState.presetItem = null;
      renderPicker();
      try { pickerEls.search.focus(); } catch (e) {}
    });
    head.appendChild(back);
    var crumb = document.createElement("span");
    crumb.className = "snd-picker-crumb";
    crumb.textContent = (item.name || "") + " › 预设";
    head.appendChild(crumb);
    pane.appendChild(head);

    if (!pickerState.presetRows.length) {
      var empty = document.createElement("div");
      empty.className = "snd-picker-empty";
      empty.textContent = "该音源没有可选预设（或尚未解析出预设表）";
      pane.appendChild(empty);
      return;
    }
    pickerState.presetRows.forEach(function (p     , i        ) {
      var row = document.createElement("div");
      row.className = "snd-picker-preset-row" + (i === pickerState.presetActive ? " active" : "");
      row.setAttribute("role", "option");
      var nm = document.createElement("span");
      nm.className = "snd-picker-name";
      nm.textContent = p.name || p.id;
      row.appendChild(nm);
      var id = document.createElement("span");
      id.className = "snd-picker-pid";
      id.textContent = p.id;
      row.appendChild(id);
      row.addEventListener("click", function () { finishPick(item, p.id); });
      row.addEventListener("mouseenter", function () {
        pickerState.presetActive = i;
        UI.qsa(".snd-picker-preset-row", pane).forEach(function (r     , j        ) {
          r.classList.toggle("active", j === i);
        });
      });
      pane.appendChild(row);
    });
  }

  function previewPreset(presetId        ) {
    var opts = pickerState.opts || {};
    if (opts.onPreview) opts.onPreview(pickerState.presetItem, presetId, null);
  }

  function doPreview(item     , cand     , presetId        , btn     ) {
    var opts = pickerState.opts || {};
    if (item) {
      if (opts.onPreview) opts.onPreview(item, presetId, btn);
      return;
    }
    ensureCandidate(cand).then(function (it     ) {
      if (it && opts.onPreview) opts.onPreview(it, cand.presetId || "", btn);
    });
  }

  /* pickerKeydown 键盘：↑↓ 选择 · Enter 确认 · Esc 分层关闭 · Tab 切页签 */
  function pickerKeydown(e     ) {
    if (!pickerState.open) return;
    var inPreset = !!pickerState.presetItem;
    var list = inPreset ? pickerState.presetRows : pickerState.rows;

    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      if (inPreset) {
        pickerState.presetItem = null;
        renderPicker();
        try { pickerEls.search.focus(); } catch (err) {}
      } else {
        closePicker();
      }
      return;
    }
    if (e.key === "Tab") {
      e.preventDefault();
      var order = ["all", "synth", "sf2", "vst3", "mm", "sample"];
      var cur = order.indexOf(pickerState.filter);
      var next = order[(cur + (e.shiftKey ? order.length - 1 : 1)) % order.length];
      pickerState.filter = next;
      pickerState.active = 0;
      UI.qsa(".snd-picker-tab", pickerEls.tabs).forEach(function (t     ) {
        t.classList.toggle("active", t.dataset.tab === next);
      });
      renderPicker();
      return;
    }
    if (!list.length) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      var delta = e.key === "ArrowDown" ? 1 : -1;
      if (inPreset) {
        pickerState.presetActive = (pickerState.presetActive + delta + list.length) % list.length;
        renderPresetPane();
      } else {
        pickerState.active = (pickerState.active + delta + list.length) % list.length;
        updatePickerActive();
        scrollActiveIntoView();
      }
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      if (inPreset) {
        var p = pickerState.presetRows[pickerState.presetActive];
        if (p) finishPick(pickerState.presetItem, p.id);
      } else {
        confirmPickerRow(pickerState.active);
      }
      return;
    }
    if (e.key === " " && inPreset) {
      e.preventDefault();
      var pr = pickerState.presetRows[pickerState.presetActive];
      if (pr) previewPreset(pr.id);
    }
  }

  /* ═══════════ 对外出口 ═══════════ */

  window.ArrRack = {
    KIND_META: KIND_META,
    RACK_FILTERS: RACK_FILTERS,
    SYNTH_WAVES: SYNTH_WAVES,
    GM_PRESETS: GM_PRESETS,
    BUILTIN_SYNTH: BUILTIN_SYNTH,
    BUILTIN_GM: BUILTIN_GM,
    MAX_ITEMS: MAX_ITEMS,
    /* 纯函数（可单测） */
    kindMeta: kindMeta,
    kindLabel: kindLabel,
    waveName: waveName,
    gmPresetName: gmPresetName,
    isBuiltinSourceId: isBuiltinSourceId,
    utf8Bytes: utf8Bytes,
    fnv1a64: fnv1a64,
    rackItemId: rackItemId,
    makeItem: makeItem,
    synthItem: synthItem,
    gmItem: gmItem,
    sampleItem: sampleItem,
    registryItem: registryItem,
    registryKindToRackKind: registryKindToRackKind,
    rackKindToRegistryKind: rackKindToRegistryKind,
    defaultPresetFor: defaultPresetFor,
    itemFromSourceId: itemFromSourceId,
    migrateV1Source: migrateV1Source,
    generalUserSourceId: generalUserSourceId,
    find: find,
    upsert: upsert,
    cloneItem: cloneItem,
    resolve: resolve,
    resolveSource: resolveSource,
    presetList: presetList,
    itemMetaText: itemMetaText,
    isAIItem: isAIItem,
    isBuiltinItem: isBuiltinItem,
    voiceDetail: voiceDetail,
    setItems: setItems,
    items: items,
    /* 视图 */
    render: render,
    visibleRows: visibleRows,
    derivedSamples: derivedSamples,
    openPicker: openPicker,
    closePicker: closePicker,
    isPickerOpen: isPickerOpen,
    refreshPicker: refreshPicker,
    openPickerFor: openPickerFor,
    openPresetPane: openPresetPane,
    /* 测试钩子：驱动选择器键盘/状态而不依赖真实拖动 */
    _picker: pickerState,
    _pickerEls: function () { return pickerEls; }
  };
})(window);
