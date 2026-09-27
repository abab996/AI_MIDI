/* 乐器箱（机架 P2）检查工具。
 *
 * 三部分：
 *   A. 静态契约（无需服务）：chat.html / arrange.ts / rack.ts / pianoroll.ts /
 *      chat.ts / style.css 之间"改了 TS 忘了改 HTML"这类只在运行时才炸的错位，
 *      以及 4.3 节的结构与动效/主题约束；
 *   B. 纯逻辑（VM 加载真实产物 frontend/js/rack.js）：条目 id 派生与后端
 *      Go 侧逐字一致（固定向量）、v1 音源迁移规则、音源解析、去重；
 *   C. 活体冒烟（需 MM_BASE）：对运行中的服务跑一遍 ops API 闭环
 *      （入架 → 指派轨道 → 改混音 → 钢琴窗 → 删除保护 → rev 单调）。
 *
 * 用法：
 *   node tools/dev/rack_check.mjs
 *   MM_BASE=http://127.0.0.1:7860 node tools/dev/rack_check.mjs
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
  if (ok) {
    console.log("  ok   " + name);
  } else {
    failures++;
    console.log("  FAIL " + name + (extra !== undefined ? "  → " + JSON.stringify(extra) : ""));
  }
}

/* ═══════════ A. 静态契约 ═══════════ */

console.log("[A] 静态契约");

const chatHtml = read("frontend/chat.html");
const arrangeTs = read("frontend/src/arrangement/arrange.ts");
const rackTs = read("frontend/src/arrangement/rack.ts");
const pianorollTs = read("frontend/src/pianoroll/pianoroll.ts");
const audioEngineTs = read("frontend/src/arrangement/audio_engine.ts");
const chatTs = read("frontend/src/chat.ts");
const styleCss = read("frontend/style.css");
const domainTs = read("frontend/src/types/domain.d.ts");

// A1. HTML ↔ TS 的 DOM id 对齐
const htmlIds = new Set([...chatHtml.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((m) => m[1]));
const rackIds = ["arrRackWindow", "arrRackTitlebar", "arrRackBody", "arrRackList", "arrRackStatusText",
  "arrRackFilters", "arrRackAddSourceBtn", "arrRackSearch", "arrRackCloseBtn", "arrRackCollapseBtn"];
const missingRackIds = rackIds.filter((id) => !htmlIds.has(id));
check("机架窗口的 10 个 DOM id 都在 chat.html 中", missingRackIds.length === 0, missingRackIds);

const paneRefIds = [...arrangeTs.matchAll(/getElementById\("(arr[A-Za-z0-9]+)"\)/g)].map((m) => m[1]);
const missingRefs = [...new Set(paneRefIds)].filter((id) => !htmlIds.has(id));
check("arrange.ts 引用的 arr* DOM id 都存在", missingRefs.length === 0, missingRefs);

// A2. 分类页签按 4.3 节列全（全部/合成器/SF2/VST/MM/采样）
const filterStart = chatHtml.indexOf('id="arrRackFilters"');
const filterSeg = chatHtml.slice(filterStart, chatHtml.indexOf("</div>", filterStart));
const tabs = [...filterSeg.matchAll(/data-tab="([a-z0-9]+)"/g)].map((m) => m[1]);
check("机架分类页签 = all/synth/sf2/vst3/mm/sample",
  JSON.stringify(tabs) === JSON.stringify(["all", "synth", "sf2", "vst3", "mm", "sample"]), tabs);
check("分类容器带 role=tablist", filterSeg.includes('role="tablist"'));
check("列表容器 role=listbox", /id="arrRackList"[^>]*role="listbox"/.test(chatHtml.replace(/\n/g, " ")));
check("搜索框有可访问名", chatHtml.includes('aria-label="搜索机架内音源"'));
check("「＋ 加载音源」取代旧的「＋ 导入 SF2」",
  chatHtml.includes("arrRackAddSourceBtn") && chatHtml.includes("＋ 加载音源") && !chatHtml.includes("arrRackAddSf2Btn"));

// A3. 机架窗口加载脚本
check("chat.html 加载 /js/arrangement/rack.js",
  chatHtml.includes('<script src="/js/arrangement/rack.js" defer></script>'));
check("rack.js 先于 arrange.js 加载",
  chatHtml.indexOf("rack.js") < chatHtml.indexOf("arrange.js"));

// A4. 硬编码音源清单必须消失（P2 的核心目标：音源只能来自注册表/机架）
const legacyLiterals = ["source-piano", "source-strings", "source-synth", "arrRack-badge-synth",
  "内置多波形合成器", "原声大三角钢琴", "SF2: 温暖钢琴 (Piano)"];
const remains = legacyLiterals.filter((s) => arrangeTs.includes(s) || rackTs.includes(s) || pianorollTs.includes(s));
check("机架/钢琴窗不再硬编码音源清单", remains.length === 0, remains);

// A5. v2 数据契约
check("cleanState 写出 version 2", /version:\s*2/.test(arrangeTs));
check("cleanState 写出 rack/pianoRoll/mixer",
  /rack:\s*\{ items:/.test(arrangeTs) && /pianoRoll:\s*\{/.test(arrangeTs) && /mixer:/.test(arrangeTs));
check("deserialize 内含 v1→v2 迁移", arrangeTs.includes("migrateV1Source"));
check("机架一致性兜底 normalizeRack 存在", arrangeTs.includes("normalizeRack"));
check("轨道音源只能来自机架（setTrackSource 校验）",
  arrangeTs.includes("音源不在机架中，请先从机架选择音源"));
check("domain.d.ts 定义 RackItem/ResolvedVoice",
  domainTs.includes("interface RackItem") && domainTs.includes("interface ResolvedVoice"));

// A6. ops API 与 AI 事件
check("arrangeOps 走 /arrangement/ops", arrangeTs.includes("/arrangement/ops"));
check("applyRemoteArrangement 存在（热应用不改本地 clip）",
  arrangeTs.includes("applyRemoteArrangement") && arrangeTs.includes("保留本地 clips"));
check("chat.ts 处理 SSE arrangement 事件",
  chatTs.includes('ev.type === "arrangement"') && chatTs.includes("applyArrangementEvent"));
check("SSE 事件按项目过滤且忽略旧 rev",
  chatTs.includes("ev.project !== currentProjectId") && chatTs.includes("ev.rev <= arrange.rev"));
check("AI 改动回显 ai-echo 存在", chatTs.includes("ai-echo") && styleCss.includes(".ai-echo"));

// A7. 钢琴窗与音频引擎接入机架
check("钢琴窗音色改走机架选择器（ArrRack.openPicker）", pianorollTs.includes("ArrRack.openPicker"));
check("钢琴窗音源持久化到 arrangement.pianoRoll", pianorollTs.includes("setPianoRollSource"));
check("钢琴窗引擎轨 30/31 逻辑保留",
  pianorollTs.includes("SF2_TRACK") && pianorollTs.includes("PERF_TRACK") === false || pianorollTs.includes("_engineSf2Prepare"));
check("audio_engine 的音源键改由 ArrRack 解析",
  audioEngineTs.includes("ArrRack.resolveSource") && audioEngineTs.includes("resolved.key"));
check("缺音源/插件轨在引擎侧压到零增益（不留旧音色）",
  audioEngineTs.includes("_silentVoice") && audioEngineTs.includes("nodes._silentVoice ? 0"));

// A8. CSS：令牌取色 / 只用 transform+opacity / 六态 / 双主题 / reduced-motion
const cssStart = styleCss.indexOf("编曲窗 · 乐器箱（机架 P2）");
const newCss = styleCss.slice(cssStart);
check("新增 CSS 段存在", cssStart > 0 && newCss.length > 2000, newCss.length);
check("CSS 段花括号平衡",
  (newCss.match(/\{/g) || []).length === (newCss.match(/\}/g) || []).length,
  [(newCss.match(/\{/g) || []).length, (newCss.match(/\}/g) || []).length]);
check("条目进场用 spring 曲线（4.10）", newCss.includes("@keyframes rk-in"));
check("条目移出用 scaleY 收拢（4.10）", newCss.includes("@keyframes rk-leave") && newCss.includes("scaleY"));
check("预设列滑入 sc-in-right（4.10）", newCss.includes("@keyframes sc-in-right"));
check("reduced-motion 降级覆盖新组件",
  newCss.includes("prefers-reduced-motion") && newCss.includes(".arr-rack-item.leaving"));
check("动效只用 transform/opacity（无 height 过渡）",
  !/\.(arr-rack|snd-picker|rk-)[a-z-]*[^{]*\{[^}]*transition:[^}]*height/.test(newCss));
const rawHex = [...newCss.matchAll(/^\s*[a-z-]+:\s*[^;]*#[0-9a-fA-F]{3,6}/gm)].map((m) => m[0].trim());
check("新增 CSS 段无硬编码 hex 颜色", rawHex.length === 0, rawHex.slice(0, 3));
check("六态齐备：hover / focus-visible / active / disabled / loading / empty",
  newCss.includes(":hover") && newCss.includes(":focus-visible") && newCss.includes(":active")
  && newCss.includes(".arr-rack-item.is-missing") && newCss.includes(".snd-picker-loading")
  && newCss.includes(".arr-rack-empty"));
check("等宽字体用于技术性文本", newCss.includes("Cascadia Mono"));
check("VST 紫用令牌混色（不新增色板）", newCss.includes("color-mix(in srgb, var(--color-primary)"));

// A9. 键盘可达性（4.3：↑↓ 选择 · Enter 确认 · Esc 关闭 · Tab 切页签）
check("选择器处理 ↑↓/Enter/Esc/Tab",
  rackTs.includes('"ArrowDown"') && rackTs.includes('"ArrowUp"') && rackTs.includes('"Enter"')
  && rackTs.includes('"Escape"') && rackTs.includes('"Tab"'));
check("机架条目支持 Enter 分配 / 空格试听", rackTs.includes('e.key === "Enter"') && rackTs.includes('e.key === " "'));
check("选择器行 role=option", rackTs.includes('setAttribute("role", "option")'));

/* ═══════════ B. 纯逻辑（真实产物 frontend/js/rack.js） ═══════════ */

console.log("\n[B] 纯逻辑（VM 加载 frontend/js/arrangement/rack.js）");

const sandbox = {
  console,
  setTimeout: () => 0,
  clearTimeout: () => {},
  localStorage: { getItem: () => null, setItem: () => {} },
  UI: { qsa: () => [], toast: () => {}, fmtSize: (b) => b + "B" },
  document: { createElement: () => ({ classList: { add() {}, remove() {} }, appendChild() {}, style: {} }) },
};
sandbox.window = sandbox;
sandbox.self = sandbox;
vm.createContext(sandbox);
vm.runInContext(read("frontend/js/arrangement/rack.js"), sandbox, { filename: "rack.js" });

const R = sandbox.window.ArrRack;
check("rack.js 注册 window.ArrRack", !!R);
check("kindMeta 六种 kind 齐备",
  ["synth", "sf2", "pack", "vst3", "mm", "sample"].every((k) => !!R.kindMeta(k).badge));

// B1. 条目 id：与后端 internal/project/arrangement_ops.go 的 rackItemID 逐字一致
//     固定向量（同表也在 internal/project/arrangement_ops_test.go 里断言）
const idVectors = [
  [["synth", "builtin:synth", "sawtooth", ""], "rk_3c069be59463"],
  [["synth", "builtin:synth", "square", ""], "rk_f51a10c53900"],
  [["sf2", "builtin:gm", "0:0", ""], "rk_2e9a99d4602d"],
  [["sf2", "sf2_generaluser_ab12cd", "0:48", ""], "rk_8bd68c9021c3"],
  [["sf2", "sf2_pianolib_x9", "0:0", ""], "rk_48ca2ed690d1"],
  [["mm", "mm_ai_stereo_77", "", ""], "rk_607a9773c699"],
  [["sample", "D:/Samples/Kick 808.wav", "", "D:/samples/kick 808.wav"], "rk_64f8b156d5f8"],
];
let idOk = true;
const idBad = [];
for (const [args, want] of idVectors) {
  const got = R.rackItemId(...args);
  if (got !== want) { idOk = false; idBad.push({ args, want, got }); }
}
check("rackItemId 与后端 FNV-1a 派生逐字一致（7 个向量）", idOk, idBad);

// 大小写与 UTF-8 一致性（中文/大写路径）
check("id 对大小写不敏感", R.rackItemId("sf2", "SF2_X", "0:0", "") === R.rackItemId("sf2", "sf2_x", "0:0", ""));
check("id 覆盖 UTF-8 中文音源名（不塌缩）",
  R.rackItemId("sf2", "钢琴库", "0:0", "") !== R.rackItemId("sf2", "钢琴库", "0:1", ""));

// B2. 条目工厂与 v1 迁移
const synthItem = R.synthItem("square");
check("synthItem 生成 synth 条目", synthItem.kind === "synth" && synthItem.sourceId === "builtin:synth" && synthItem.presetId === "square");
check("gmItem 生成内置 SF2 条目", R.gmItem("0:48").sourceId === "builtin:gm" && R.gmItem("0:48").presetId === "0:48");

const migSynth = R.migrateV1Source({ type: "synth", wave: "triangle", label: "合成器 · 三角波" });
check("v1 synth → 机架条目（保留波形）",
  migSynth && migSynth.kind === "synth" && migSynth.presetId === "triangle");
const migBuiltin = R.migrateV1Source({ type: "builtin", tone: "strings" });
check("v1 builtin strings → 0:48（无 GeneralUser 时回落 builtin:gm）",
  migBuiltin && migBuiltin.presetId === "0:48" && (migBuiltin.sourceId === "builtin:gm" || migBuiltin.sourceId.startsWith("sf2_")));
const migSf2 = R.migrateV1Source({ type: "sf2", libId: "abc", presetId: "1:7", label: "钢琴" });
check("v1 sf2 → 机架条目（libId 作为 sourceId 兜底）",
  migSf2 && migSf2.kind === "sf2" && migSf2.sourceId === "abc" && migSf2.presetId === "1:7");
check("已是 v2 引用的音源不重复迁移", R.migrateV1Source({ itemId: "rk_x" }) === null);

// B3. 解析（发声描述）
const items = [];
R.upsert(items, R.synthItem("sawtooth"));
const vSynth = R.resolve(items, { itemId: items[0].id, presetId: "square" });
check("synth 解析 → 波形发声", vSynth.ok && vSynth.voice.type === "synth" && vSynth.voice.wave === "square");
const vMissing = R.resolve(items, { itemId: "rk_nope", presetId: "" });
check("未知条目 → 音源缺失（不静默发错音）", !vMissing.ok && vMissing.missing && vMissing.voice.type === "none");
check("key 含条目 id + 预设（重建发声链判据）", vSynth.key === "item:" + items[0].id + ":square");

const gmItems = [];
R.upsert(gmItems, R.gmItem("0:0"));
const vGm = R.resolve(gmItems, { itemId: gmItems[0].id, presetId: "0:48" });
check("builtin:gm 解析 → builtin 发声（弦乐 program 48）",
  vGm.voice.type === "builtin" && vGm.voice.tone === "strings");

// B4. 去重
const dup = [];
const a1 = R.upsert(dup, R.synthItem("sine"));
const a2 = R.upsert(dup, R.synthItem("sine"));
check("同 kind/sourceId/preset 入架复用条目（不重复）",
  dup.length === 1 && a1.created === true && a2.created === false);

/* ═══════════ C. 活体冒烟（可选） ═══════════ */

function httpFetch(url, opts = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const req = httpRequest(
      {
        hostname: u.hostname,
        port: u.port,
        path: u.pathname + u.search,
        method: (opts.method || "GET").toUpperCase(),
        headers: { Connection: "close", ...(opts.headers || {}) },
      },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          const body = Buffer.concat(chunks).toString("utf8");
          resolve({
            ok: res.statusCode >= 200 && res.statusCode < 300,
            status: res.statusCode,
            text: async () => body,
            json: async () => JSON.parse(body),
          });
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
    const res = await httpFetch(base + p, {
      method,
      headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    return { status: res.status, json: await res.json().catch(() => ({})) };
  };

  const health = await api("GET", "/api/health");
  check("服务健康", health.status === 200, health.status);

  // 建一个临时项目跑完整 ops 闭环，跑完删除
  const created = await api("POST", "/api/projects", { name: "rack_check 临时项目" });
  const pid = created.json && (created.json.id || (created.json.meta && created.json.meta.id));
  check("创建临时项目", !!pid, created.json);
  if (!pid) {
    console.log(failures ? `\n结果: ${failures} 项失败` : "\n结果: 全部通过");
    process.exit(failures ? 1 : 0);
  }

  try {
    const sources = await api("GET", "/api/sound/sources?kind=sf2");
    const sf2 = (sources.json.sources || [])[0];
    check("注册表可列举 SF2 音源", !!sf2, (sources.json.sources || []).length);

    const ops = async (list) => api("POST", `/api/projects/${pid}/arrangement/ops`, { ops: list, client: "ui" });

    // 1) 入架（用内置合成器，保证不依赖磁盘音色库）
    const r1 = await ops([{ op: "addRackItem", sourceId: "builtin:synth", presetId: "square" }]);
    check("ops: 入架成功", r1.status === 200 && r1.json.rev === 1, r1.json);
    const arr1 = r1.json.arrangement || {};
    const rackItems = ((arr1.rack || {}).items) || [];
    check("ops: 返回完整编排数据（含机架）", rackItems.length >= 1, rackItems.length);
    const item = rackItems.find((it) => it.presetId === "square") || rackItems[0];
    check("ops: 条目 id 与前端算法可对齐", typeof item.id === "string" && item.id.startsWith("rk_"), item.id);
    check("ops: changed 含 rack", (r1.json.changed || []).includes("rack"), r1.json.changed);

    // 2) 指派轨道 + 混音
    const r2 = await ops([
      { op: "setTrackSource", track: 0, itemId: item.id, presetId: "square" },
      { op: "setTrackMix", track: 0, gain: 0.55, pan: -0.2, mute: false },
    ]);
    check("ops: 指派音源 + 混音成功", r2.status === 200, r2.json);
    const t0 = (r2.json.arrangement.tracks || [])[0] || {};
    check("ops: 轨道 source = 机架引用",
      t0.source && t0.source.itemId === item.id && t0.source.presetId === "square", t0.source);
    check("ops: 混音写入 mixer.channels",
      t0.id && r2.json.arrangement.mixer.channels[t0.id] &&
      Math.abs(r2.json.arrangement.mixer.channels[t0.id].pan + 0.2) < 1e-6,
      r2.json.arrangement.mixer);

    // 3) 新增轨道 + 钢琴窗
    const r3 = await ops([
      { op: "arrangeTrack", op: "addTrack", name: "检查轨" },
      { op: "setPianoRoll", itemId: item.id, presetId: "square" },
    ].map((o, i) => (i === 0 ? { op: "addTrack", name: "检查轨" } : o)));
    check("ops: 新增轨道 + 钢琴窗音源成功", r3.status === 200, r3.json);
    check("ops: pianoRoll 落盘",
      r3.json.arrangement.pianoRoll && r3.json.arrangement.pianoRoll.itemId === item.id,
      r3.json.arrangement.pianoRoll);

    // 4) rev 单调
    check("ops: rev 单调递增", r3.json.rev > r2.json.rev && r2.json.rev > r1.json.rev,
      [r1.json.rev, r2.json.rev, r3.json.rev]);

    // 5) 删除保护
    const r4 = await ops([{ op: "removeRackItem", itemId: item.id }]);
    check("ops: 被占用条目拒绝删除并说明原因",
      r4.status === 400 && /仍被/.test(r4.json.detail || ""), r4.json);

    // 6) 整份 GET 与 PUT 的 rev 序列
    const got = await api("GET", `/api/projects/${pid}/arrangement`);
    check("GET 返回 v2 数据", got.status === 200 && got.json.version === 2, got.json.version);
    const put = await api("PUT", `/api/projects/${pid}/arrangement`, got.json);
    check("PUT 推进 rev", put.status === 200 && put.json.rev > r3.json.rev, put.json);

    // 7) 非法操作
    const bad = await ops([{ op: "explode" }]);
    check("非法操作被拒绝（可解释）", bad.status === 400 && /未知操作/.test(bad.json.detail || ""), bad.json);

    // 8) v1 老工程：服务端 ops 触发 v1→v2 迁移（AI 在工程未被前端打开时也要能操作）
    const v1 = {
      version: 1,
      bpm: 96,
      snap: 0.25,
      ppb: 26,
      loop: { on: false, start: 0, end: 16 },
      tracks: [
        { id: "tr_v1a", name: "旧轨 1", color: "#00B8CC", volume: 0.8, mute: false, solo: false,
          source: { type: "synth", wave: "triangle", label: "合成器 · 三角波" },
          clips: [{ id: "c1", type: "midi", name: "m", start: 0, length: 4, notes: [{ note: "C4", velocity: 100, start: 0, end: 1 }] }] },
        { id: "tr_v1b", name: "旧轨 2", color: "#FF8C00", volume: 0.7, mute: false, solo: false,
          source: { type: "builtin", tone: "strings", label: "内置 · 弦乐群" }, clips: [] },
      ],
    };
    const putV1 = await api("PUT", `/api/projects/${pid}/arrangement`, v1);
    check("写入 v1 编排数据（老工程）", putV1.status === 200, putV1.json);

    const migrated = await ops([{ op: "setTrackMix", track: 0, gain: 0.7 }]);
    check("ops 对 v1 工程自动迁移", migrated.status === 200, migrated.json);
    const mArr = migrated.json.arrangement || {};
    check("迁移后 version=2", mArr.version === 2, mArr.version);
    const mRack = (mArr.rack || {}).items || [];
    check("迁移生成机架条目（synth 保留波形 + builtin 弦乐 → 0:48）",
      mRack.length === 2
      && mRack.some((it) => it.kind === "synth" && it.presetId === "triangle")
      && mRack.some((it) => it.kind === "sf2" && it.presetId === "0:48"),
      mRack);
    const mTracks = mArr.tracks || [];
    check("迁移后轨道只留 {itemId,presetId} 引用",
      mTracks.length === 2 && mTracks.every((t) => t.source && t.source.itemId && !t.source.type),
      mTracks.map((t) => t.source));
    check("迁移保留 clips/notes 原样",
      (mTracks[0].clips || []).length === 1 && (mTracks[0].clips[0].notes || []).length === 1,
      mTracks[0].clips);

    // 9) 静态产物可达（前端真的能加载 rack.js）
    const asset = await api("GET", "/js/arrangement/rack.js");
    check("静态产物 /js/arrangement/rack.js 可达", asset.status === 200, asset.status);
  } finally {
    await api("DELETE", `/api/projects/${pid}`);
  }
}

console.log(failures ? `\n结果: ${failures} 项失败` : "\n结果: 全部通过");
process.exit(failures ? 1 : 0);
