/* 编曲窗浏览器级验收：CDP 驱动无头 Chrome/Edge。
 *
 * P2（乐器箱 / 机架 / 音源选择器）与 P3（素材库三页签）都在这里跑真实页面交互：
 * v1 老工程迁移 → 机架条目渲染 → 分类/搜索/空态 → 音源选择器（键盘 ↑↓/Enter/Esc、
 * 预设二级视图）→ 分配落到服务端 → 素材库三页签（采样树 + 波形缩略 + 音源卡片 +
 * 效果器空态 + 跨页签搜索）→ 双主题截图 → prefers-reduced-motion 降级 → 零异常。
 *
 * 用法（需服务在跑，且本机有 Chrome 或 Edge）：
 *   MM_BASE=http://127.0.0.1:7912 node tools/dev/arrange_ui_check.mjs
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
    /* 显式 Content-Length：Node 默认 chunked，而 Go 侧 handler 在 chunked
       DELETE 时读不到请求体（浏览器 fetch 发字符串 body 时带 Content-Length） */
    const headers = { Connection: "close", ...(opts.headers || {}) };
    if (opts.body !== undefined && opts.body !== null) {
      headers["Content-Length"] = Buffer.byteLength(opts.body);
    }
    const req = httpRequest(
      { hostname: u.hostname, port: u.port, path: u.pathname + u.search,
        method: (opts.method || "GET").toUpperCase(),
        headers: headers },
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

/* 造一个可解码的最小 WAV（16bit PCM 单声道正弦）：素材页签的波形缩略图要真解码 */
function makeWav(seconds, freq) {
  const rate = 44100;
  const n = Math.floor(rate * seconds);
  const buf = Buffer.alloc(44 + n * 2);
  buf.write("RIFF", 0);
  buf.writeUInt32LE(36 + n * 2, 4);
  buf.write("WAVE", 8);
  buf.write("fmt ", 12);
  buf.writeUInt32LE(16, 16);
  buf.writeUInt16LE(1, 20);
  buf.writeUInt16LE(1, 22);
  buf.writeUInt32LE(rate, 24);
  buf.writeUInt32LE(rate * 2, 28);
  buf.writeUInt16LE(2, 32);
  buf.writeUInt16LE(16, 34);
  buf.write("data", 36);
  buf.writeUInt32LE(n * 2, 40);
  for (let i = 0; i < n; i++) {
    const v = Math.sin((2 * Math.PI * freq * i) / rate) * 0.6 * (1 - i / n);
    buf.writeInt16LE(Math.max(-32768, Math.min(32767, Math.round(v * 32767))), 44 + i * 2);
  }
  return buf;
}

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
    await cdp.waitFor("!!window.ArrMaterial", 10000, "素材库模块加载");
    await cdp.evaluate(`window.__openProject(${JSON.stringify(projectId)}, "")`);
    await cdp.waitFor(`window.Arrange.projectId === ${JSON.stringify(projectId)}`, 15000, "打开项目");
    await cdp.waitFor("window.Arrange.tracks.length > 0", 10000, "轨道载入");
    // 首启声明 / Star 弹窗会盖住界面：先关掉（否则截图全是遮罩）
    await cdp.evaluate(`(function(){
      ["disclaimerOk", "starLaterBtn"].forEach(function(id){
        var b = document.getElementById(id);
        if (b && b.offsetParent !== null) { try { b.click(); } catch (e) {} }
      });
      ["disclaimerOverlay", "starOverlay"].forEach(function(id){
        var o = document.getElementById(id);
        if (o) o.hidden = true;
      });
      return true;
    })()`);
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

    // 5) 「＋ 加载音源」= 只入架不分配（机架 ＋ 与空态大按钮都走这条）
    const addFlow = await cdp.evaluate(`(async function(){
      var A = window.Arrange;
      A.openOutputRack();
      var before = A.rack.items.length;
      var trackBefore = A.tracks[0].source.itemId;
      document.getElementById("arrRackAddSourceBtn").click();
      await new Promise(function(r){ setTimeout(r, 120); });
      var opened = window.ArrRack.isPickerOpen();
      // 选一个"其他音源"候选（合成器波形之一）
      var rows = window.ArrRack._picker.rows;
      var idx = -1;
      for (var i = 0; i < rows.length; i++) {
        if (!rows[i].inRack && rows[i].cand && rows[i].cand.kind === "synth") { idx = i; break; }
      }
      if (idx < 0) return { opened: opened, idx: idx };
      window.ArrRack._picker.active = idx;
      var want = rows[idx].cand;
      var overlay = document.getElementById("sndPickerOverlay");
      overlay.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      return { opened: opened, idx: idx, wantSource: want.sourceId, wantPreset: want.presetId,
               before: before, trackBefore: trackBefore };
    })()`);
    await cdp.waitFor("!window.ArrRack.isPickerOpen()", 8000, "加载音源后选择器关闭");
    const addResult = await cdp.evaluate(`(function(){
      var A = window.Arrange;
      var last = A.rack.items[A.rack.items.length - 1];
      return { count: A.rack.items.length, lastSource: last ? last.sourceId : "",
               lastPreset: last ? last.presetId : "", track: A.tracks[0].source.itemId,
               inRackSection: !![].slice.call(document.querySelectorAll("#arrRackList .arr-rack-item")).length };
    })()`);
    check("「＋ 加载音源」只入架不分配（轨道音源不变）",
      addFlow.opened && addResult.count === addFlow.before + 1 &&
      addResult.lastSource === addFlow.wantSource && addResult.lastPreset === addFlow.wantPreset &&
      addResult.track === addFlow.trackBefore,
      { ...addFlow, ...addResult });

    // 6) 试听链路：引擎轨 29 的电平非零 = 音源真的发声（无引擎时跳过）
    const engineReady = await cdp.evaluate(
      'window.__engineState === "ready" && !!(window.AudioBackend && window.AudioBackend.isEngine && window.AudioBackend.isEngine())');
    if (!engineReady) {
      console.log("  skip 试听实测（引擎未就绪或处于 WebAudio 模式）");
    } else {
      const previews = await cdp.evaluate(`(async function(){
        var A = window.Arrange;
        var out = [];
        var seen = {};
        for (var i = 0; i < A.rack.items.length; i++) {
          var item = A.rack.items[i];
          if (item.kind !== "synth" && item.kind !== "sf2") continue;
          var key = item.kind + "|" + (item.presetId || "");
          if (seen[key]) continue;
          seen[key] = true;
          A.previewRackItem(item);
          var peak = 0, t0 = performance.now();
          while (performance.now() - t0 < 1200) {
            await new Promise(function(r){ setTimeout(r, 40); });
            var lv = (window.__engineLevels || [])[29] || 0;
            if (lv > peak) peak = lv;
          }
          out.push({ kind: item.kind, preset: item.presetId || "", name: item.name, peak: peak });
        }
        return out;
      })()`);
      const silent = previews.filter((p) => !(p.peak > 0.001));
      check("机架音源试听实测：引擎试听轨（29）电平非零",
        previews.length > 0 && silent.length === 0, previews);
      console.log("       " + previews.map((p) => p.kind + "/" + (p.preset || "-") + " peak=" + p.peak.toFixed(4)).join("  "));
    }

    // 6) 素材库三页签（P3）
    console.log("[UI] 素材库：三页签 / 采样树与波形 / 音源卡片 / 效果器空态");
    /* 素材库在编曲窗内：先打开编曲窗（否则右栏 display:none，截图与尺寸都不对） */
    await cdp.evaluate("window.Arrange.open()");
    await cdp.waitFor("!!document.querySelector('#arrangeWorkspace') && !document.querySelector('#arrangeWorkspace').hidden",
      8000, "编曲窗打开");
    /* 编曲窗首开 500ms 后弹 Beta 提示（sessionStorage 记忆）：先按键记档再关掉 */
    await new Promise((r) => setTimeout(r, 700));
    await cdp.evaluate(`(function(){
      try { sessionStorage.setItem("melodymate_arr_beta_v1", "1"); } catch (e) {}
      var ok = document.getElementById("arrBetaOk");
      if (ok) { try { ok.click(); } catch (e) {} }
      var m = document.getElementById("arrBetaModal");
      if (m) { m.hidden = true; m.classList.remove("modal-in"); }
      return true;
    })()`);
    await new Promise((r) => setTimeout(r, 300));
    // 先造一个真实的采样目录（含可解码的 wav），并登记成采样包
    const sampleDir = fs.mkdtempSync(path.join(os.tmpdir(), "mm-mat-"));
    fs.writeFileSync(path.join(sampleDir, "kick_808.wav"), makeWav(0.25, 220));
    fs.writeFileSync(path.join(sampleDir, "snare_top.wav"), makeWav(0.18, 880));
    const addDir = await api("POST", "/api/arrangement/dirs", { path: sampleDir });
    check("登记素材目录", addDir.status === 200, addDir.json);
    const packAdd = await api("POST", "/api/sound/sources", {
      kind: "samplepack", name: "UI 检查采样包", dirs: [sampleDir],
    });
    check("同名目录登记为采样包（供分组标签）", packAdd.status === 200, packAdd.json);

    const tabs = await cdp.evaluate(`(function(){
      var t = document.querySelectorAll("#matTabs .mat-tab");
      return { count: t.length, ids: [].map.call(t, function(b){ return b.dataset.tab; }),
               active: (document.querySelector("#matTabs .mat-tab.active")||{}).dataset
                 ? document.querySelector("#matTabs .mat-tab.active").dataset.tab : "" };
    })()`);
    check("素材库三页签 = 采样/音源/效果器",
      JSON.stringify(tabs.ids) === JSON.stringify(["sample", "source", "fx"]), tabs);

    // 采样页签：拉目录 → 展开根节点 → 波形与元信息
    await cdp.evaluate("window.ArrMaterial.loadDirs(window.Arrange)");
    await cdp.waitFor("document.querySelectorAll('#matBody .arr-file-item.dir').length > 0", 8000, "素材根目录行");
    const sampleTab = await cdp.evaluate(`(function(){
      var rows = document.querySelectorAll("#matBody .arr-file-item.dir");
      var root = null;
      for (var i = 0; i < rows.length; i++) {
        if (rows[i].title === ${JSON.stringify(sampleDir)}) { root = rows[i]; break; }
      }
      if (!root) return { root: "", found: false };
      root.click();
      return { root: root.textContent.trim(), found: true };
    })()`);
    check("展开目标素材根目录", sampleTab.found, sampleTab);
    await cdp.waitFor("document.querySelectorAll('#matBody .mat-sample-row').length > 0", 8000, "采样文件行");
    await cdp.waitFor(
      "!!document.querySelector('#matBody .mat-sample-row .mat-item-meta') && /kHz/.test(document.querySelector('#matBody .mat-sample-row .mat-item-meta').textContent)",
      10000, "波形分析完成（元信息含采样率）");
    const rows = await cdp.evaluate(`(function(){
      var r = document.querySelectorAll("#matBody .mat-sample-row");
      var first = r[0];
      var canvas = first.querySelector("canvas.mat-wave");
      var painted = false;
      try {
        var ctx = canvas.getContext("2d");
        var d = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
        for (var i = 3; i < d.length; i += 4) { if (d[i] > 0) { painted = true; break; } }
      } catch (e) {}
      return { count: r.length, painted: painted,
               meta: first.querySelector(".mat-item-meta").textContent,
               title: first.title,
               draggable: first.draggable,
               packTag: (document.querySelector("#matBody .mat-pack-tag")||{}).textContent || "" };
    })()`);
    check("采样页签渲染文件行（含拖拽语义）", rows.count >= 2 && rows.draggable, rows);
    check("波形缩略图已绘制（canvas 有像素）", rows.painted, rows);
    check("元信息含时长/采样率/大小", /kHz/.test(rows.meta) && /:/.test(rows.meta), rows.meta);
    check("根目录显示所属采样包标签", rows.packTag === "UI 检查采样包", rows);
    console.log("       " + sampleTab.root.replace(/\\s+/g, " ").slice(0, 60) + " | " + rows.meta);

    // 音源页签：卡片 + 状态胶囊 + AI 徽标
    const sourceTab = await cdp.evaluate(`(async function(){
      window.ArrMaterial.setTab(window.Arrange, "source");
      await new Promise(function(r){ setTimeout(r, 200); });
      var cards = document.querySelectorAll("#matBody .mat-source-card");
      var pills = [].map.call(document.querySelectorAll("#matBody .mat-pill"), function(p){ return p.textContent; });
      return { cards: cards.length, on: pills.filter(function(t){ return t === "已在架"; }).length,
               add: pills.filter(function(t){ return t === "＋ 入架"; }).length,
               grid: !!document.querySelector("#matBody .mat-grid") };
    })()`);
    check("音源页签渲染注册表卡片网格", sourceTab.grid && sourceTab.cards >= 2, sourceTab);
    check("卡片带 [已在架]/[＋ 入架] 状态胶囊", sourceTab.on >= 1 && sourceTab.add >= 1, sourceTab);

    // 跨页签搜索：关键词保留 + 过滤生效
    const search = await cdp.evaluate(`(async function(){
      var input = document.getElementById("matSearch");
      input.value = "Pianoteq";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await new Promise(function(r){ setTimeout(r, 260); });
      var srcFiltered = document.querySelectorAll("#matBody .mat-source-card").length;
      window.ArrMaterial.setTab(window.Arrange, "sample");
      await new Promise(function(r){ setTimeout(r, 200); });
      var kept = document.getElementById("matSearch").value;
      var sampleFiltered = document.querySelectorAll("#matBody .arr-file-item.dir").length;
      input.value = "";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await new Promise(function(r){ setTimeout(r, 260); });
      var restored = document.querySelectorAll("#matBody .arr-file-item.dir").length;
      return { srcFiltered: srcFiltered, kept: kept, sampleFiltered: sampleFiltered, restored: restored };
    })()`);
    check("搜索框跨页签保留关键词", search.kept === "Pianoteq" && search.srcFiltered >= 1, search);
    check("搜索在采样页签同样生效（清空后恢复）",
      search.sampleFiltered === 0 && search.restored >= 1, search);

    // 效果器页签：引导空态（P4/P5 前）
    const fxTab = await cdp.evaluate(`(async function(){
      window.ArrMaterial.setTab(window.Arrange, "fx");
      await new Promise(function(r){ setTimeout(r, 200); });
      var empty = document.querySelector("#matBody .mat-empty");
      return { hasEmpty: !!empty, text: empty ? empty.textContent : "",
               hint: (document.getElementById("matHint")||{}).textContent || "" };
    })()`);
    check("效果器页签给出引导空态（未接入前）",
      fxTab.hasEmpty && /效果器/.test(fxTab.text), fxTab);
    check("页签提示语随页签切换", /FX/.test(fxTab.hint) || /效果器/.test(fxTab.hint), fxTab.hint);

    // 音源卡片「＋ 入架」→ 分配
    const cardAssign = await cdp.evaluate(`(async function(){
      var A = window.Arrange;
      window.ArrMaterial.setTab(A, "source");
      await new Promise(function(r){ setTimeout(r, 200); });
      var before = A.tracks[0].source.itemId;
      var pill = null;
      var pills = document.querySelectorAll("#matBody .mat-pill");
      for (var i = 0; i < pills.length; i++) {
        if (pills[i].textContent.indexOf("入架") >= 0) { pill = pills[i]; break; }
      }
      if (!pill) return { ok: false };
      pill.click();
      await new Promise(function(r){ setTimeout(r, 800); });
      return { ok: true, before: before, after: A.tracks[0].source.itemId, name: pill.textContent };
    })()`);
    check("音源卡片「＋ 入架」= 入架并分配给选中轨",
      cardAssign.ok && cardAssign.after && cardAssign.after !== cardAssign.before, cardAssign);

    // 素材库双主题截图（关掉机架浮窗与任何遗留遮罩，整页截）
    await cdp.evaluate(`(function(){
      var w = document.getElementById("arrRackWindow");
      if (w) w.hidden = true;
      document.querySelectorAll(".modal-overlay").forEach(function(o){
        if (o.id !== "sndPickerOverlay") o.hidden = true;
      });
      return true;
    })()`);
    await cdp.evaluate('document.documentElement.dataset.theme = "parchment"');
    await cdp.evaluate("window.ArrMaterial.setTab(window.Arrange, 'sample')");
    await new Promise((r) => setTimeout(r, 600));
    let matShot = await cdp.screenshot("material-warm");
    await cdp.evaluate('document.documentElement.dataset.theme = "dark"');
    await cdp.evaluate("window.ArrMaterial.setTab(window.Arrange, 'source')");
    await new Promise((r) => setTimeout(r, 600));
    let srcShot = await cdp.screenshot("material-source-dark");
    check("素材库双主题截图产出", fs.existsSync(matShot) && fs.existsSync(srcShot));

    // 清理：注销目录与采样包
    await api("DELETE", "/api/arrangement/dirs", { path: sampleDir });
    if (packAdd.json && packAdd.json.source) await api("DELETE", `/api/sound/sources/${packAdd.json.source.id}`);
    try { fs.rmSync(sampleDir, { recursive: true, force: true }); } catch {}


    // 6) AI 热应用：模拟服务端推送（走同一函数）
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

    // 7) 双主题截图（暗蓝 + 暖纸）
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

    // 8) 控制台零异常
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
