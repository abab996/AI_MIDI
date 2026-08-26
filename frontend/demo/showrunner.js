/* ═══════════════════════════════════════════════════════════════════
 * AI_MIDI-go 宣传视频 · 演示导播引擎（Showrunner）
 * ---------------------------------------------------------------
 * 仅当 URL 带 ?demo=1 时激活；普通用户访问零影响。
 *
 * 原理：
 *  - 真实产品原样运行（真实后端 + 真实前端），本脚本只做三件事：
 *    1) 拦截 /api/chat 的 SSE，按预写剧本确定性回放（消除大模型随机性）
 *    2) 用合成鼠标/键盘事件驱动真实 UI 代码路径（拟真打字/画笔/拖拽）
 *    3) 提供分镜触发接口：数字键 1-6 触发各镜头，便于逐镜录制
 *
 * 按键表：
 *   0  一键准备（建工程/传素材/刷新）      H  显示·隐藏导播角标
 *   1  进入演示工程                        2  镜头02 对话生成和弦
 *   3  镜头03 卷帘展开+画笔+调式高亮       4  镜头04 扫弦/切片/粒子擦除
 *   5  镜头05 幽灵音符+键盘即兴            6  镜头06 发送聊天+全奏编曲
 * ═══════════════════════════════════════════════════════════════════ */
(function (window, document) {
  "use strict";
  if (!/[?&]demo=1/.test(location.search)) return;

  const S = window.DEMO_SCRIPT;
  const PR = () => window.PianoRoll;
  const Demo = (window.__DEMO__ = {
    projectId: null,
    busy: false,
    hudHidden: false,
    replayQueue: null,
  });

  /* ═════════════ 基础工具 ═════════════ */
  /* Worker 计时器：页面处于后台时 setTimeout 会被强节流（最小 1s），
     而 Worker 内定时器不受影响——编排水下必须用它驱动 */
  const timerWorker = (() => {
    try {
      const src = "self.onmessage=function(e){setTimeout(function(){self.postMessage({token:e.data.token})},e.data.ms)};";
      const url = URL.createObjectURL(new Blob([src], { type: "application/javascript" }));
      return new Worker(url);
    } catch (_) { return null; }
  })();
  let sleepToken = 0;

  function sleep(ms) {
    if (ms <= 30 || !timerWorker) return new Promise((r) => setTimeout(r, ms));
    return new Promise((resolve) => {
      const token = ++sleepToken;
      const h = (ev) => {
        if (ev.data && ev.data.token === token) {
          timerWorker.removeEventListener("message", h);
          resolve();
        }
      };
      timerWorker.addEventListener("message", h);
      timerWorker.postMessage({ token, ms });
    });
  }

  function log(...a) {
    console.log("[DEMO]", ...a);
    hudStatus(a.join(" "));
  }

  function hudStatus(t) {
    if (!t) return;
    hud.innerHTML = "<b style='color:#ff8c42'>● CUE</b> " + t;
  }

  /* ---- 导播角标 + 分镜按钮坞 ---- */
  const hud = document.createElement("div");
  hud.id = "demoHud";
  hud.style.cssText =
    "position:fixed;left:12px;bottom:64px;z-index:99999;font:11px/1.5 Consolas,monospace;" +
    "color:#e0f7fc;background:rgba(10,20,36,.88);border:1px solid rgba(0,240,255,.4);" +
    "padding:8px 10px;border-radius:3px;pointer-events:none;max-width:420px";
  hud.textContent = "DEMO READY";
  const dock = document.createElement("div");
  dock.id = "demoDock";
  dock.style.cssText =
    "position:fixed;left:12px;bottom:12px;z-index:100000;display:flex;gap:6px;";
  document.addEventListener("DOMContentLoaded", () => {
    document.body.appendChild(hud);
    document.body.appendChild(dock);
    buildDock();
  });
  if (document.body) { document.body.appendChild(hud); document.body.appendChild(dock); buildDock(); }

  function buildDock() {
    const defs = [
      ["d0", "0 准备"], ["d1", "1 进工程"], ["d2", "2 对话"],
      ["d3", "3 卷帘"], ["d4", "4 工具"], ["d5", "5 幽灵"], ["d6", "6 全奏"],
    ];
    dock.innerHTML = "";
    defs.forEach(([id, label]) => {
      const b = document.createElement("button");
      b.id = id;
      b.type = "button";
      b.textContent = label;
      b.style.cssText =
        "font:bold 11px Consolas,monospace;color:#e0f7fc;background:rgba(10,20,36,.92);" +
        "border:1px solid rgba(0,240,255,.45);padding:5px 9px;border-radius:2px;cursor:pointer";
      b.addEventListener("click", () => runShot(id.slice(1)));
      dock.appendChild(b);
    });
  }

  function runShot(n) {
    if (Demo.busy) { log("上一镜仍在执行…"); return; }
    Demo.busy = true;
    log("▶ " + (SHOTS["Digit" + n] || {}).label);
    const fn = (SHOTS["Digit" + n] || {}).fn;
    Promise.resolve().then(fn).catch((err) => {
      console.error(err);
      log("✗ " + err.message);
    }).finally(() => { Demo.busy = false; });
  }

  window.addEventListener("keydown", (e) => {
    if (e.key === "h" || e.key === "H") {
      if (e.ctrlKey || e.altKey || e.metaKey) return;
      const tag = (e.target && e.target.tagName) || "";
      if (tag === "INPUT" || tag === "TEXTAREA") return;
      Demo.hudHidden = !Demo.hudHidden;
      hud.style.display = Demo.hudHidden ? "none" : "";
      dock.style.display = Demo.hudHidden ? "none" : "flex";
    }
  });

  /* ═════════════ SSE 确定性回放（拦截 /api/chat）═════════════ */
  const _fetch = window.fetch.bind(window);
  window.fetch = function (input, init) {
    const url = typeof input === "string" ? input : (input && input.url) || "";
    if (Demo.replayQueue && url.indexOf("/api/chat") !== -1) {
      /* 提取真实请求体里的 message：本地气泡渲染的就是它，
         剧本回显同文，避免整表重渲染时用户消息"闪变" */
      let chatMessage = "";
      try {
        if (init && init.body) chatMessage = JSON.parse(init.body).message || "";
      } catch (_) {}
      const entries = typeof Demo.replayQueue === "function"
        ? Demo.replayQueue(Demo.projectId, chatMessage)
        : Demo.replayQueue;
      Demo.replayQueue = null;
      log("SSE 拦截命中，回放 " + entries.length + " 帧");
      return Promise.resolve(sseReplay(entries));
    }
    return _fetch(input, init);
  };

  function sseReplay(entries) {
    /* Worker 驱动的定时推送：不受页面后台节流影响 */
    const enc = new TextEncoder();
    const t0 = performance.now();
    let closed = false;
    const stream = new ReadableStream({
      start(controller) {
        entries.forEach((e) => {
          const fire = () => {
            if (closed) return;
            try {
              let out = "";
              (e.frames || []).forEach((f) => { out += "data: " + JSON.stringify(f) + "\n\n"; });
              (e.events || []).forEach((ev) => { out += "data: " + JSON.stringify(ev) + "\n\n"; });
              if (out) controller.enqueue(enc.encode(out));
              if (e.done) { closed = true; controller.close(); }
            } catch (err) { console.error("[DEMO] sse enqueue", err); }
          };
          const delay = Math.max(0, e.at - (performance.now() - t0));
          if (delay <= 30 || !timerWorker) setTimeout(fire, delay);
          else {
            const token = "sse" + Math.random();
            const h = (ev) => {
              if (ev.data && ev.data.token === token) {
                timerWorker.removeEventListener("message", h);
                fire();
              }
            };
            timerWorker.addEventListener("message", h);
            timerWorker.postMessage({ token, ms: delay });
          }
        });
      },
      cancel() { closed = true; },
    });
    return new Response(stream, {
      status: 200,
      headers: { "Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache" },
    });
  }

  function armGen(which) {
    /* gen3 为函数式剧本：拦截 /api/chat 时注入真实 message 再生成帧，
       保证回放的用户消息与本地气泡逐字一致（无闪变） */
    Demo.replayQueue = which === 3 ? S.gen3
      : which === 2 ? S.gen2(Demo.projectId)
      : S.gen1(Demo.projectId);
    log("已装载生成剧本 #" + which);
  }

  /* ═════════════ 虚拟鼠标指针（录制视觉引导）═════════════
   * 跟随所有合成鼠标事件；点击时涟漪扩散。cursor=0 可关闭。 */
  const cursorOn = new URLSearchParams(location.search).get("cursor") !== "0";
  let curX = innerWidth * 0.62, curY = innerHeight * 0.55;
  let tgtX = curX, tgtY = curY;
  let curEl = null, rippleEl = null;

  if (cursorOn) {
    const mountCursor = () => {
      curEl = document.createElement("div");
      curEl.id = "demoCursor";
      curEl.style.cssText =
        "position:fixed;left:0;top:0;z-index:200000;pointer-events:none;will-change:transform;" +
        "width:26px;height:26px;margin:-3px 0 0 -3px;filter:drop-shadow(0 2px 5px rgba(0,0,0,.45));";
      curEl.innerHTML =
        '<svg width="26" height="26" viewBox="0 0 24 24"><path d="M6.2 2.6 L6.2 17.4 L10.3 14.0 L12.8 19.7 Q13.15 20.5 13.95 20.15 L16.35 19.1 Q17.15 18.75 16.75 17.95 L14.15 12.55 L19.3 11.9 Z" fill="#FFFFFF" stroke="#2B2B2B" stroke-width="1.25" stroke-linejoin="round" stroke-linecap="round"/></svg>';
      rippleEl = document.createElement("div");
      rippleEl.id = "demoRipple";
      rippleEl.style.cssText =
        "position:fixed;left:0;top:0;z-index:199999;pointer-events:none;width:14px;height:14px;" +
        "margin:-7px 0 0 -7px;border:2px solid rgba(0,240,255,.9);border-radius:50%;opacity:0;";
      document.body.appendChild(curEl);
      document.body.appendChild(rippleEl);
    };
    if (document.body) mountCursor();
    else document.addEventListener("DOMContentLoaded", mountCursor);
    (function follow() {
      if (curEl) {
        curX += (tgtX - curX) * 0.28;
        curY += (tgtY - curY) * 0.28;
        curEl.style.transform = "translate(" + curX.toFixed(1) + "px," + curY.toFixed(1) + "px)";
      }
      requestAnimationFrame(follow);
    })();
  }

  function cursorMove(x, y) {
    if (!cursorOn) return;
    tgtX = x; tgtY = y;
    if (!curEl) { curX = x; curY = y; }
  }

  function cursorClick(x, y) {
    if (!rippleEl) return;
    rippleEl.style.left = x + "px";
    rippleEl.style.top = y + "px";
    rippleEl.style.transition = "none";
    rippleEl.style.opacity = "0.95";
    rippleEl.style.transform = "scale(0.4)";
    void rippleEl.offsetWidth;
    rippleEl.style.transition = "transform .5s cubic-bezier(.2,.8,.3,1), opacity .5s ease-out";
    rippleEl.style.transform = "scale(3.2)";
    rippleEl.style.opacity = "0";
  }

  /* ═════════════ 拟真输入驱动 ═════════════ */
  function fire(el, type, props) {
    const ev = new MouseEvent(type, Object.assign({
      bubbles: true, cancelable: true, view: window,
      button: 0, buttons: 1,
    }, props));
    if (props && typeof props.clientX === "number") {
      cursorMove(props.clientX, props.clientY);
      if (type === "mousedown") cursorClick(props.clientX, props.clientY);
    }
    el.dispatchEvent(ev);
    return ev;
  }

  /* 贝塞尔轨迹鼠标移动：duration 内均匀采样派发 mousemove */
  async function moveMouse(x0, y0, x1, y1, duration) {
    const cx = (x0 + x1) / 2 + (y1 - y0) * 0.18;
    const cy = (y0 + y1) / 2 + (x0 - x1) * 0.18;
    const steps = Math.max(6, Math.round(duration / 14));
    for (let i = 1; i <= steps; i++) {
      const t = i / steps;
      const u = 1 - t;
      const x = u * u * x0 + 2 * u * t * cx + t * t * x1;
      const y = u * u * y0 + 2 * u * t * cy + t * t * y1;
      fire(document.documentElement, "mousemove", { clientX: x, clientY: y });
      await sleep(duration / steps);
    }
    fire(document.documentElement, "mousemove", { clientX: x1, clientY: y1 });
  }

  /* 等待虚拟光标真正到达目标点（消除"涟漪已出、指针还在路上"的割裂感） */
  function cursorArrive(x, y, timeout = 500) {
    tgtX = x; tgtY = y;
    return new Promise((res) => {
      const t0 = performance.now();
      (function wait() {
        if (Math.hypot(curX - x, curY - y) < 5 || performance.now() - t0 > timeout) {
          curX = x; curY = y;   /* 超时兜底：直接吸附 */
          res();
        } else setTimeout(wait, 16);
      })();
    });
  }

  async function clickAt(x, y, opts) {
    const el = document.elementFromPoint(x, y) || document.documentElement;
    await cursorArrive(x, y);
    fire(el, "mousemove", { clientX: x, clientY: y });
    await sleep(60);
    fire(el, "mousedown", Object.assign({ clientX: x, clientY: y }, opts));
    fire(el, "mouseup", Object.assign({ clientX: x, clientY: y, buttons: 0 }, opts));
    fire(el, "click", Object.assign({ clientX: x, clientY: y }, opts));
  }

  async function clickEl(el, opts) {
    const r = el.getBoundingClientRect();
    await clickAt(r.left + r.width / 2, r.top + r.height / 2, opts);
  }

  async function dblClickEl(el) {
    /* 不发单击（label 包裹 checkbox 会被打勾），直接双击序列 */
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    fire(el, "mousemove", { clientX: x, clientY: y });
    fire(el, "mousedown", { clientX: x, clientY: y });
    fire(el, "mouseup", { clientX: x, clientY: y, buttons: 0 });
    fire(el, "dblclick", { clientX: x, clientY: y });
  }

  function keyTap(key, code, extra) {
    const init = Object.assign({ key, code, bubbles: true, cancelable: true }, extra || {});
    delete init._onWindow;
    const target = document.activeElement || document.body;
    const dn = new KeyboardEvent("keydown", init);
    try { Object.defineProperty(dn, "keyCode", { get: () => init.keyCode || 0 }); } catch (_) {}
    target.dispatchEvent(dn);
    setTimeout(() => target.dispatchEvent(new KeyboardEvent("keyup", init)), 70);
  }

  /* 卷帘快捷键依赖 isFocused 且活动元素不能是输入框 */
  function ensurePRFocus() {
    const ae = document.activeElement;
    if (ae && (ae.tagName === "INPUT" || ae.tagName === "TEXTAREA")) ae.blur();
    if (PR() && PR().setFocus) PR().setFocus(true);
  }

  /* ═════════════ 页面级摄像机（真实页面的电影运镜）═════════════
   * 对 body 施加 scale/translate + 运动模糊，M3 emphasized 缓动。
   * 注意：3D 姿态仅作瞬态（Chromium 对 3D 变换不重栅格化会发糊），
   * 运镜结束解除合成层强制按终态重栅格化。 */
  let camStyled = false;
  function cam(to, dur = 700, opts = {}) {
    if (!camStyled) {
      const s = document.createElement("style");
      s.textContent = "html{perspective:1500px;} body{transform-origin:50% 55%;will-change:transform,filter;}";
      document.head.appendChild(s);
      camStyled = true;
    }
    if (opts.blur) {
      document.body.style.transition = "filter .14s ease";
      document.body.style.filter = `blur(${opts.blur}px)`;
    }
    document.body.style.transition =
      `transform ${dur}ms ${opts.ease || "cubic-bezier(.05,.7,.1,1)"}` +
      (opts.blur ? `, filter ${Math.round(dur * 0.85)}ms ease ${Math.round(dur * 0.15)}ms` : "");
    document.body.style.transform =
      `translate(${to.x || 0}px, ${to.y || 0}px) scale(${to.scale ?? 1})` +
      ` rotateX(${to.rx || 0}deg) rotateY(${to.ry || 0}deg)`;
    clearTimeout(cam._t);
    cam._t = setTimeout(() => {
      document.body.style.filter = "none";
      document.body.style.willChange = "auto";
      void document.body.offsetWidth;
    }, dur + 60);
  }

  /* 拟真打字：随机 45-95ms 字距，逗号句尾略作停顿 */
  async function typeInto(el, text) {
    const r = el.getBoundingClientRect();
    if (r.width) cursorMove(r.left + Math.min(r.width * 0.3, 320), r.top + r.height / 2);
    el.focus();
    el.value = "";
    fire(el, "input", {});
    for (let i = 0; i < text.length; i++) {
      const ch = text[i];
      el.value += ch;
      el.dispatchEvent(new Event("input", { bubbles: true }));
      let d = 45 + Math.random() * 50;
      if ("，。！？、".includes(ch)) d += 160;
      await sleep(d);
    }
    await sleep(350);
  }

  function pressEnter(input) {
    const ev = new KeyboardEvent("keydown", {
      key: "Enter", code: "Enter", keyCode: 13, which: 13,
      bubbles: true, cancelable: true,
    });
    Object.defineProperty(ev, "keyCode", { get: () => 13 });
    input.dispatchEvent(ev);
  }

  /* ═════════════ 钢琴卷帘坐标映射 ═════════════ */
  function gridPoint(beat, pitch) {
    const p = PR();
    const rect = p.gridCanvas.getBoundingClientRect();
    const mx = rect.left + (beat - p.scrollX) * p.pixelsPerBeat;
    const my = rect.top + p.rulerHeight + (127 - pitch) * p.noteRowHeight - p.scrollY + p.noteRowHeight / 2;
    return { x: Math.round(mx), y: Math.round(my), rect };
  }

  async function wheelZoom(ticks) {
    const p = PR();
    const rect = p.gridCanvas.getBoundingClientRect();
    const x = rect.left + rect.width * 0.45;
    const y = rect.top + rect.height * 0.5;
    for (let i = 0; i < ticks; i++) {
      const ev = new WheelEvent("wheel", {
        bubbles: true, cancelable: true, deltaY: 120, ctrlKey: true, clientX: x, clientY: y,
      });
      p.gridCanvas.dispatchEvent(ev);
      await sleep(70);
    }
  }

  /* 在卷帘上从 (b0,p0) 拖到 (b1,p1)：mousedown→move 路径→mouseup */
  async function dragOnGrid(b0, p0, b1, p1, duration, mods) {
    const a = gridPoint(b0, p0);
    const b = gridPoint(b1, p1);
    const g = PR().gridCanvas;
    await cursorArrive(a.x, a.y, 420);
    fire(g, "mousedown", { clientX: a.x, clientY: a.y, ctrlKey: !!(mods && mods.ctrl) });
    await sleep(80);
    const steps = Math.max(6, Math.round((duration || 260) / 26));
    for (let i = 1; i <= steps; i++) {
      const t = i / steps;
      fire(document.documentElement, "mousemove", {
        clientX: a.x + (b.x - a.x) * t,
        clientY: a.y + (b.y - a.y) * t,
        ctrlKey: !!(mods && mods.ctrl),
      });
      await sleep((duration || 260) / steps);
    }
    fire(document.documentElement, "mouseup", {
      clientX: b.x, clientY: b.y, buttons: 0, ctrlKey: !!(mods && mods.ctrl),
    });
  }

  /* 取景：把视图平移到指定起始拍（等价于真实拖拽平移的结果） */
  function frameView(startBeat, centerPitch) {
    const p = PR();
    p.scrollX = Math.max(0, startBeat);
    if (centerPitch) {
      p.scrollY = Math.max(0, Math.min(128 * p.noteRowHeight,
        (127 - centerPitch) * p.noteRowHeight - (p.gridCanvas.clientHeight - p.rulerHeight) / 2));
    }
    p.render();
  }

  /* 点击标尺移动播放头 */
  async function setPlayhead(beat) {
    const p = PR();
    const rect = p.gridCanvas.getBoundingClientRect();
    const x = rect.left + (beat - p.scrollX) * p.pixelsPerBeat;
    const y = rect.top + 8;
    await clickAt(x, y);
  }

  function setTool(tool) {
    PR().setTool(tool);
    log("工具 → " + tool);
  }

  /* 自定义下拉：点开按钮后选含指定文本的选项 */
  async function pickDropdown(btnId, menuId, contains) {
    const btn = document.getElementById(btnId);
    const menu = document.getElementById(menuId);
    if (!btn || !menu) throw new Error("dropdown missing: " + btnId);
    await clickEl(btn);
    await sleep(320);
    const items = menu.querySelectorAll(".select-option, [data-value]");
    for (const it of items) {
      if ((it.textContent || "").indexOf(contains) !== -1) {
        await clickEl(it);
        await sleep(220);
        return true;
      }
    }
    clickEl(btn); /* 收起菜单兜底 */
    return false;
  }

  /* 文件树双击打开 */
  async function openFileInTree(fileName) {
    const items = document.querySelectorAll("#fileList .file-item");
    for (const it of items) {
      const nm = it.querySelector(".file-name");
      if (nm && nm.textContent.trim() === fileName) {
        await dblClickEl(nm);
        await sleep(1100); /* 抽屉升起 + 解析 */
        return true;
      }
    }
    log("✗ 文件树中未找到 " + fileName);
    return false;
  }

  /* ═════════════ 准备流程（键 0）═════════════ */
  const PID_KEY = "ai-midi-demo-project";
  Demo.projectId = (() => {
    try { return localStorage.getItem(PID_KEY) || null; } catch (_) { return null; }
  })();
  function rememberProject(id) {
    Demo.projectId = id;
    try { localStorage.setItem(PID_KEY, id); } catch (_) {}
  }

  async function ensureAssets() {
    /* 1) 找到或创建演示工程 */
    let projects = await (await _fetch("/api/projects")).json();
    let hit = projects.find((p) => p.name === S.projectName);
    if (!hit) {
      await _fetch("/api/projects", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: S.projectName }),
      });
      projects = await (await _fetch("/api/projects")).json();
      hit = projects.find((p) => p.name === S.projectName);
      if (!hit) throw new Error("工程创建失败");
    }
    rememberProject(hit.id);
    log("工程就绪 id=" + hit.id);

    /* 2) 上传缺失的 MIDI 素材（走真实上传接口） */
    const payload = await (await _fetch("/api/projects/" + hit.id)).json();
    const have = new Set((payload.midi_files || []).map((f) => f.name.split("/").pop()));
    const wanted = [S.chordFileName, S.fullFileName, S.choreo.shot5.ghostFile,
      "Demo_Melody.mid", "Demo_Drums.mid", "Demo_BassLine.mid"];
    let uploaded = false;
    for (const fname of wanted) {
      if (have.has(fname)) continue;
      uploaded = true;
      const bytes = await (await _fetch("/demo/assets/" + fname)).arrayBuffer();
      const fd = new FormData();
      fd.append("files", new File([bytes], fname, { type: "audio/midi" }));
      const resp = await _fetch("/api/projects/" + hit.id + "/files", { method: "POST", body: fd });
      log((resp.ok ? "已上传 " : "✗ 上传失败 ") + fname);
    }
    if (!uploaded) { log("素材早已就绪"); return; }

    /* 3) 有新上传才刷新页面让文件树生效（保留 demo 参数） */
    log("2 秒后刷新…");
    await sleep(2000);
    location.reload();
  }

  async function enterProject() {
    /* 刷新后内存丢失时从 localStorage 恢复；再按名称兜底 */
    let card = document.querySelector('.proj-card[data-id="' + Demo.projectId + '"]');
    if (!card) {
      const cards = document.querySelectorAll(".proj-card");
      for (const c of cards) {
        if ((c.textContent || "").indexOf(S.projectName) !== -1) { card = c; break; }
      }
    }
    if (!card) { log("✗ 档案卡未找到——请先按键 0 准备"); return; }
    rememberProject(card.dataset.id);
    /* 卡片上的「打开」按钮是最稳的入口 */
    const openBtn = card.querySelector(".btn-open, button");
    if (openBtn && openBtn.textContent.indexOf("打开") !== -1) {
      await clickEl(openBtn);
    } else {
      await clickEl(card);
    }
    await sleep(1600);
    log("已进入工作台");
  }

  /* ═════════════ 镜头 02：对话生成 ═════════════ */
  async function shot2() {
    const input = document.getElementById("msgInput");
    await typeInto(input, S.prompt1);
    armGen(1);
    await sleep(250);
    pressEnter(input);
    await sleep(6200);
    log("镜头02 完成");
  }

  /* ═════════════ 镜头 03：卷帘 + 画笔 + 调式高亮 ═════════════ */
  async function shot3() {
    const c = S.choreo.shot3;
    await openFileInTree(S.chordFileName);
    await sleep(400);
    ensurePRFocus();

    /* BPM 85 */
    const bpmInput = document.getElementById("prBpmInput");
    bpmInput.value = c.bpm;
    bpmInput.dispatchEvent(new Event("change", { bubbles: true }));

    /* 音色 → SF2 温暖钢琴 */
    await pickDropdown("prSoundDropdown", "prSoundMenu", "温暖钢琴");
    await sleep(200);

    /* 音阶高亮：多利亚，主音 D */
    await pickDropdown("prScaleDropdown", "prScaleMenu", "多利亚");
    const rootSel = await trySetScaleRoot(c.rootPitch);
    if (!rootSel) { PR().selectedRootPitch = c.rootPitch; PR().render(); }

    /* 手绘旋律（画笔默认工具）*/
    for (const nd of c.drawNotes) {
      await dragOnGrid(nd.beat, nd.pitch, nd.beat + nd.dur, nd.pitch, 170);
      await sleep(130);
    }
    log("旋律绘制完成");

    /* 取景：回看和弦尾部 + 手绘旋律区域 */
    frameView(52, 74);
    await sleep(500);

    /* 试听一小段 */
    await setPlayhead(c.playheadBeat);
    await sleep(500);
    ensurePRFocus();
    keyTap(" ", "Space", { keyCode: 32 });
    await sleep(c.playMs);
    keyTap(" ", "Space", { keyCode: 32 });
    log("镜头03 完成");
  }

  async function trySetScaleRoot(rootPitch) {
    /* 存在主音下拉则真实点击选择 D；否则由调用方直设状态 */
    const NAMES = { 60: "C", 62: "D", 64: "E", 65: "F", 67: "G", 69: "A", 71: "B" };
    const btn = document.getElementById("prRootDropdown") || document.getElementById("prRootBtn");
    if (!btn) return false;
    const menuId = btn.id.replace("Btn", "Menu").replace("Dropdown", "Menu");
    return pickDropdown(btn.id, menuId, NAMES[rootPitch] || "D");
  }

  /* ═════════════ 镜头 04：扫弦 / 切片 / 粒子擦除 ═════════════ */
  async function shot4() {
    const c = S.choreo.shot4;
    await ensureChordsOpen();
    if (!PR().getActiveTab()) { log("卷帘未打开"); return; }
    ensurePRFocus();

    await wheelZoom(c.zoomOutWheelTicks);
    await sleep(300);
    frameView(0, 74);   /* 回到曲首：扫弦/切片/擦除目标都在前 20 拍 */
    await sleep(300);

    /* 框选第 3 小节和弦区（Ctrl+拖拽） */
    await dragOnGrid(c.selectBox.fromBeat, c.selectBox.fromPitch,
      c.selectBox.toBeat, c.selectBox.toPitch, 420, { ctrl: true });
    await sleep(250);

    /* Alt+S 扫弦弹窗 */
    keyTap("S", "KeyS", { altKey: true, keyCode: 83 });
    await sleep(950);
    const stTime = document.getElementById("strumTime");
    if (!stTime) { PR().openStrumModal(); await sleep(950); }
    const stRamp = document.getElementById("strumVelRamp");
    /* 滑块动态拖动感 */
    for (let v = parseInt(stTime.value, 10); v < c.strum.time; v += 2) {
      stTime.value = v;
      stTime.dispatchEvent(new Event("input", { bubbles: true }));
      await sleep(85);
    }
    stTime.value = c.strum.time;
    stTime.dispatchEvent(new Event("input", { bubbles: true }));
    stRamp.value = c.strum.velRamp;
    stRamp.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(700);
    document.getElementById("strumOk").click();
    await sleep(900);
    log("扫弦完成");

    /* 试听扫弦效果（播放头放在框选和弦处） */
    await setPlayhead(8);
    await sleep(400);
    ensurePRFocus();
    keyTap(" ", "Space", { keyCode: 32 });
    await sleep(3400);
    keyTap(" ", "Space", { keyCode: 32 });
    await sleep(400);

    /* 切片刀：竖切第 3 小节长和弦 */
    setTool("slice");
    await sleep(500);
    await dragOnGrid(c.sliceBeat, 96, c.sliceBeat, 52, 480);
    await sleep(700);
    log("切片完成");

    /* 橡皮擦粒子扫除 */
    setTool("erase");
    await sleep(600);
    const pts = c.eraseSweep.map((p) => gridPoint(p.beat, p.pitch));
    const g = PR().gridCanvas;
    fire(g, "mousedown", { clientX: pts[0].x, clientY: pts[0].y });
    for (let k = 0; k < pts.length; k++) {
      const mid = k === 0 ? pts[0] : {
        x: (pts[k - 1].x + pts[k].x) / 2, y: (pts[k - 1].y + pts[k].y) / 2,
      };
      fire(document.documentElement, "mousemove", { clientX: mid.x, clientY: mid.y });
      await sleep(90);
      fire(document.documentElement, "mousemove", { clientX: pts[k].x, clientY: pts[k].y });
      await sleep(170);
    }
    fire(document.documentElement, "mouseup", { clientX: pts[pts.length - 1].x, clientY: pts[pts.length - 1].y, buttons: 0 });
    await sleep(1300);
    setTool("draw");
    log("镜头04 完成");
  }

  /* ═════════════ 镜头 05：幽灵音符 + 键盘即兴 ═════════════ */
  async function shot5() {
    const c = S.choreo.shot5;
    await ensureChordsOpen();
    if (!PR().drawerEl || PR().drawerEl.classList.contains("closed")) { log("卷帘未打开"); return; }

    /* 打开贝斯参考轨标签页 */
    await openFileInTree(c.ghostFile);
    await sleep(300);
    /* 切回和弦标签页 */
    const tabs = document.querySelectorAll(".pr-tab");
    for (const tb of tabs) {
      if (tb.textContent.indexOf(S.chordFileName.replace(".mid", "")) !== -1 ||
          tb.textContent.indexOf("Chords") !== -1) {
        await clickEl(tb.querySelector(".pr-tab-name"));
        break;
      }
    }
    await sleep(350);

    /* 开启幽灵参考轨 */
    const okGhost = await pickDropdown("prGhostDropdown", "prGhostMenu", c.ghostFile.replace(".mid", ""));
    if (!okGhost) log("⚠ 幽灵轨选项未命中（检查标签页名）");

    /* 打字键盘模式 */
    const typingBtn = document.getElementById("prTypingMidiBtn");
    if (typingBtn && !typingBtn.classList.contains("active")) await clickEl(typingBtn);
    await sleep(300);

    /* 即兴弹奏（FL 键位，序列来自剧本数据）*/
    for (const [code, ms] of c.jam) {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: code.replace("Key", ""), code, bubbles: true }));
      await sleep(ms);
      window.dispatchEvent(new KeyboardEvent("keyup", { key: code.replace("Key", ""), code, bubbles: true }));
      await sleep(c.jamPauseMs);
    }
    log("镜头05 完成");
  }

  /* ═════════════ 镜头 06：发送聊天 → 全奏 ═════════════ */
  async function shot6() {
    await ensureChordsOpen();
    /* 发送到聊天 */
    const sendBtn = document.getElementById("prSendToChatBtn");
    if (sendBtn) await clickEl(sendBtn);
    await sleep(900);

    /* 关闭抽屉回工作台（点真实关闭按钮） */
    const closeBtn = document.getElementById("prCloseBtn");
    if (closeBtn) await clickEl(closeBtn);
    else PR().close();
    await sleep(800);
    if (PR().drawerEl && !PR().drawerEl.classList.contains("closed")) { PR().close(); await sleep(600); }

    /* 输入追问并生成（替换注入的长音符串，保持画面干净） */
    const input = document.getElementById("msgInput");
    await typeInto(input, S.prompt2);
    armGen(2);
    await sleep(250);
    pressEnter(input);
    await sleep(7200);

    /* 打开全奏文件试听高潮段 */
    await openFileInTree(S.fullFileName);
    await sleep(400);
    const bpmInput = document.getElementById("prBpmInput");
    bpmInput.value = 85;
    bpmInput.dispatchEvent(new Event("change", { bubbles: true }));
    await setPlayhead(30);
    await sleep(450);
    keyTap(" ", "Space", { keyCode: 32, _onWindow: true });
    await sleep(9500);
    keyTap(" ", "Space", { keyCode: 32, _onWindow: true });
    log("镜头06 完成");
  }

  /* ═════════════ 镜头 07：快速任务（真实弹窗）═════════════ */
  async function shot7() {
    /* 从档案库或工作台顶栏均可打开 */
    const btn = document.getElementById("quickTaskBtn");
    if (!btn) { log("✗ 未找到快速任务按钮"); return; }
    await clickEl(btn);
    await sleep(900);
    cam({ scale: 1.14, y: 30 }, 700);          /* 推近弹窗 */

    /* MIDI 拖入解析：构造 DataTransfer 派发真实 drop 事件 */
    const bytes = await (await _fetch("/demo/assets/" + S.chordFileName)).arrayBuffer();
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], S.chordFileName, { type: "audio/midi" }));
    const dz = document.getElementById("qtDropzone");
    dz.dispatchEvent(new DragEvent("dragover", { bubbles: true, dataTransfer: dt }));
    dz.dispatchEvent(new DragEvent("drop", { bubbles: true, dataTransfer: dt }));
    await sleep(1600);
    const ps = document.getElementById("qtParseStatus");
    log("解析状态: " + (ps ? ps.textContent.trim() : "?"));

    /* 任务 chips：点选「配和弦」（默认已选中，光标仍走一遍） */
    const chordChip = document.querySelector('#qtFuncSelector .fn[data-func="chord"]');
    if (chordChip) { await clickEl(chordChip); await sleep(500); }

    /* 具体要求 + BPM */
    const req = document.getElementById("qtReq");
    if (req) {
      await clickEl(req);
      await sleep(300);
      await typeInto(req, "使用 1-6-4-5 和声进行，节奏舒缓，加一条对位旋律线");
    }
    const bpm = document.getElementById("qtBpm");
    if (bpm) {
      bpm.focus(); bpm.value = "";
      for (const ch of "85") {
        bpm.value += ch;
        bpm.dispatchEvent(new Event("input", { bubbles: true }));
        await sleep(140);
      }
    }
    await sleep(400);

    /* 发起任务（新建工程 → 自动发送 → SSE 拦截回放） */
    armGen(3);
    const submit = document.getElementById("qtSubmit");
    await clickEl(submit);
    cam({ scale: 1 }, 600);
    await sleep(6800);
    log("镜头07 完成");
  }

  /* ═════════════ 镜头 08：编曲窗 ═════════════ */
  async function resolveProject() {
    if (Demo.projectId) return Demo.projectId;
    let projects = await (await _fetch("/api/projects")).json();
    let hit = projects.find((p) => p.name === S.projectName);
    if (!hit) {
      await _fetch("/api/projects", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: S.projectName }),
      });
      projects = await (await _fetch("/api/projects")).json();
      hit = projects.find((p) => p.name === S.projectName);
    }
    if (hit) rememberProject(hit.id);
    return hit ? hit.id : null;
  }

  async function ensureArrangement() {
    const pid = await resolveProject();
    if (!pid) { log("✗ 无法解析演示工程"); return false; }
    /* 编排引用的分轨文件也补传（左栏文件列表完整性） */
    const payload = await (await _fetch("/api/projects/" + pid)).json();
    const have = new Set((payload.midi_files || []).map((f) => f.name.split("/").pop()));
    for (const fname of ["Demo_Melody.mid", "Demo_Drums.mid", "Demo_BassLine.mid"]) {
      if (have.has(fname)) continue;
      const bytes = await (await _fetch("/demo/assets/" + fname)).arrayBuffer();
      const fd = new FormData();
      fd.append("files", new File([bytes], fname, { type: "audio/midi" }));
      await _fetch("/api/projects/" + pid + "/files", { method: "POST", body: fd });
    }
    const preset = await (await _fetch("/demo/assets/preset_arrangement.json")).json();
    const resp = await _fetch("/api/projects/" + pid + "/arrangement", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(preset),
    });
    log(resp.ok ? "编排预置完成" : "✗ 编排写入失败");
    return resp.ok;
  }

  async function shot8() {
    /* 编排必须先于进工程写入（setProject 时才会 GET） */
    const inStudio = document.getElementById("studioView") &&
      !document.getElementById("studioView").hidden;
    if (!inStudio) {
      await ensureArrangement();
      await enterProject();
    } else {
      await ensureArrangement();
      await enterProject();
    }
    await sleep(800);

    /* 打开编曲窗（折叠动画） */
    const toggle = document.getElementById("arrangeToggleBtn");
    await clickEl(toggle);
    await sleep(1500);

    /* 等待编排数据渲染完成（setProject 的 GET 是异步的，避免空时间线入画） */
    for (let i = 0; i < 20; i++) {
      if (document.querySelectorAll(".arr-clip").length >= 4) break;
      await sleep(200);
    }
    log("剪辑块渲染: " + document.querySelectorAll(".arr-clip").length + " 个");

    /* BPM 对齐 85 */
    const bpm = document.getElementById("arrBpmInput");
    if (bpm) {
      bpm.value = 85;
      bpm.dispatchEvent(new Event("change", { bubbles: true }));
    }

    /* 轨道头依次点亮（点击轨名 = 选中轨道） */
    const names = document.querySelectorAll(".arr-track-head .arr-th-name");
    for (const h of names) { await clickEl(h); await sleep(480); }
    if (names.length) await clickEl(names[0]);

    /* 播放前缓推镜头——必须等运镜完全落定再测按钮坐标，
       否则按钮在动画中位移，点击会偏位 */
    cam({ scale: 1.06, x: -10 }, 1100);
    await sleep(1300);

    /* 播放：播放头扫过 + 电平表跳舞 */
    const playBtn = document.getElementById("arrPlayBtn");
    await clickEl(playBtn);
    await sleep(5200);

    /* 推近落定后再点鼓组独奏（纹理切换） */
    cam({ scale: 1.12, y: -8 }, 1200);
    await sleep(1400);
    const heads = document.querySelectorAll(".arr-track-head");
    if (heads.length >= 4) {
      const sBtn = heads[3].querySelector(".arr-th-btn:nth-of-type(2)");
      if (sBtn) await clickEl(sBtn);
      log("鼓组独奏开");
    }
    await sleep(3400);
    /* 收尾：取消独奏并停止 */
    if (heads.length >= 4) {
      const sBtn = heads[3].querySelector(".arr-th-btn:nth-of-type(2)");
      if (sBtn) await clickEl(sBtn);
    }
    await clickEl(document.getElementById("arrStopBtn"));
    cam({ scale: 1 }, 700);
    await sleep(500);
    log("镜头08 完成");
  }

  /* ═════════════ 镜头 09：卷帘简述（强因果演示：点→音符出现，右键→粒子消散）═════════════ */
  async function shot9() {
    await ensureChordsOpen();
    ensurePRFocus();
    setTool("draw");
    frameView(60, 74);          /* 取景到空白区域（17 小节后），动作一目了然 */
    await sleep(900);

    const grid = PR().gridCanvas;
    const clickNote = async (beat, pitch) => {
      const p = gridPoint(beat, pitch);
      await cursorArrive(p.x, p.y, 420);
      fire(grid, "mousedown", { clientX: p.x, clientY: p.y });
      fire(document.documentElement, "mouseup", { clientX: p.x, clientY: p.y, buttons: 0 });
    };
    const rightClickErase = async (beat, pitch) => {
      const p = gridPoint(beat, pitch);
      await cursorArrive(p.x, p.y, 420);
      fire(grid, "mousedown", { clientX: p.x, clientY: p.y, button: 2, buttons: 2 });
      fire(document.documentElement, "mouseup", { clientX: p.x, clientY: p.y, button: 2, buttons: 0 });
    };

    await clickNote(64.0, 74);      /* 点一下 → 音符出现 */
    await sleep(1000);
    await clickNote(65.0, 77);      /* 再点一下 → 第二枚 */
    await sleep(1200);
    await rightClickErase(64.0, 74);/* 右键 → 粒子消散 */
    await sleep(1400);
    log("镜头09 完成");
  }

  /* ═════════════ 分镜注册与触发 ═════════════ */
  const SHOTS = {
    Digit0: { label: "一键准备", fn: ensureAssets },
    Digit1: { label: "进入工程", fn: enterProject },
    Digit2: { label: "镜头02 对话生成", fn: shot2 },
    Digit3: { label: "镜头03 卷帘画笔", fn: shot3 },
    Digit4: { label: "镜头04 扫弦切片擦除", fn: shot4 },
    Digit5: { label: "镜头05 幽灵即兴", fn: shot5 },
    Digit6: { label: "镜头06 全奏闭环", fn: shot6 },
    Digit7: { label: "镜头07 快速任务", fn: shot7 },
    Digit8: { label: "镜头08 编曲窗", fn: shot8 },
    Digit9: { label: "镜头09 卷帘简述", fn: shot9 },
  };

  window.addEventListener("keydown", (e) => {
    const shot = SHOTS[e.code] ||
      (/^([0-6])$/.test(e.key || "") ? SHOTS["Digit" + e.key] : null);
    if (!shot) return;
    const tag = (e.target && e.target.tagName) || "";
    if (tag === "INPUT" || tag === "TEXTAREA") return; /* 输入框中打数字不触发 */
    if (e.ctrlKey || e.altKey || e.metaKey) return;
    e.preventDefault();
    runShot(e.code.replace("Digit", ""));
  });

  /* ═════════════ URL 参数驱动（真实 Chrome 自动录制用）═════════════
   * ?demo=1&auto=1   全自动连播：进工程 → 镜头02..06
   * ?demo=1&shot=N   只跑单镜（N=1..6；0=仅准备）
   */
  const params = new URLSearchParams(location.search);
  const autoMode = params.get("auto") === "1";
  const singleShot = params.get("shot");
  /* clean=1：录制干净版——隐藏按钮坞与角标（H 键仍可切换） */
  if (params.get("clean") === "1") {
    Demo.hudHidden = true;
    const hide = () => { hud.style.display = "none"; dock.style.display = "none"; };
    if (document.body) hide(); else document.addEventListener("DOMContentLoaded", hide);
  }

  async function autoRun() {
    await sleep(1800);
    if (!Demo.projectId) {
      log("✗ 缺少工程——请先访问 ?demo=1&shot=0 完成准备");
      return;
    }
    await enterProject();
    await sleep(600);
    for (const n of ["2", "3", "4", "5", "6"]) {
      log("AUTO ▶ 镜头" + n);
      await SHOTS["Digit" + n].fn();
      await sleep(1200);
    }
    log("AUTO ■ 全部完成");
  }

  /* 单镜独立运行时的前置保障：确保和弦文件已在卷帘打开 */
  async function ensureChordsOpen() {
    if (PR() && PR().getActiveTab && PR().getActiveTab()) return;
    await openFileInTree(S.chordFileName);
    await sleep(600);
  }

  if (singleShot === "0") {
    window.addEventListener("load", () => setTimeout(() => ensureAssets(), 1600));
  } else if (singleShot === "7") {
    /* 快速任务从档案库顶栏即可发起（自建新工程），无需先进入 */
    window.addEventListener("load", () => setTimeout(() => runShot("7"), 1600));
  } else if (singleShot === "8") {
    /* 编曲窗需要编排预置先于进工程写入；新建工程后档案卡未渲染则刷新重入 */
    window.addEventListener("load", () => setTimeout(async () => {
      try {
        await ensureArrangement();
        await sleep(300);
        const card = document.querySelector('.proj-card[data-id="' + Demo.projectId + '"]');
        if (!card) { log("刷新以加载档案卡…"); location.reload(); return; }
        await enterProject();
        await sleep(600);
        runShot("8");
      } catch (err) { log("✗ " + err.message); }
    }, 1600));
  } else if (singleShot && SHOTS["Digit" + singleShot]) {
    /* 单镜独立录制：先进工程（按名称兜底），再跑目标镜头 */
    window.addEventListener("load", () => setTimeout(async () => {
      try {
        if (singleShot !== "1") {
          const inStudio = document.getElementById("studioView") &&
            !document.getElementById("studioView").hidden;
          if (!inStudio) { await enterProject(); await sleep(500); }
        }
        runShot(singleShot);
      } catch (err) { log("✗ " + err.message); }
    }, 1600));
  } else if (autoMode) {
    window.addEventListener("load", () => setTimeout(autoRun, 800));
  }

  console.log("[DEMO] 导播系统就绪 worker=" + (timerWorker ? "ok" : "NULL") +
    " auto=" + autoMode + " shot=" + singleShot);
})(window, document);
