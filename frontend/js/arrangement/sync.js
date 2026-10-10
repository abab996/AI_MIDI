/* Project snapshot merge and durable recovery drafts. No audio/DOM dependencies. */
(function (window        ) {
  "use strict";
  function copy(x     )      { return x === undefined ? undefined : JSON.parse(JSON.stringify(x)); }
  function equal(a     , b     )          {
    if (a === b) return true;
    if (!a || !b || typeof a !== "object" || typeof b !== "object" || Array.isArray(a) !== Array.isArray(b)) return false;
    var keys = Object.keys(a), other = Object.keys(b);
    return keys.length === other.length && keys.every(function (k) { return Object.prototype.hasOwnProperty.call(b, k) && equal(a[k], b[k]); });
  }
  function merge(base     , local     , remote     ) {
    var conflicts           = [];
    function visit(b     , l     , r     , path        )      {
      if (equal(l, r)) return copy(l);
      if (equal(b, l)) return copy(r);
      if (equal(b, r)) return copy(l);
      if (path === "rev") return r;
      if (Array.isArray(b) && Array.isArray(l) && Array.isArray(r)) {
        var field = path.split(".").pop();
        var key = field === "fx" ? "slot" : (field === "tracks" || field === "items" || field === "clips") ? "id" : "";
        if (key && [b,l,r].every(function (a) { return a.every(function (v     ) { return v && v[key] !== undefined; }) && new Set(a.map(function(v     ){return String(v[key]);})).size === a.length; })) {
          var maps = [b,l,r].map(function (a) { var m      = Object.create(null); a.forEach(function(v     ){m[String(v[key])] = v;}); return m; });
          var ids = Array.from(new Set(l.concat(r,b).map(function (v     ) { return String(v[key]); })));
          var values      = Object.create(null);
          ids.forEach(function (id) { var v = visit(maps[0][id], maps[1][id], maps[2][id], path + "[" + id + "]"); if(v !== undefined) values[id] = v; });
          var surviving = ids.filter(function(id){return values[id] !== undefined;});
          var common = b.map(function(v     ){return String(v[key]);}).filter(function(id        ){return maps[1][id] && maps[2][id] && values[id];});
          var orders = [b,l,r].map(function(a){return a.map(function(v     ){return String(v[key]);}).filter(function(id        ){return common.indexOf(id)>=0;});});
          if (!equal(orders[0],orders[1]) && !equal(orders[0],orders[2]) && !equal(orders[1],orders[2])) conflicts.push(path + ".order");
          var chosen = !equal(orders[0],orders[1]) ? l : r;
          var order = chosen.map(function(v     ){return String(v[key]);}).filter(function(id        ){return values[id] !== undefined;});
          surviving.forEach(function(id){if(order.indexOf(id)<0) order.push(id);});
          return order.map(function(id        ){return values[id];});
        }
      } else if (b && l && r && !Array.isArray(b) && !Array.isArray(l) && !Array.isArray(r) && typeof b === "object" && typeof l === "object" && typeof r === "object" && path.split(".").pop() !== "source") {
        var out      = {};
        Array.from(new Set(Object.keys(b).concat(Object.keys(l),Object.keys(r)))).forEach(function(k){var v=visit(b[k],l[k],r[k],path?path+"."+k:k);if(v!==undefined)out[k]=v;});
        return out;
      }
      conflicts.push(path || "arrangement");
      return copy(l);
    }
    return {value:visit(base,local,remote,""), conflicts:conflicts};
  }
  var opening                              = null;
  function db()                       {
    if (!opening) opening = new Promise(function(resolve,reject){
      if (!window.indexedDB) { reject(new Error("无法保存恢复草稿：IndexedDB 不可用")); return; }
      var req=window.indexedDB.open("MelodyMate_ArrangementDrafts",1);
      req.onupgradeneeded=function(){req.result.createObjectStore("drafts");};
      req.onsuccess=function(){resolve(req.result);}; req.onerror=function(){opening=null;reject(req.error);};
    });
    return opening;
  }
  function draft(method        , project        , value      )               {
    return db().then(function(database){return new Promise(function(resolve,reject){
      var tx=database.transaction("drafts",method==="get"?"readonly":"readwrite"), store=tx.objectStore("drafts");
      var req=method==="get"?store.get(project):method==="put"?store.put(copy(value),project):store.delete(project);
      tx.oncomplete=function(){resolve(req.result);}; tx.onerror=function(){reject(tx.error);}; tx.onabort=function(){reject(tx.error || new Error("恢复草稿保存失败"));};
    });});
  }
  var cancelConflict                   =null;
  function chooseConflict(conflicts          )                       {
    if(cancelConflict)cancelConflict();
    return new Promise(function(resolve){
      var previous=document.activeElement               , overlay=document.createElement("div"), modal=document.createElement("div");
      overlay.className="modal-overlay"; overlay.setAttribute("role","dialog"); overlay.setAttribute("aria-modal","true"); overlay.setAttribute("aria-label","处理保存冲突"); modal.className="modal";
      var title=document.createElement("h3");title.className="modal-title";title.textContent="处理保存冲突";
      var text=document.createElement("p");text.className="confirm-text";text.textContent="发现 "+conflicts.length+" 处同时修改。本地草稿与服务端内容均已保留，自动保存已暂停。请选择冲突部分保留哪一版；不冲突的修改会合并。";
      var actions=document.createElement("div");actions.className="modal-actions";
      var buttons                     =[];
      function finish(choice             ){document.removeEventListener("keydown",onKey,true);overlay.remove();cancelConflict=null;try{previous.focus();}catch(e){}resolve(choice);}
      [["local","保留本地"],["remote","保留服务端"],["later","稍后处理"]].forEach(function(pair){var b=document.createElement("button");b.type="button";b.className="btn btn-secondary";b.textContent=pair[1];b.onclick=function(){finish(pair[0]==="later"?null:pair[0]);};buttons.push(b);actions.appendChild(b);});
      function onKey(e               ){if(e.key==="Escape"){e.preventDefault();e.stopImmediatePropagation();finish(null);}if(e.key==="Tab"){e.preventDefault();e.stopImmediatePropagation();var index=buttons.indexOf(document.activeElement                     );buttons[(index+(e.shiftKey?2:1)+3)%3].focus();}}
      document.addEventListener("keydown",onKey,true);
      modal.append(title,text,actions);overlay.appendChild(modal);document.body.appendChild(overlay);buttons[2].focus();cancelConflict=function(){finish(null);};
    });
  }
  window.ArrangementSync={copy:copy,equal:equal,merge:merge,draft:draft,chooseConflict:chooseConflict,cancelConflict:function(){if(cancelConflict)cancelConflict();}};
})(window);
