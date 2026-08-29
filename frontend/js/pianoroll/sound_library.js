/* IndexedDB 永久音源库存储与预设管理器 */
(function (window) {
  "use strict";

  var DB_NAME = "AI_MIDI_SoundLibrary";
  var DB_VERSION = 1;
  var STORE_NAME = "soundfonts";

  function SoundLibrary() {
    this.db = null;
    this._cachedFonts = [];
    this._initPromise = null;
  }

  SoundLibrary.prototype.init = function () {
    var self = this;
    if (this._initPromise) return this._initPromise;
    this._initPromise = new Promise(function (resolve, reject) {
      if (self.db) { resolve(self.db); return; }
      var req = indexedDB.open(DB_NAME, DB_VERSION);
      req.onupgradeneeded = function (e) {
        var db = e.target.result;
        if (!db.objectStoreNames.contains(STORE_NAME)) {
          var store = db.createObjectStore(STORE_NAME, { keyPath: "id" });
          store.createIndex("name", "name", { unique: false });
          store.createIndex("createdAt", "createdAt", { unique: false });
        }
      };
      req.onsuccess = function (e) {
        self.db = e.target.result;
        resolve(self.db);
      };
      req.onerror = function (e) {
        reject(e.target.error);
      };
    });
    return this._initPromise;
  };

  SoundLibrary.prototype.getCachedSoundFonts = function () {
    return this._cachedFonts || [];
  };

  SoundLibrary.prototype.listSoundFonts = function () {
    var self = this;
    return this.init().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(STORE_NAME, "readonly");
        var store = tx.objectStore(STORE_NAME);
        var list = [];
        // 游标逐条读取并剥离 data 字段：getAll 会一次性把所有记录的
        // 完整 ArrayBuffer（每个可能数十 MB）物化进内存，仅为列目录
        var cursorReq = store.openCursor();
        cursorReq.onsuccess = function () {
          var cursor = cursorReq.result;
          if (!cursor) {
            self._cachedFonts = list;
            resolve(list);
            return;
          }
          var item = cursor.value;
          list.push({
            id: item.id,
            name: item.name,
            size: item.size,
            presetsCount: item.presets ? item.presets.length : 0,
            presets: item.presets || [],
            diskPath: item.diskPath || "", // 服务器落盘绝对路径（旧记录可能没有）
            createdAt: item.createdAt
          });
          cursor.continue();
        };
        cursorReq.onerror = function () { reject(cursorReq.error); };
      });
    });
  };

  SoundLibrary.prototype.saveSoundFont = function (name, arrayBuffer, presets) {
    var self = this;
    // 镜像到原生引擎音色库（Library/soundfonts/）：失败仅告警，不影响浏览器音源。
    // 响应里的 saved 为服务器落盘绝对路径——存入记录供原生引擎直通加载，
    // 否则加载时只能靠"文件名消毒规则 + 引擎进程 CWD"间接推导（CWD 不对
    // 时原生音源会静默失效回退 WebAudio）
    var diskPathPromise = null;
    try {
      diskPathPromise = fetch("/api/audio/soundfonts?name=" + encodeURIComponent(name), {
        method: "POST",
        headers: { "Content-Type": "application/octet-stream" },
        body: arrayBuffer
      }).then(function (res) { return res.json(); })
        .then(function (j) {
          console.log("[SoundLibrary] 已同步到引擎音色库", j);
          return (j && j.saved) ? j.saved : null;
        })
        .catch(function (err) {
          console.warn("[SoundLibrary] 引擎同步失败（浏览器音源不受影响）", err);
          return null;
        });
    } catch (e) { /* 非阻塞 */ diskPathPromise = Promise.resolve(null); }
    var id = "sf2_" + Date.now() + "_" + Math.random().toString(36).substr(2, 6);
    return diskPathPromise.then(function (diskPath) {
      return self.init().then(function (db) {
        return new Promise(function (resolve, reject) {
          var tx = db.transaction(STORE_NAME, "readwrite");
          var store = tx.objectStore(STORE_NAME);
          var record = {
            id: id,
            name: name,
            size: arrayBuffer.byteLength,
            presets: presets || [],
            data: arrayBuffer,
            diskPath: diskPath || "",
            createdAt: Date.now()
          };
          var req = store.put(record);
          req.onsuccess = function () { /* 等 tx.oncomplete 再 resolve */ };
          req.onerror = function () { reject(req.error); };
          tx.onabort = function () { reject(tx.error || new Error("音色库写入被中止")); };
          // 事务提交（而非 put 成功）才算落库——IndexedDB 配额错误在
          // commit 阶段才暴露；随后刷新缓存，调用方拿到的列表即包含新条目
          tx.oncomplete = function () {
            self.listSoundFonts().then(
              function () { resolve(record); },
              function () { resolve(record); }
            );
          };
        });
      });
    });
  };

  SoundLibrary.prototype.getSoundFont = function (id) {
    var self = this;
    return this.init().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(STORE_NAME, "readonly");
        var store = tx.objectStore(STORE_NAME);
        var req = store.get(id);
        req.onsuccess = function () { resolve(req.result); };
        req.onerror = function () { reject(req.error); };
      });
    });
  };

  SoundLibrary.prototype.deleteSoundFont = function (id, name) {
    var self = this;
    // 同步删除磁盘镜像（Library/soundfonts/）：此前只删 IndexedDB 记录，
    // 镜像文件永久残留并被引擎当作默认音色加载。失败不影响本地删除
    if (name && window.UI && UI.delJSON) {
      UI.delJSON("/api/audio/soundfonts?name=" + encodeURIComponent(name))
        .catch(function (err) { console.warn("[SoundLibrary] 磁盘镜像删除失败（本地记录已删除）", err); });
    }
    return this.init().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(STORE_NAME, "readwrite");
        var store = tx.objectStore(STORE_NAME);
        var req = store.delete(id);
        req.onsuccess = function () { /* 等 tx.oncomplete 再 resolve */ };
        req.onerror = function () { reject(req.error); };
        tx.onabort = function () { reject(tx.error || new Error("音色库删除被中止")); };
        tx.oncomplete = function () {
          self.listSoundFonts().then(
            function () { resolve(true); },
            function () { resolve(true); }
          );
        };
      });
    });
  };

  window.SoundLibrary = new SoundLibrary();
  // 页面加载时自动预热音源库缓存
  try {
    window.SoundLibrary.listSoundFonts().catch(function () {});
  } catch (e) {}
})(window);
