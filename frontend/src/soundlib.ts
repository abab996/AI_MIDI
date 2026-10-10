/* 音源注册表前端出口（对应后端 internal/soundlib 与 /api/sound/*）。
 *
 * 全局唯一事实源：输出机架、钢琴窗音色选择、素材库与设置页「声音管理」
 * 都从这里取候选与元数据，不再各自扫 IndexedDB 或硬编码列表。
 * IndexedDB（pianoroll/sound_library.js）降级为浏览器模式（WebAudio）的
 * 解码缓存——引擎模式下发声只需要注册表里的磁盘路径与 bank:program。
 *
 * 缓存策略：内存缓存 + 变更后整体重取（注册表规模在几十条量级，
 * 增量同步的复杂度不值当）。写操作成功后本地失效并广播，各界面自行重绘。
 */
(function (window) {
  "use strict";

  var KIND_META: { [kind: string]: SoundKindMeta } = {
    sf2: { label: "音源 SF2", shortLabel: "SF2", badge: "SF2", icon: "🎹", cls: "snd-kind-sf2" },
    samplepack: { label: "采样包", shortLabel: "采样包", badge: "PACK", icon: "📁", cls: "snd-kind-pack" },
    vst3: { label: "VST 插件", shortLabel: "VST", badge: "VST3", icon: "🎛", cls: "snd-kind-vst" },
    mm: { label: "MM 插件", shortLabel: "MM", badge: "MM", icon: "🧩", cls: "snd-kind-mm" }
  };
  /* 设置页分组顺序（P5/P6 前 VST/MM 组只给引导空态） */
  var KIND_ORDER = ["sf2", "samplepack", "vst3", "mm"];

  var KINDS_VST: { [kind: string]: boolean } = { vst3: true, mm: true };

  function SoundRegistry(this: SoundRegistryApi) {
    this._sources = [];
    this._loaded = false;
    this._loading = null;
    this._presetCache = {};
    this._presetInflight = {};
    this._listeners = [];
  }

  /* ===== 元数据 ===== */

  SoundRegistry.prototype.kindMeta = function (this: SoundRegistryApi, kind: string) {
    return KIND_META[kind] || { label: kind || "音源", shortLabel: kind || "?", badge: "?", icon: "♪", cls: "" };
  };

  SoundRegistry.prototype.kindOrder = function () {
    return KIND_ORDER.slice();
  };

  /* VST3 / MM 属于插件（可作乐器或效果器），P1 只登记不消费 */
  SoundRegistry.prototype.isPluginKind = function (kind: string) {
    return !!KINDS_VST[kind];
  };

  SoundRegistry.prototype.isAI = function (src: any) {
    return !!(src && (src.origin === "ai" || src.ai));
  };

  SoundRegistry.prototype.isBuiltin = function (src: any) {
    return !!(src && src.origin === "builtin");
  };

  SoundRegistry.prototype.originLabel = function (src: any) {
    if (!src) return "";
    if (src.origin === "ai") return "AI";
    if (src.origin === "builtin") return "内置";
    return "用户";
  };

  /* ===== 读取 ===== */

  SoundRegistry.prototype.onChange = function (this: SoundRegistryApi, fn: () => void) {
    if (typeof fn === "function") this._listeners.push(fn);
  };

  SoundRegistry.prototype._emit = function (this: SoundRegistryApi) {
    for (var i = 0; i < this._listeners.length; i++) {
      try { this._listeners[i](); } catch (e) { /* 单个订阅者出错不影响其它界面 */ }
    }
  };

  /* load 拉取注册表；已加载则直接返回缓存（force=true 强制重取） */
  SoundRegistry.prototype.load = function (this: SoundRegistryApi, force?: boolean) {
    var self = this;
    if (this._loaded && !force) return Promise.resolve(this._sources);
    if (this._loading) return this._loading;
    this._loading = fetch("/api/sound/sources")
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (j) {
        self._sources = (j && j.sources) || [];
        self._loaded = true;
        self._loading = null;
        self._emit();
        return self._sources;
      })
      .catch(function (err) {
        self._loading = null;
        throw err;
      });
    return this._loading;
  };

  /* reload 写操作后调用：整体重取并广播 */
  SoundRegistry.prototype.reload = function (this: SoundRegistryApi) {
    this._presetCache = {};
    return this.load(true);
  };

  SoundRegistry.prototype.all = function (this: SoundRegistryApi) {
    return this._sources || [];
  };

  SoundRegistry.prototype.list = function (this: SoundRegistryApi, kind?: string) {
    var all = this.all();
    if (!kind) return all.slice();
    return all.filter(function (s: any) { return s.kind === kind; });
  };

  SoundRegistry.prototype.get = function (this: SoundRegistryApi, id: string) {
    if (!id) return null;
    var all = this.all();
    for (var i = 0; i < all.length; i++) {
      if (all[i].id === id) return all[i];
    }
    return null;
  };

  SoundRegistry.prototype.enabledOf = function (this: SoundRegistryApi, kind?: string) {
    return this.list(kind).filter(function (s: any) { return s.enabled !== false; });
  };

  /* query 关键词过滤（名称/厂商/标签/路径/文件名），大小写不敏感 */
  SoundRegistry.prototype.query = function (this: SoundRegistryApi, kind: string, q: string) {
    var list = this.list(kind);
    var kw = (q || "").trim().toLowerCase();
    if (!kw) return list;
    return list.filter(function (s: any) {
      var rt = s.runtime || {};
      var hay = [s.name, s.vendor, s.path, rt.fileName, (s.dirs || []).join(" "), (s.tags || []).join(" ")]
        .join(" ").toLowerCase();
      return hay.indexOf(kw) >= 0;
    });
  };

  /* countByKind 各组计数（设置页组头用） */
  SoundRegistry.prototype.countByKind = function (this: SoundRegistryApi, kind: string) {
    return this.list(kind).length;
  };

  /* ===== 预设 ===== */

  /* presets 取 SF2 预设列表（bank:program）。后端直接读 pdta，
     不依赖浏览器端 IndexedDB 记录，换机器/清缓存后仍可用。 */
  SoundRegistry.prototype.presets = function (this: SoundRegistryApi, id: string) {
    var self = this;
    if (!id) return Promise.resolve([]);
    var cached = this._presetCache[id];
    if (cached) return Promise.resolve(cached);
    var inflight = this._presetInflight[id];
    if (inflight) return inflight;
    var p = fetch("/api/sound/presets?id=" + encodeURIComponent(id))
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (j) {
        var presets = (j && j.presets) || [];
        self._presetCache[id] = presets;
        delete self._presetInflight[id];
        return presets;
      })
      .catch(function (err) {
        delete self._presetInflight[id];
        throw err;
      });
    this._presetInflight[id] = p;
    return p;
  };

  /* firstPreset 取首个预设 id（机架条目/拖拽分配时的默认音色） */
  SoundRegistry.prototype.firstPreset = function (this: SoundRegistryApi, id: string) {
    var cached = this._presetCache[id];
    if (cached && cached.length) return cached[0].id;

    // 未取过则异步预热，本次先返回 null（调用方会在就绪后重绘）
    if (!this._presetInflight[id]) {
      this.presets(id).catch(function () {});
    }
    return null;
  };

  /* ===== 写操作 ===== */

  function postJSON(url: string, body?: any, method?: string) {
    return fetch(url, {
      method: method || "POST",
      headers: { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) throw new Error((j && j.detail) || ("HTTP " + r.status));
        return j;
      });
    });
  }

  /* importSf2 上传 SF2 到注册表（落盘 + 入册）。
     同时把字节镜像进 IndexedDB，浏览器模式（WebAudio）才能解码播放。 */
  SoundRegistry.prototype.importSf2 = function (this: SoundRegistryApi, file: File, onProgress?: Function) {
    var self = this;
    if (onProgress) onProgress("读取文件…");
    var raw: ArrayBuffer = new ArrayBuffer(0);
    return file.arrayBuffer().then(function (buf: ArrayBuffer) {
      raw = buf;
      if (onProgress) onProgress("上传中…");
      return fetch("/api/sound/import/sf2?name=" + encodeURIComponent(file.name), {
        method: "POST",
        headers: { "Content-Type": "application/octet-stream" },
        body: buf
      });
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) throw new Error((j && j.detail) || "导入失败");
        var src = j.source;
        // 浏览器模式解码缓存：失败不影响导入结果（引擎模式走磁盘路径发声）
        try {
          if (window.SoundLibrary && window.SoundLibrary.saveSoundFont && src) {
            var presets: any[] = [];
            try {
              var sf = window.PianoRoll && window.PianoRoll.soundfont;
              var parsed = sf && sf.parseSF2 ? sf.parseSF2(raw) : null;
              if (parsed && parsed.presets) presets = parsed.presets;
            } catch (e) { presets = []; }
            window.SoundLibrary.saveSoundFont(file.name, raw, presets, src.id);
          }
        } catch (e) { /* 缓存写入失败仅告警 */ }
        return self.reload().then(function () { return j; });
      });
    });
  };

  SoundRegistry.prototype.addSamplePack = function (this: SoundRegistryApi, name: string, dirs: string[]) {
    var self = this;
    return postJSON("/api/sound/sources", { kind: "samplepack", name: name, dirs: dirs })
      .then(function (j) { return self.reload().then(function () { return j; }); });
  };

  SoundRegistry.prototype.update = function (this: SoundRegistryApi, id: string, patch: any) {
    var self = this;
    return postJSON("/api/sound/sources/" + encodeURIComponent(id), patch, "PUT")
      .then(function (j) { return self.reload().then(function () { return j; }); });
  };

  SoundRegistry.prototype.setEnabled = function (this: SoundRegistryApi, id: string, enabled: boolean) {
    return this.update(id, { enabled: !!enabled });
  };

  SoundRegistry.prototype.remove = function (this: SoundRegistryApi, id: string, opts?: any) {
    var self = this;
    var withFile = opts && opts.file ? "?file=1" : "";
    return fetch("/api/sound/sources/" + encodeURIComponent(id) + withFile, { method: "DELETE" })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) throw new Error((j && j.detail) || "删除失败");
          return self.reload().then(function () { return j; });
        });
      });
  };

  /* rescan 重新扫描磁盘（用户手动拷入音色库后不必重启程序） */
  SoundRegistry.prototype.rescan = function (this: SoundRegistryApi) {
    var self = this;
    return postJSON("/api/sound/migrate", {}).then(function (j) {
      return self.reload().then(function () { return j; });
    });
  };

  /* ===== 采样包内容 ===== */

  SoundRegistry.prototype.samples = function (this: SoundRegistryApi, packId: string, sub?: string) {
    var url = "/api/sound/samples?pack=" + encodeURIComponent(packId);
    if (sub) url += "&sub=" + encodeURIComponent(sub);
    return fetch(url).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) throw new Error((j && j.detail) || "读取采样包失败");
        return j;
      });
    });
  };

  /* ===== 与引擎/浏览器播放的桥接 ===== */

  /* diskPathOf 注册表条目的磁盘路径（引擎模式发声用）。
     后端返回的 runtime.absPath 已是绝对路径。 */
  SoundRegistry.prototype.diskPathOf = function (this: SoundRegistryApi, src: any) {
    if (!src) return "";
    if (src.runtime && src.runtime.absPath) return src.runtime.absPath;
    return "";
  };

  /* browserRecord 取浏览器模式（WebAudio）解码缓存对应的 IndexedDB 记录 */
  SoundRegistry.prototype.loadSf2 = function(this: SoundRegistryApi,sourceId: string,libId?: string) {
    var self=this;
    return this.load().then(function(){
      var src=self.get(sourceId);
      if(src && src.enabled===false)throw new Error("音源已禁用");
      return src?self.browserRecord(src).then(function(hit: any){return hit&&window.SoundLibrary?window.SoundLibrary.getSoundFont(hit.id):null;}):libId&&window.SoundLibrary?window.SoundLibrary.getSoundFont(libId):null;
    }).then(function(rec: any){
      if(rec?.data)return rec;
      return fetch("/api/sound/data?id="+encodeURIComponent(sourceId)).then(function(r){if(!r.ok)throw new Error("SF2 数据读取失败：HTTP "+r.status);return r.arrayBuffer();}).then(function(data){return {data:data,name:self.get(sourceId)?.name||sourceId};});
    });
  };

  SoundRegistry.prototype.browserRecord = function (this: SoundRegistryApi, src: any) {
    if (!src || !window.SoundLibrary) return Promise.resolve(null);
    return window.SoundLibrary.recordForSource(src.id, src.name).catch(function () { return null; });
  };

  /* parsePresetId 预设 id → bank/program。兼容注册表格式 "0:48" 与
     历史格式 "sf2_0_48_2"（含可选 idx 后缀）。引擎侧只认 bank/program，
     两种写法的解析集中在这里，避免各调用点各写一遍正则。 */
  SoundRegistry.prototype.parsePresetId = function (presetId: any) {
    var id = String(presetId || "");
    var m = /^(\d+):(\d+)$/.exec(id);
    if (m) return { bank: parseInt(m[1], 10) || 0, program: parseInt(m[2], 10) || 0 };
    var parts = id.replace(/^sf2_/, "").split("_");
    return { bank: parseInt(parts[0], 10) || 0, program: parseInt(parts[1], 10) || 0 };
  };

  /* sizeText / presetText 列表里展示的元信息（monospace 小字） */
  SoundRegistry.prototype.metaText = function (this: SoundRegistryApi, src: any) {
    var rt = (src && src.runtime) || {};
    var parts = [];
    if (src.kind === "sf2") {
      if (rt.missing) parts.push("文件缺失");
      if (rt.size) parts.push(fmtSize(rt.size));
      if (rt.presetCount) parts.push(rt.presetCount + " 预设");
    } else if (src.kind === "samplepack") {
      if (src.dirs && src.dirs.length) parts.push(src.dirs.length + " 个目录");
      if (rt.sampleFiles) parts.push(rt.sampleFiles + " 文件");
    }
    return parts.join(" · ");
  };

  function fmtSize(bytes: number) {
    if (window.UI && UI.fmtSize) return UI.fmtSize(bytes);
    if (bytes >= 1 << 20) return (bytes / (1 << 20)).toFixed(1) + "MB";
    if (bytes >= 1 << 10) return Math.round(bytes / 1024) + "KB";
    return bytes + "B";
  }

  window.SoundRegistry = new (SoundRegistry as any)();
  /* 预取：机架/钢琴窗/设置页都可能随用户操作首次打开，提前拉一次 */
  try { window.SoundRegistry.load().catch(function () {}); } catch (e) {}
})(window);
