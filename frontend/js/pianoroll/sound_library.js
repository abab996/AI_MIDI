/* IndexedDB 永久音源库存储与预设管理器 */
(function (window) {
  "use strict";

  var DB_NAME = "AI_MIDI_SoundLibrary";
  var DB_VERSION = 1;
  var STORE_NAME = "soundfonts";

  function SoundLibrary() {
    this.db = null;
  }

  SoundLibrary.prototype.init = function () {
    var self = this;
    return new Promise(function (resolve, reject) {
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
          if (!cursor) { resolve(list); return; }
          var item = cursor.value;
          list.push({
            id: item.id,
            name: item.name,
            size: item.size,
            presetsCount: item.presets ? item.presets.length : 0,
            presets: item.presets || [],
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
    var id = "sf2_" + Date.now() + "_" + Math.random().toString(36).substr(2, 6);
    return this.init().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(STORE_NAME, "readwrite");
        var store = tx.objectStore(STORE_NAME);
        var record = {
          id: id,
          name: name,
          size: arrayBuffer.byteLength,
          presets: presets || [],
          data: arrayBuffer,
          createdAt: Date.now()
        };
        var req = store.put(record);
        req.onsuccess = function () { resolve(record); };
        req.onerror = function () { reject(req.error); };
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

  SoundLibrary.prototype.deleteSoundFont = function (id) {
    var self = this;
    return this.init().then(function (db) {
      return new Promise(function (resolve, reject) {
        var tx = db.transaction(STORE_NAME, "readwrite");
        var store = tx.objectStore(STORE_NAME);
        var req = store.delete(id);
        req.onsuccess = function () { resolve(true); };
        req.onerror = function () { reject(req.error); };
      });
    });
  };

  window.SoundLibrary = new SoundLibrary();
})(window);
