/* 混音台 v1（P4）检查工具。
 *
 * 三部分：
 *   A. 静态契约：chat.html ↔ arrange.ts/mixer.ts 的 DOM id 对齐、4.5 节结构
 *      （底部面板 / 通道条 1:1 / 主通道 / 8 槽 / 双表）、CSS 令牌与动效约束、
 *      键盘可达与 ARIA；
 *   B. 纯逻辑（VM 加载真实产物 frontend/js/arrangement/mixer.js）：
 *      dB 换算、推子位置映射、电平→表高、插件名缩写；
 *   C. 活体冒烟（需 MM_BASE）：ops 的 setTrackMix / setFxSlot / setMaster
 *      落到 arrangement.mixer，含错误路径（越界槽位、插件装载 P5 前拒绝）。
 *
 * 引擎侧（原生 DSP）由 tools/engine_smoke 覆盖：
 *   fx.setChain 槽参数 / fx.getChain 空置回读 / mixer.setMaster /
 *   getLevels 的 master 字段 / 协议版本 v2。
 *
 * 浏览器级交互（推子拖动、电平表 scaleY、F9、双主题）由
 * tools/dev/arrange_ui_check.mjs 覆盖。
 *
 * 用法：
 *   node tools/dev/mixer_check.mjs
 *   MM_BASE=http://127.0.0.1:7860 node tools/dev/mixer_check.mjs
 */
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";
import { request as httpRequest } from "node:http";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const read = (p) => fs.readFileSync(path.join(repoRoot, p), "utf8");

let failures = 0;
function check(name, ok, extra) {
  if (ok) console.log("  ok   " + name);
  else {
    failures++;
    console.log("  FAIL " + name + (extra !== undefined ? "  → " + JSON.stringify(extra).slice(0, 300) : ""));
  }
}

/* ═══════════ A. 静态契约 ═══════════ */

console.log("[A] 静态契约");

const chatHtml = read("frontend/chat.html");
const mixerTs = read("frontend/src/arrangement/mixer.ts");
const arrangeTs = read("frontend/src/arrangement/arrange.ts");
const styleCss = read("frontend/style.css");
const shortcutsTs = read("frontend/src/shortcuts.ts");

const htmlIds = new Set([...chatHtml.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((m) => m[1]));
for (const id of ["mxrBtn", "mxrPanel", "mxrLanes", "mxrMaster", "mxrCloseBtn", "mxrCount"]) {
  check(`chat.html 含 #${id}`, htmlIds.has(id));
}
const refIds = [...new Set([...mixerTs.matchAll(/getElementById\("([A-Za-z0-9_-]+)"\)/g)].map((m) => m[1]))];
const missing = refIds.filter((id) => !htmlIds.has(id));
check("mixer.ts 引用的 DOM id 都存在", missing.length === 0, missing);

check("chat.html 加载 /js/arrangement/mixer.js", chatHtml.includes('src="/js/arrangement/mixer.js"'));
check("mixer.js 先于 arrange.js 加载",
  chatHtml.indexOf("mixer.js") < chatHtml.indexOf("arrange.js"));
check("顶栏有混音台按钮（aria-expanded/aria-controls）",
  /id="mxrBtn"[^>]*aria-expanded="false"[^>]*aria-controls="mxrPanel"/.test(chatHtml.replace(/\n/g, " ")));
check("面板默认 hidden（不占位）", /id="mxrPanel" hidden/.test(chatHtml));
check("通道条容器 role=group + 主通道独立容器",
  chatHtml.includes('id="mxrLanes" role="group"') && chatHtml.includes('id="mxrMaster"'));
check("F9 快捷键已登记（可在设置改键）", shortcutsTs.includes('"arrange.mixer": "F9"'));
check("arrange.ts 绑定混音台并接快捷键",
  arrangeTs.includes("window.ArrMixer.bind(this)") &&
  arrangeTs.includes('K.matches(e, "arrange.mixer")') &&
  arrangeTs.includes("window.ArrMixer.toggle(self)"));
check("轨道渲染后同步通道条（1:1 增量）", arrangeTs.includes("window.ArrMixer.syncTracks(this)"));
check("声像由混音台注入引擎混音（applyMixSafe 读 channels[id].pan）",
  arrangeTs.includes("mixCh.pan"));

// 4.5 规格要点
check("通道条含色条/名称/声像/FX/推子/电平表/M,S",
  ["mxr-color", "mxr-name", "mxr-pan", "mxr-fx", "mxr-fader", "mxr-meter", "mxr-ms-btn"].every((c) => mixerTs.includes(c)));
check("8 个 FX 槽", mixerTs.includes("var MAX_SLOTS = 8"));
check("自绘推子（无 input[type=range]）", !mixerTs.includes("type = \"range\"") && !mixerTs.includes('"range"'));
check("推子拖拽用 pointer capture", mixerTs.includes("setPointerCapture"));
check("拖拽中不打服务端（松手才提交）",
  /松手才写/.test(mixerTs) && mixerTs.includes("commitFader"));
check("双击复位 0dB", mixerTs.includes("setMasterGain(ctrl, 1.0, true)") && mixerTs.includes("setGain(ctrl, idx, 1.0, true)"));
check("电平表只改 transform/opacity",
  mixerTs.includes('"scaleY(" + r.toFixed(3) + ")"') && mixerTs.includes("peakEl.style.opacity"));
check("面板关闭停 rAF（不空转）", mixerTs.includes("function stopMeters") && mixerTs.includes("if (!state.open) return;"));
check("写入走 ops API（与 AI 同一路径）",
  mixerTs.includes("ctrl.arrangeOps(ops)") && mixerTs.includes('op: "setTrackMix"') &&
  mixerTs.includes('op: "setFxSlot"') && mixerTs.includes('op: "setMaster"'));
check("失败回退整份 PUT（不静默丢改动）", mixerTs.includes("ctrl.scheduleSave();"));
check("FX 插件装载如实标注未开放（不做假状态）",
  mixerTs.includes("P5 VST3 / P6 MM 开放") && mixerTs.includes("装载插件…（P5"));
check("键盘：↑↓ 推子 / ←→ 声像 / Enter 切 M,S",
  mixerTs.includes('k !== "ArrowUp"') && mixerTs.includes('k !== "ArrowLeft"') && mixerTs.includes('k === "Enter"'));
check("ARIA：推子 role=slider + aria-valuenow/text",
  mixerTs.includes('setAttribute("role", "slider")') && mixerTs.includes('"aria-valuetext"'));
check("通道条 role=group + aria-label", mixerTs.includes('setAttribute("role", "group")'));

const cssStart = styleCss.indexOf("混音台 v1（P4，4.5 节规格）");
const mxrCss = styleCss.slice(cssStart);
check("新增 CSS 段存在", cssStart > 0 && mxrCss.length > 2000, mxrCss.length);
check("CSS 段花括号平衡",
  (mxrCss.match(/\{/g) || []).length === (mxrCss.match(/\}/g) || []).length,
  [(mxrCss.match(/\{/g) || []).length, (mxrCss.match(/\}/g) || []).length]);
const rawHex = [...mxrCss.matchAll(/^\s*[a-z-]+:\s*[^;]*#[0-9a-fA-F]{3,6}/gm)].map((m) => m[0].trim());
check("新增 CSS 无硬编码 hex 颜色", rawHex.length === 0, rawHex.slice(0, 3));
check("动效只用 transform/opacity（无 height 过渡）",
  !/\.mxr-[a-z-]+[^{]*\{[^}]*transition:[^}]*\bheight\b/.test(mxrCss));
check("reduced-motion 降级覆盖混音台",
  mxrCss.includes("prefers-reduced-motion") && mxrCss.includes(".mxr-panel.mxr-in"));
check("六态齐备（hover/focus-visible/active/dragging/hot/empty）",
  mxrCss.includes(":hover") && mxrCss.includes(":focus-visible") && mxrCss.includes(":active") &&
  mxrCss.includes(".dragging") && mxrCss.includes(".hot") && mxrCss.includes(".mxr-empty"));
check("通道条子项不被 flex 压扁（曾把色条与轨道名压成 0 高度）",
  mxrCss.includes(".mxr-strip > * { flex: 0 0 auto; }"));
check("等宽字体用于 dB/编号", mxrCss.includes("Cascadia Mono"));

/* ═══════════ B. 纯逻辑（真实产物） ═══════════ */

console.log("\n[B] 纯逻辑（VM 加载 frontend/js/arrangement/mixer.js）");

const sandbox = {
  console,
  setTimeout: () => 0,
  clearTimeout: () => {},
  requestAnimationFrame: () => 1,
  cancelAnimationFrame: () => {},
  localStorage: { getItem: () => null, setItem: () => {} },
  UI: { qsa: () => [], toast: () => {} },
  document: { createElement: () => ({ classList: { add() {}, remove() {}, toggle() {} }, style: {}, dataset: {}, appendChild() {}, setAttribute() {}, querySelector: () => null }), getElementById: () => null },
};
sandbox.window = sandbox;
sandbox.self = sandbox;
vm.createContext(sandbox);
vm.runInContext(read("frontend/js/arrangement/mixer.js"), sandbox, { filename: "mixer.js" });

const M = sandbox.window.ArrMixer;
check("mixer.js 注册 window.ArrMixer", !!M);
check("8 槽 / dB 范围常量", M.MAX_SLOTS === 8 && M.MIN_DB === -60 && M.MAX_DB === 6);
check("主总线用 -1 哨兵", M.MASTER_TRACK === -1);

check("0dB（增益 1.0）→ +0.0", M.gainToDbText(1.0) === "+0.0", M.gainToDbText(1.0));
check("半幅 → -6.0dB", M.gainToDbText(0.5) === "-6.0", M.gainToDbText(0.5));
check("0.7071 ≈ -3.0dB", M.gainToDbText(0.7071) === "-3.0", M.gainToDbText(0.7071));
check("静音 → -∞", M.gainToDbText(0) === "-∞" && M.gainToDbText(0.00001) === "-∞");
check("推子位置↔增益线性且夹取 0~1",
  M.posToGain(0.25) === 0.25 && M.posToGain(-1) === 0 && M.posToGain(9) === 1 && M.gainToPos(0.4) === 0.4);
check("电平→表高：0 与 -60dB 以下为 0",
  M.levelToRatio(0) === 0 && M.levelToRatio(0.0005) === 0);
check("电平→表高：0dB(1.0) → (0-(-60))/(6-(-60)) ≈ 0.909",
  Math.abs(M.levelToRatio(1.0) - 60 / 66) < 1e-6, M.levelToRatio(1.0));
check("电平→表高：超过 +6dB 夹到 1", M.levelToRatio(4.0) === 1);
check("电平→表高单调不减", M.levelToRatio(0.1) < M.levelToRatio(0.5) && M.levelToRatio(0.5) < M.levelToRatio(0.9));
check("插件名缩写：去路径与扩展名", M.shortPluginName("C:/Program Files/Serum.vst3") === "Serum", M.shortPluginName("C:/Program Files/Serum.vst3"));
check("插件名缩写：vst3: 前缀取尾段", M.shortPluginName("vst3:Pro-Q 3") === "Pro-Q 3", M.shortPluginName("vst3:Pro-Q 3"));
check("插件名缩写：超长截断带省略号", M.shortPluginName("AVeryLongPluginName").endsWith("…"));

// 通道状态访问：无 mixer 时应自动建结构（AI 热应用换工程后可能只有空对象）
const fakeCtrl = { tracks: [{ id: "t1", volume: 0.8, mute: false, solo: false }], mixer: undefined };
const ch = M.channelOf(fakeCtrl, "t1");
check("channelOf 自建通道结构", ch && ch.gain === 1 && ch.pan === 0 && Array.isArray(ch.fx));
check("masterOf 自建主总线", M.masterOf(fakeCtrl).gain === 1);
check("gainOf 以 track.volume 为准", M.gainOf(fakeCtrl, 0) === 0.8);
fakeCtrl.tracks[0].volume = undefined;
check("缺 volume 时回落 mixer 通道 gain", M.gainOf(fakeCtrl, 0) === 1);
check("panOf 默认居中", M.panOf(fakeCtrl, 0) === 0);

/* ═══════════ C. 活体冒烟（可选） ═══════════ */

function httpFetch(url, opts = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    /* 显式 Content-Length：Node 默认 chunked，Go 侧 handler 在 chunked
       DELETE 时读不到请求体（浏览器 fetch 行为是带 Content-Length） */
    const headers = { Connection: "close", ...(opts.headers || {}) };
    if (opts.body !== undefined && opts.body !== null) {
      headers["Content-Length"] = Buffer.byteLength(opts.body);
    }
    const req = httpRequest(
      { hostname: u.hostname, port: u.port, path: u.pathname + u.search,
        method: (opts.method || "GET").toUpperCase(), headers },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          const body = Buffer.concat(chunks).toString("utf8");
          resolve({ status: res.statusCode, text: async () => body, json: async () => JSON.parse(body) });
        });
      }
    );
    req.on("error", reject);
    if (opts.body !== undefined && opts.body !== null) req.write(opts.body);
    req.end();
  });
}

const base = process.env.MM_BASE;
if (!base) {
  console.log("\n[C] 活体冒烟：跳过（未设置 MM_BASE）");
} else {
  console.log("\n[C] 活体冒烟 → " + base);
  const api = async (method, p, body) => {
    const r = await httpFetch(base + p, {
      method, headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    return { status: r.status, json: await r.json().catch(() => ({})) };
  };
  const ops = (pid, list) => api("POST", `/api/projects/${pid}/arrangement/ops`, { ops: list, client: "mixer_check" });

  const proj = await api("POST", "/api/projects", { name: "混音台检查" });
  /* 创建接口直接返回工程对象（meta.id）；不同版本可能包一层 project */
  const projBody = proj.json || {};
  const pid = (projBody.project && projBody.project.id) || (projBody.meta && projBody.meta.id) || projBody.id;
  check("创建临时工程", !!pid, Object.keys(projBody).slice(0, 6));
  if (pid) {
    // 建 2 条轨（新工程默认 1 条）
    await ops(pid, [{ op: "addTrack", name: "检查轨 B" }]);
    const listed = await api("GET", `/api/projects/${pid}/arrangement`);
    const tracks = (listed.json.tracks || []);
    check("工程有 2 条轨（通道条 1:1 的数据面）", tracks.length === 2, tracks.length);

    const mixRes = await ops(pid, [
      { op: "setTrackMix", track: 0, gain: 0.42, pan: -0.5, mute: true },
      { op: "setMaster", gain: 0.8 },
    ]);
    check("ops setTrackMix/setMaster 成功", mixRes.status === 200 && mixRes.json.ok, mixRes.json);
    check("changed 含 mixer", (mixRes.json.changed || []).includes("mixer"), mixRes.json.changed);

    const after = await api("GET", `/api/projects/${pid}/arrangement`);
    const ch0 = ((after.json.mixer || {}).channels || {})[tracks[0].id] || {};
    check("通道 gain/pan/mute 落盘",
      Math.abs(ch0.gain - 0.42) < 1e-6 && Math.abs(ch0.pan + 0.5) < 1e-6 && ch0.mute === true, ch0);
    check("轨道 volume 与通道 gain 同源",
      Math.abs((after.json.tracks[0] || {}).volume - 0.42) < 1e-6, after.json.tracks[0]);
    check("主总线增益落盘", Math.abs(((after.json.mixer || {}).master || {}).gain - 0.8) < 1e-6,
      (after.json.mixer || {}).master);

    const fxRes = await ops(pid, [{ op: "setFxSlot", track: 0, slot: 3, bypass: false, wet: 0.25 }]);
    check("ops setFxSlot 成功", fxRes.status === 200 && fxRes.json.ok, fxRes.json);
    const fxArr = await api("GET", `/api/projects/${pid}/arrangement`);
    const fxSlots = (((fxArr.json.mixer || {}).channels || {})[tracks[0].id] || {}).fx || [];
    check("FX 槽数组补齐到 8 个", fxSlots.length === 8, fxSlots.length);
    check("目标槽为启用 + wet 0.25",
      fxSlots[3] && fxSlots[3].bypass === false && Math.abs(fxSlots[3].wet - 0.25) < 1e-6, fxSlots[3]);
    check("未触碰的槽保持默认旁通", fxSlots[0].bypass === true && fxSlots[0].wet === 1, fxSlots[0]);

    const masterFx = await ops(pid, [{ op: "setFxSlot", master: true, slot: 0, bypass: false, wet: 0.5 }]);
    check("主链 FX 槽可写", masterFx.status === 200 && masterFx.json.ok, masterFx.json);
    const mArr = await api("GET", `/api/projects/${pid}/arrangement`);
    const mSlots = ((mArr.json.mixer || {}).master || {}).fx || [];
    check("主链槽位落盘", mSlots[0] && mSlots[0].bypass === false && Math.abs(mSlots[0].wet - 0.5) < 1e-6, mSlots[0]);

    // 错误路径
    const badSlot = await ops(pid, [{ op: "setFxSlot", track: 0, slot: 99, wet: 0.5 }]);
    check("槽位越界被拒", badSlot.status === 400 && /槽位序号/.test(badSlot.json.detail || ""), badSlot.json);
    const badWet = await ops(pid, [{ op: "setFxSlot", track: 0, slot: 0, wet: 2 }]);
    check("wet 越界被拒", badWet.status === 400 && /wet 超出范围/.test(badWet.json.detail || ""), badWet.json);
    const badGain = await ops(pid, [{ op: "setMaster", gain: 9 }]);
    check("主增益越界被拒", badGain.status === 400 && /主总线增益/.test(badGain.json.detail || ""), badGain.json);
    const emptyOp = await ops(pid, [{ op: "setFxSlot", track: 0, slot: 0 }]);
    check("空参数被拒（至少要给一项）", emptyOp.status === 400 && /至少要给出/.test(emptyOp.json.detail || ""), emptyOp.json);

    // 撤销/重做语义：mixer 属于编排数据，整份 PUT 时不得丢字段
    const snapshot = await api("GET", `/api/projects/${pid}/arrangement`);
    const putBack = await api("PUT", `/api/projects/${pid}/arrangement`, snapshot.json);
    check("整份 PUT 回写保留 mixer", putBack.status === 200, putBack.json);
    const afterPut = await api("GET", `/api/projects/${pid}/arrangement`);
    check("回写后 FX 槽与主增益仍在",
      ((((afterPut.json.mixer || {}).channels || {})[tracks[0].id] || {}).fx || [])[3] !== undefined &&
      Math.abs(((afterPut.json.mixer || {}).master || {}).gain - 0.8) < 1e-6, afterPut.json.mixer);

    await api("DELETE", `/api/projects/${pid}`);
    /* 删除是"移出索引"语义：以项目列表为准（arrangement 文件可能仍在回收目录） */
    const listAfter = await api("GET", "/api/projects");
    const ids = ((listAfter.json.projects) || []).map((p) => p.id || (p.meta && p.meta.id));
    check("清理临时工程（列表已移除）", !ids.includes(pid), ids.length);
  }
}

console.log(failures ? `\n结果: ${failures} 项失败` : "\n结果: 全部通过");
process.exit(failures ? 1 : 0);
