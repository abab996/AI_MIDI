/* 乐器箱（机架 P2）浏览器级验收：CDP 驱动无头 Chrome/Edge。
 *
 * 跑真实页面的真实交互：v1 老工程迁移 → 机架条目渲染 → 分类/搜索/空态 →
 * 音源选择器（键盘 ↑↓/Enter/Esc、预设二级视图）→ 分配落到服务端 →
 * 双主题截图 → prefers-reduced-motion 降级 → 控制台零异常。
 *
 * 用法（需服务在跑，且本机有 Chrome 或 Edge）：
 *   MM_BASE=http://127.0.0.1:7912 node tools/dev/rack_ui_check.mjs
 *   MM_BROWSER="C:\path\to\chrome.exe" ...   # 指定浏览器
 *
 * 截图输出到 output/ui_check/（不入库）。
 */
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { request as httpRequest } from "node:http";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const outDir = path.join(repoRoot, "output", "ui_check");
const base = process.env.MM_BASE || "http://127.0.0.1:7912";

let failures = 0;
function check(name, ok, extra) {
  if (ok) console.log("  ok   " + name);
  else {
    failures++;
    console.log("  FAIL " + name + (extra !== undefined ? "  → " + JSON.stringify(extra).slice(0, 400) : ""));
  }
}

/* ── HTTP 小工具（避免 undici keep-alive 退出噪音） ── */
function httpFetch(url, opts = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const req = httpRequest(
      { hostname: u.hostname, port: u.port, path: u.pathname + u.search,
        method: (opts.method || "GET").toUpperCase(),
        headers: { Connection: "close", ...(opts.headers || {}) } },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          const body = Buffer.concat(chunks).toString("utf8");
          resolve({ ok: res.statusCode >= 200 && res.statusCode < 300, status: res.statusCode,
            text: async () => body, json: async () => JSON.parse(body) });
        });
      }
    );
    req.on("error", reject);
    if (opts.body !== undefined && opts.body !== null) req.write(opts.body);
    req.end();
  });
}
const api = async (method, p, body) => {
  const r = await httpFetch(base + p, {
    method, headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: r.status, json: await r.json().catch(() => ({})) };
};

/* ── 找浏览器 ── */
function findBrowser() {
  if (process.env.MM_BROWSER) return process.env.MM_BROWSER;
  const candidates = [
    path.join(process.env["ProgramFiles"] || "", "Google/Chrome/Application/chrome.exe"),
    path.join(process.env["ProgramFiles(x86)"] || "", "Google/Chrome/Application/chrome.exe"),
    path.join(process.env["LOCALAPPDATA"] || "", "Google/Chrome/Application/chrome.exe"),
    path.join(process.env["ProgramFiles"] || "", "Microsoft/Edge/Application/msedge.exe"),
    path.join(process.env["ProgramFiles(x86)"] || "", "Microsoft/Edge/Application/msedge.exe"),
    "/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser",
  ];
  for (const c of candidates) if (c && fs.existsSync(c)) return c;
  return null;
}

/* ── CDP 客户端 ── */
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.errors = [];
    this.sessionId = null;
    ws.addEventListener("message", (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { return; }
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) reject(new Error(msg.error.message + " (" + JSON.stringify(msg.error.data || "") + ")"));
        else resolve(msg.result);
        return;
      }
      this.onEvent(msg);
    });
  }
  onEvent(msg) {
    const m = msg.method;
    if (m === "Runtime.exceptionThrown") {
      const d = msg.params && msg.params.exceptionDetails;
      this.errors.push("exception: " + (d && (d.exception && d.exception.description || d.text)));
    } else if (m === "Runtime.consoleAPICalled" && msg.params && msg.params.type === "error") {
      this.errors.push("console.error: " + (msg.params.args || []).map((a) => a.value || a.description || "").join(" "));
    } else if (m === "Log.entryAdded" && msg.params && msg.params.entry && msg.params.entry.level === "error") {
      const text = msg.params.entry.text || "";
      /* 浏览器自身对 <meta> 里 frame-ancestors 的合规提示：与本阶段改动无关，
         且不是脚本异常（真正的 JS 异常走 Runtime.exceptionThrown） */
      if (/frame-ancestors/.test(text)) return;
      this.errors.push("log: " + text);
    }
  }
  send(method, params = {}, sessionId = this.sessionId) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify(sessionId ? { id, method, params, sessionId } : { id, method, params }));
    });
  }
  async evaluate(expression, awaitPromise = true) {
    const r = await this.send("Runtime.evaluate", {
      expression, awaitPromise, returnByValue: true, userGesture: true,
    });
    if (r.exceptionDetails) {
      const d = r.exceptionDetails;
      throw new Error("evaluate 异常: " + (d.exception && d.exception.description || d.text) + " @ " + expression.slice(0, 120));
    }
    return r.result ? r.result.value : undefined;
  }
  async waitFor(expression, timeoutMs = 15000, label = expression) {
    const t0 = Date.now();
    for (;;) {
      let ok = false;
      try { ok = await this.evaluate("!!(" + expression + ")"); } catch { ok = false; }
      if (ok) return true;
      if (Date.now() - t0 > timeoutMs) throw new Error("等待超时: " + label);
      await new Promise((r) => setTimeout(r, 120));
    }
  }
  async screenshot(name, clip) {
    fs.mkdirSync(outDir, { recursive: true });
    const params = { format: "png" };
    if (clip) params.clip = { ...clip, scale: 1 };
    const r = await this.send("Page.captureScreenshot", params);
    const file = path.join(outDir, name + ".png");
    fs.writeFileSync(file, Buffer.from(r.data, "base64"));
    return file;
  }
}

async function main() {
  const browser = findBrowser();
  if (!browser) {
    console.log("未找到 Chrome/Edge：跳过浏览器级验收（可用 MM_BROWSER 指定路径）");
    process.exit(0);
  }
  const health = await api("GET", "/api/health");
  if (health.status !== 200) {
    console.log("服务不可用（" + base + "）：请先启动服务或用 MM_BASE 指定地址");
    process.exit(1);
  }

  const port = 9300 + Math.floor(Math.random() * 300);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "mm-ui-check-"));
  const proc = spawn(browser, [
    "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
    "--disable-extensions", "--hide-scrollbars", "--mute-audio",
    "--window-size=1500,950",
    "--remote-debugging-port=" + port,
    "--user-data-dir=" + profile,
    "about:blank",
  ], { stdio: "ignore" });

  let cdp = null;
  let projectId = null;
  try {
    // 等 DevTools 端口就绪
    let version = null;
    for (let i = 0; i < 80; i++) {
      try {
        const r = await httpFetch(`http://127.0.0.1:${port}/json/version`);
        if (r.status === 200) { version = await r.json(); break; }
      } catch {}
      await new Promise((r) => setTimeout(r, 150));
    }
    if (!version) throw new Error("浏览器 DevTools 端口未就绪");

    const ws = new WebSocket(version.webSocketDebuggerUrl);
    await new Promise((res, rej) => {
      ws.addEventListener("open", res, { once: true });
      ws.addEventListener("error", () => rej(new Error("CDP 连接失败")), { once: true });
    });
    cdp = new CDP(ws);

    // 建项目 + 写一份 v1 老工程（验证 UI 侧迁移）
    const created = await api("POST", "/api/projects", { name: "UI 验收临时项目" });
    projectId = created.json && (created.json.id || (created.json.meta && created.json.meta.id));
    if (!projectId) throw new Error("创建临时项目失败: " + JSON.stringify(created.json));
    const v1 = {
      version: 1, bpm: 110, snap: 0.25, ppb: 26,
      loop: { on: false, start: 0, end: 16 },
      tracks: [
        { id: "tr_uiA", name: "UI 旧轨 A", color: "#00B8CC", volume: 0.8, mute: false, solo: false,
          source: { type: "synth", wave: "square", label: "合成器 · 方波" },
          clips: [{ id: "c_ui1", type: "midi", name: "melody", start: 0, length: 4,
            notes: [{ note: "C4", velocity: 100, start: 0, end: 1 }] }] },
        { id: "tr_uiB", name: "UI 旧轨 B", color: "#FF8C00", volume: 0.7, mute: false, solo: false,
          source: { type: "builtin", tone: "strings", label: "内置 · 弦乐群" }, clips: [] },
      ],
    };
    await api("PUT", `/api/projects/${projectId}/arrangement`, v1);

    const target = await cdp.send("Target.createTarget", { url: "about:blank" });
    const attached = await cdp.send("Target.attachToTarget", { targetId: target.targetId, flatten: true });
    cdp.sessionId = attached.sessionId;
    await cdp.send("Page.enable");
    await cdp.send("Runtime.enable");
    await cdp.send("Log.enable");

    const consoleErrorsBefore = () => cdp.errors.length;
    const errBase = consoleErrorsBefore();

    console.log("[UI] 加载页面并打开工程");
    /* 静态路由是文件名形式（/chat.html），/chat 会 404 */
    await cdp.send("Page.navigate", { url: base + "/chat.html" });
    await cdp.waitFor("window.Arrange && window.ArrRack && window.Arrange.initialized", 20000, "Arrange 初始化");
    await cdp.evaluate(`window.__openProject(${JSON.stringify(projectId)}, "")`);
    await cdp.waitFor(`window.Arrange.projectId === ${JSON.stringify(projectId)}`, 15000, "打开项目");
    await cdp.waitFor("window.Arrange.tracks.length > 0", 10000, "轨道载入");
    await new Promise((r) => setTimeout(r, 600)); // 等首帧渲染与防抖保存

    // 1) 状态与 v1→v2 迁移（页面内完成）
    const state = await cdp.evaluate(`(function(){
      var A = window.Arrange;
      return {
        version: A.cleanState().version,
        rackCount: A.rack.items.length,
        kinds: A.rack.items.map(function(i){return i.kind;}),
        sources: A.tracks.map(function(t){return t.source;}),
        legacy: A.tracks.some(function(t){return t.source.type;}),
        pianoRoll: A.pianoRollSource,
        badge: A.sourceBadgeText(A.tracks[0])
      };
    })()`);
    check("页面把 v1 工程迁移到 v2", state.version === 2 && !state.legacy, state);
    check("机架条目数 = 2（synth + 弦乐 0:48）", state.rackCount === 2, state.rackCount);
    check("条目种类齐备 synth/sf2", state.kinds.includes("synth") && state.kinds.includes("sf2"), state.kinds);
    check("轨道音源只剩 {itemId,presetId}",
      state.sources.every((s) => s.itemId && s.presetId !== undefined && !s.type), state.sources);
    check("钢琴窗音源已落到机架条目", !!state.pianoRoll.itemId, state.pianoRoll);
    check("轨道头徽标显示机架条目名（不是旧硬编码文案）",
      /^♪ /.test(state.badge) && !/内置 · |SF2 音源/.test(state.badge), state.badge);

    // 2) 机架窗口渲染（分类 / 搜索 / 空态）
    console.log("[UI] 机架窗口：条目 / 分类 / 搜索 / 空态");
    const rack = await cdp.evaluate(`(function(){
      var A = window.Arrange;
      A.openOutputRack();
      var win = document.getElementById("arrRackWindow");
      var rows = document.querySelectorAll("#arrRackList .arr-rack-item");
      function clickTab(tab){
        var el = document.querySelector('#arrRackFilters .rack-filter-btn[data-tab="' + tab + '"]');
        if (el) el.click();
        return document.querySelectorAll("#arrRackList .arr-rack-item").length;
      }
      var all = rows.length;
      var sf2Rows = clickTab("sf2");
      var mmRows = clickTab("mm");
      var empty = !!document.querySelector("#arrRackList .arr-rack-empty");
      clickTab("all");
      // 搜索（120ms 防抖）
      var input = document.getElementById("arrRackSearch");
      input.value = "弦乐";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      return { open: !win.hidden, all: all, sf2: sf2Rows, mm: mmRows,
               empty: empty, hasListbox: !!document.querySelector('#arrRackList[role="listbox"]'),
               hasHandle: !!document.querySelector("#arrRackList .arr-rack-item-drag-handle"),
               hasActions: !!document.querySelector("#arrRackList .arr-rack-preview-btn")
                 && !!document.querySelector("#arrRackList .arr-rack-assign-btn")
                 && !!document.querySelector("#arrRackList .arr-rack-more-btn"),
               status: (document.getElementById("arrRackStatusText") || {}).textContent || "" };
    })()`);
    check("机架窗口可打开并渲染条目", rack.open && rack.all === 2, rack);
    check("分类页签过滤生效（SF2=1 条 / MM=0 条）", rack.sf2 === 1 && rack.mm === 0, rack);
    check("MM 分类给空态引导", rack.empty, rack);
    check("列表 role=listbox + 拖把手 + 三个操作按钮",
      rack.hasListbox && rack.hasHandle && rack.hasActions, rack);
    await new Promise((r) => setTimeout(r, 300));
    const searched = await cdp.evaluate(`document.querySelectorAll("#arrRackList .arr-rack-item").length`);
    check("机架内搜索生效（关键词「弦乐」只剩 1 条）", searched === 1, searched);
    await cdp.evaluate(`(function(){ var i=document.getElementById("arrRackSearch"); i.value=""; i.dispatchEvent(new Event("input",{bubbles:true})); })()`);

    // 3) 音源选择器：两段式 / 键盘 / 预设二级视图
    console.log("[UI] 音源选择器：两段式 + 键盘 + 预设视图");
    await cdp.evaluate(`window.ArrRack.openPickerFor(window.Arrange, { assign: true, trackIdx: 0, title: "UI 检查" })`);
    // 注册表/预设是异步拉的：先等"加载态"落地成行（骨架屏本身也是 4.x 要求的六态之一）
    await cdp.waitFor('document.querySelectorAll("#sndPickerOverlay .snd-picker-row").length > 0',
      8000, "选择器行渲染");
    const picker = await cdp.evaluate(`(function(){
      var overlay = document.getElementById("sndPickerOverlay");
      return { open: overlay && !overlay.hidden,
               rows: document.querySelectorAll("#sndPickerOverlay .snd-picker-row").length,
               sections: document.querySelectorAll("#sndPickerOverlay .snd-picker-section").length,
               pills: document.querySelectorAll("#sndPickerOverlay .snd-picker-pill").length,
               onCount: document.querySelectorAll("#sndPickerOverlay .snd-picker-pill.on").length };
    })()`);
    check("选择器打开且两段式（已在机架 / 其他音源）", picker.open && picker.sections === 2, picker);
    check("候选包含机架条目 + 内置/注册表音源", picker.rows >= 8 && picker.onCount === 2, picker);

    // 键盘：↓ 移动活动行 → Tab 切页签
    const kbNav = await cdp.evaluate(`(function(){
      var overlay = document.getElementById("sndPickerOverlay");
      function key(k){
        overlay.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
      }
      var start = window.ArrRack._picker.active;
      key("ArrowDown"); key("ArrowDown");
      var afterDown = window.ArrRack._picker.active;
      var activeEl = document.querySelector("#sndPickerOverlay .snd-picker-row.active");
      key("Tab");
      var filter = window.ArrRack._picker.filter;
      key("Tab");
      var filter2 = window.ArrRack._picker.filter;
      return { start: start, afterDown: afterDown, hasActive: !!activeEl,
               filter: filter, filter2: filter2 };
    })()`);
    check("↑↓ 移动活动行并高亮", kbNav.afterDown === kbNav.start + 2 && kbNav.hasActive, kbNav);
    check("Tab 切换分类页签", kbNav.filter !== "all" && kbNav.filter2 !== kbNav.filter, kbNav);

    // Enter：对"其他音源"候选（已带具体预设，如合成器某一波形）→ 自动入架并立即分配
    const enterCandidate = await cdp.evaluate(`(async function(){
      var overlay = document.getElementById("sndPickerOverlay");
      function key(k){ overlay.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })); }
      // 回到"全部"分类（前面的 Tab 测试把页签切走了），让活动行停在"其他音源"的合成器条目
      window.ArrRack._picker.filter = "all";
      window.ArrRack.refreshPicker();
      var rows = window.ArrRack._picker.rows;
      var idx = -1;
      for (var i = 0; i < rows.length; i++) {
        if (!rows[i].inRack && rows[i].cand && rows[i].cand.kind === "synth") { idx = i; break; }
      }
      window.ArrRack._picker.active = idx >= 0 ? idx : 0;
      var want = idx >= 0 ? rows[idx].cand : null;
      key("Enter");
      return { idx: idx, rows: rows.length,
               wantSource: want ? want.sourceId : "", wantPreset: want ? want.presetId : "" };
    })()`);
    await cdp.waitFor("!window.ArrRack.isPickerOpen()", 8000, "候选入架后选择器关闭");
    const afterCandidate = await cdp.evaluate(`(function(){
      var A = window.Arrange;
      var item = window.ArrRack.find(A.rack.items, A.tracks[0].source.itemId);
      return { sourceId: item ? item.sourceId : "", preset: A.tracks[0].source.presetId,
               inRack: !!item, count: A.rack.items.length };
    })()`);
    check("Enter 选中「其他音源」候选 → 自动入架并立即分配（该行已带具体预设）",
      enterCandidate.idx >= 0 && afterCandidate.inRack &&
      afterCandidate.sourceId === enterCandidate.wantSource &&
      afterCandidate.preset === enterCandidate.wantPreset, { ...enterCandidate, ...afterCandidate });

    // 在架 SF2 条目 → 预设二级视图（真实音色库 269 个预设）→ Esc 分层返回
    await cdp.evaluate(`window.ArrRack.openPickerFor(window.Arrange, { assign: true, trackIdx: 0 })`);
    await cdp.waitFor('document.querySelectorAll("#sndPickerOverlay .snd-picker-row").length > 0',
      8000, "选择器重开");
    const presetState = await cdp.evaluate(`(function(){
      var hit = false;
      document.querySelectorAll("#sndPickerOverlay .snd-picker-row").forEach(function(row){
        var rec = window.ArrRack._picker.rows[Number(row.dataset.idx)];
        if (!hit && rec && rec.inRack && rec.item.kind === "sf2" && rec.item.sourceId.indexOf("builtin:") !== 0) {
          row.click(); hit = true;
        }
      });
      return { hit: hit };
    })()`);
    let presetRows = 0;
    try {
      await cdp.waitFor('document.querySelectorAll("#sndPickerOverlay .snd-picker-preset-row").length > 1',
        8000, "真实音色库预设列表");
      presetRows = await cdp.evaluate('document.querySelectorAll("#sndPickerOverlay .snd-picker-preset-row").length');
    } catch { /* 留待断言报错 */ }
    const presetOpen = await cdp.evaluate('!document.querySelector("#sndPickerOverlay .snd-picker-presets").hidden');
    check("在架 SF2 条目进入预设二级视图（真实音色库预设表可用）",
      presetState.hit && presetOpen && presetRows > 50, { presetOpen, presetRows, ...presetState });
    const presetPid = await cdp.evaluate(
      '(document.querySelector("#sndPickerOverlay .snd-picker-pid")||{}).textContent || ""');
    check("预设行显示 bank:program（等宽技术文本）", /^\d+:\d+$/.test(presetPid), presetPid);

    // Esc：预设视图 → 列表（分层返回，不直接关闭）
    const escState = await cdp.evaluate(`(function(){
      var overlay = document.getElementById("sndPickerOverlay");
      overlay.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      return { backToList: document.querySelector("#sndPickerOverlay .snd-picker-presets").hidden,
               open: window.ArrRack.isPickerOpen() };
    })()`);
    check("Esc 分层返回（预设 → 列表）而非直接关闭", escState.backToList && escState.open, escState);

    // 4) 通过选择器分配（已在机架段的目标条目 → 预设 → 落到服务端）
    console.log("[UI] 通过选择器分配音源并校验服务端状态");
    const assignTarget = await cdp.evaluate(`(function(){
      var A = window.Arrange;
      var before = A.tracks[0].source.itemId;
      var item = A.rack.items.filter(function(i){ return i.id !== before; })[0] || A.rack.items[0];
      // 点"已在机架"段里的目标条目行
      var hit = false;
      document.querySelectorAll("#sndPickerOverlay .snd-picker-row").forEach(function(row){
        var rec = window.ArrRack._picker.rows[Number(row.dataset.idx)];
        if (!hit && rec && rec.inRack && rec.item.id === item.id) { row.click(); hit = true; }
      });
      return { before: before, target: item.id, hit: hit };
    })()`);
    check("选择器里能点中「已在机架」的目标条目", assignTarget.hit, assignTarget);
    try {
      await cdp.waitFor('document.querySelectorAll("#sndPickerOverlay .snd-picker-preset-row").length > 0',
        8000, "目标条目预设列表");
      await cdp.evaluate('document.querySelectorAll("#sndPickerOverlay .snd-picker-preset-row")[0].click()');
    } catch { /* 留待断言报错 */ }
    await cdp.waitFor("!window.ArrRack.isPickerOpen()", 5000, "选择器关闭");
    const assigned = await cdp.evaluate(`(function(){
      var A = window.Arrange;
      return { after: A.tracks[0].source.itemId, preset: A.tracks[0].source.presetId };
    })()`);
    check("从选择器分配后轨道音源改指目标条目且选择器关闭",
      assigned.after === assignTarget.target && assigned.after !== assignTarget.before, { ...assignTarget, ...assigned });
    await new Promise((r) => setTimeout(r, 1800)); // 等 ops + 防抖保存（800ms）
    const srv = await api("GET", `/api/projects/${projectId}/arrangement`);
    const srvTrack = (srv.json.tracks || [])[0] || {};
    check("服务端收到同一份音源引用（前端与 AI 共用同一实现）",
      srvTrack.source && srvTrack.source.itemId === assigned.after, { srv: srvTrack.source, ui: assigned });
    check("服务端数据为 v2 且含 rack/pianoRoll/mixer",
      srv.json.version === 2 && !!srv.json.rack && !!srv.json.pianoRoll && !!srv.json.mixer, Object.keys(srv.json));

    // 5) AI 热应用：模拟服务端推送（走同一函数）
    const hot = await cdp.evaluate(`(function(){
      var A = window.Arrange;
      var item = A.rack.items[0];
      A.applyRemoteArrangement({
        version: 2, bpm: 90, tracks: [{ id: A.tracks[0].id, name: "AI 改名", mute: false, solo: false,
          volume: 0.42, source: { itemId: item.id, presetId: "sine" }, clips: [] }],
        rack: { items: A.rack.items }, pianoRoll: { itemId: item.id, presetId: "sine" },
        mixer: { channels: {} }, loop: { on: true, start: 4, end: 12 }
      }, ["tracks", "pianoRoll", "transport", "mixer"]);
      return { name: A.tracks[0].name, vol: A.tracks[0].volume, preset: A.tracks[0].source.presetId,
               bpm: A.bpm, clips: (A.tracks[0].clips || []).length, loopOn: A.loop.on };
    })()`);
    check("AI 热应用改名/音量/预设/走带生效",
      hot.name === "AI 改名" && Math.abs(hot.vol - 0.42) < 1e-6 && hot.preset === "sine" && hot.bpm === 90, hot);
    check("热应用保留本地 clip（不被服务端快照清空）", hot.clips === 1, hot.clips);

    // 6) 双主题截图（暗蓝 + 暖纸）
    console.log("[UI] 双主题截图 + reduced-motion 降级");
    await cdp.evaluate(`(function(){
      localStorage.setItem("melodymate-blueprint-theme", "dark");
      document.documentElement.dataset.theme = "dark";
      window.Arrange.openOutputRack();
    })()`);
    await new Promise((r) => setTimeout(r, 500));
    const rackBox = await cdp.evaluate(`(function(){
      var el = document.getElementById("arrRackWindow");
      var r = el.getBoundingClientRect();
      return { x: Math.max(0, r.x - 6), y: Math.max(0, r.y - 6), width: Math.min(r.width + 12, 1490), height: Math.min(r.height + 12, 940) };
    })()`);
    const shotDark = await cdp.screenshot("rack-dark", rackBox);
    check("暗蓝主题截图产出", fs.existsSync(shotDark));
    await cdp.evaluate(`document.documentElement.dataset.theme = "parchment"`);
    await new Promise((r) => setTimeout(r, 500));
    const shotWarm = await cdp.screenshot("rack-warm", rackBox);
    check("暖纸主题截图产出", fs.existsSync(shotWarm));

    // 选择器双主题截图
    await cdp.evaluate(`window.ArrRack.openPickerFor(window.Arrange, { assign: true, trackIdx: 0 })`);
    await new Promise((r) => setTimeout(r, 400));
    const pickerWarm = await cdp.screenshot("picker-warm");
    await cdp.evaluate(`document.documentElement.dataset.theme = "dark"`);
    await new Promise((r) => setTimeout(r, 300));
    const pickerDark = await cdp.screenshot("picker-dark");
    check("选择器双主题截图产出", fs.existsSync(pickerWarm) && fs.existsSync(pickerDark));

    // reduced-motion：动画必须降级为 none
    await cdp.send("Emulation.setEmulatedMedia", {
      features: [{ name: "prefers-reduced-motion", value: "reduce" }],
    });
    const rm = await cdp.evaluate(`(function(){
      var item = document.querySelector("#arrRackList .arr-rack-item");
      var ghost = document.querySelector(".arr-rack-dragghost");
      var skel = document.querySelector(".snd-picker-skel");
      var picker = window.ArrRack._pickerEls();
      return {
        item: item ? getComputedStyle(item).animationName : "no-item",
        preset: getComputedStyle(picker.presets).animationName,
        echo: (function(){ var p = document.getElementById("arrPosDisplay"); if(!p) return "n/a";
                 p.classList.add("ai-echo"); var n = getComputedStyle(p, "::after").animationName; p.classList.remove("ai-echo"); return n; })()
      };
    })()`);
    check("reduced-motion 下条目进场动画关闭", rm.item === "none", rm);
    check("reduced-motion 下预设滑入动画关闭", rm.preset === "none", rm);
    check("reduced-motion 下 AI 回显脉冲关闭", rm.echo === "none" || rm.echo === "n/a", rm);
    await cdp.send("Emulation.setEmulatedMedia", { features: [] });

    // 7) 控制台零异常
    const errs = cdp.errors.slice(errBase);
    check("页面无 JS 异常 / console.error", errs.length === 0, errs.slice(0, 5));

    fs.writeFileSync(path.join(outDir, "README.txt"),
      "本目录为 tools/dev/rack_ui_check.mjs 的浏览器验收截图（不入库）。\n" +
      "rack-dark.png / rack-warm.png：机架窗口双主题\n" +
      "picker-dark.png / picker-warm.png：音源选择器双主题\n");
    console.log("\n截图输出：" + outDir);
  } finally {
    if (cdp && cdp.ws) { try { cdp.ws.close(); } catch {} }
    try { proc.kill(); } catch {}
    if (projectId) await api("DELETE", `/api/projects/${projectId}`).catch(() => {});
    try { fs.rmSync(profile, { recursive: true, force: true }); } catch {}
  }

  console.log(failures ? `\n结果: ${failures} 项失败` : "\n结果: 全部通过");
  process.exit(failures ? 1 : 0);
}

main().catch((e) => {
  console.error("UI 验收脚本异常: " + (e && e.stack ? e.stack : e));
  process.exit(2);
});
