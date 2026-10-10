/* 启动令牌引导（authfetch.js）
   必须作为页面最早同步加载的脚本：后端对 /api/ 请求校验 X-MM-Token，
   令牌经 /api/bootstrap.js 下发（响应校验 Origin，跨站页面取不到）。

   为什么在 window.fetch 上做：全站 7 个文件共 13 处直接调用 fetch，
   逐点改既易漏又难维护；这里统一包裹，同源 /api/ 请求自动带头，
   外域请求与静态资源请求不受影响。

   顺序上不可后移：任何先于本脚本运行的代码都可能抢先在令牌写入前
   劫持 fetch，从而截获令牌。 */
(function () {
  "use strict";

  var TOKEN_PATH = "/api/bootstrap.js";
  var HEADER = "X-MM-Token";

  /* 后端未启用令牌时（随机源失败等）下发空串：此时不带头，行为与旧版一致 */
  var token = "";

  /* 同步取令牌：async=false 的 XHR 只在 document 解析期可用，
     本脚本是解析期同步脚本，因此这里能立刻拿到（fetch 是异步的，
     包裹必须在此之前完成） */
  function loadToken() {
    try {
      token = sessionStorage.getItem("mm_token") || "";
      var grant=new URLSearchParams(window.location.hash.slice(1)).get("mm_launch");
      var xhr = new XMLHttpRequest();
      xhr.open("GET", TOKEN_PATH, false);
      if(token)xhr.setRequestHeader(HEADER,token);
      if(grant)xhr.setRequestHeader("X-MM-Launch",grant);
      xhr.send(null);
      if (xhr.status >= 200 && xhr.status < 300) {
        var data = JSON.parse(xhr.responseText || "{}");
        if (data && typeof data.token === "string") {token = data.token;sessionStorage.setItem("mm_token",token);}
      }
      if(grant)history.replaceState(null,"",window.location.pathname+window.location.search);
    } catch (e) {
      /* 取不到令牌：不阻断页面（后端可能未启用校验），请求照发 */
      token = "";
    }
  }

  function isSameOriginApi(rawUrl: string): boolean {
    try {
      var u = new URL(rawUrl, window.location.href);
      return u.origin === window.location.origin && u.pathname.indexOf("/api/") === 0;
    } catch (e) {
      return false;
    }
  }

  loadToken();

  var original = window.fetch;
  if (typeof original !== "function") return;
  var nativeFetch: typeof window.fetch = original.bind(window);

  window.fetch = function (input: any, init?: any): Promise<Response> {
    var url = typeof input === "string" ? input : (input && input.url) || "";
    if (token && isSameOriginApi(url)) {
      init = init || {};
      var headers: Headers;
      if (init.headers) {
        headers = new Headers(init.headers);
      } else if (input && typeof input === "object" && input.headers) {
        headers = new Headers(input.headers);
      } else {
        headers = new Headers();
      }
      if (!headers.has(HEADER)) headers.set(HEADER, token);
      init.headers = headers;
    }
    return nativeFetch(input, init);
  };

  /* navigator.sendBeacon 同样带不了自定义头（页面卸载时的兜底保存用它），
     会让"关页保存草稿/编排"直接 401。这里接管它：改用 fetch + keepalive
     发同一份请求，从而带上令牌；Beacon 的 Content-Type 仍按 Blob 类型
     推导，后端解析逻辑不变。 */
  if (typeof navigator !== "undefined" && typeof navigator.sendBeacon === "function") {
    var nativeBeacon = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = function (url: string, data?: any): boolean {
      if (!token || !isSameOriginApi(url)) return nativeBeacon(url, data);
      try {
        var init: any = { method: "POST", body: data, keepalive: true };
        if (data && typeof data.type === "string" && data.type) {
          init.headers = { "Content-Type": data.type };
        }
        window.fetch(url, init);
        return true;
      } catch (e) {
        return nativeBeacon(url, data);
      }
    };
  }
})();
