/* 素材库三页签（P3）检查工具。
 *
 * 三部分：
 *   A. 静态契约（无需服务）：chat.html ↔ arrange.ts/material.ts 的 DOM id 对齐、
 *      4.4 节结构约定（三页签 / 跨页签搜索 / 目录入口 / 提示行）、
 *      CSS 令牌取色与动效降级、键盘可达；
 *   B. 纯逻辑（VM 加载真实产物 frontend/js/arrangement/material.js）：
 *      节点键 / 绝对路径拼接 / 时长格式化 / 峰值计算 / 采样包归属；
 *   C. 活体冒烟（需 MM_BASE）：目录注册 → 采样包登记 → 列表接口 → 移除。
 *
 * 浏览器级行为（波形真的画出来、页签切换、卡片分配）由
 * tools/dev/arrange_ui_check.mjs 覆盖。
 *
 * 用法：
 *   node tools/dev/material_check.mjs
 *   MM_BASE=http://127.0.0.1:7860 node tools/dev/material_check.mjs
 */
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import os from "node:os";
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
const arrangeTs = read("frontend/src/arrangement/arrange.ts");
const materialTs = read("frontend/src/arrangement/material.ts");
const styleCss = read("frontend/style.css");
const stateTs = read("frontend/src/types/state.d.ts");
const windowTs = read("frontend/src/types/window.d.ts");

const htmlIds = new Set([...chatHtml.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((m) => m[1]));
for (const id of ["matTabs", "matSearch", "matBody", "matHint", "arrAddDirBtn"]) {
  check(`chat.html 含 #${id}`, htmlIds.has(id));
}

const refIds = [...materialTs.matchAll(/getElementById\("([A-Za-z0-9_-]+)"\)/g)].map((m) => m[1]);
const missing = [...new Set(refIds)].filter((id) => !htmlIds.has(id));
check("material.ts 引用的 DOM id 都存在", missing.length === 0, missing);

check("chat.html 加载 /js/arrangement/material.js", chatHtml.includes('src="/js/arrangement/material.js"'));
check("material.js 先于 arrange.js 加载",
  chatHtml.indexOf("material.js") < chatHtml.indexOf("arrange.js"));

const tabsSeg = chatHtml.slice(chatHtml.indexOf('id="matTabs"'), chatHtml.indexOf("</div>", chatHtml.indexOf('id="matTabs"')));
const tabIds = [...tabsSeg.matchAll(/data-tab="([a-z]+)"/g)].map((m) => m[1]);
check("三页签 = 采样/音源/效果器", JSON.stringify(tabIds) === JSON.stringify(["sample", "source", "fx"]), tabIds);
check("页签容器 role=tablist", tabsSeg.includes('role="tablist"'));
check("内容容器 role=tabpanel", /id="matBody"[^>]*role="tabpanel"/.test(chatHtml.replace(/\n/g, " ")));
check("搜索框有可访问名", chatHtml.includes('aria-label="搜索素材"'));

check("旧 ids（arrFileTree）已退场", !chatHtml.includes('id="arrFileTree"'));
check("素材逻辑抽到 material.ts（arrange.ts 只留委托）",
  materialTs.includes("window.ArrMaterial = {") && arrangeTs.includes("window.ArrMaterial.render(this)"));
check("arrange.ts 不再直接遍历素材树", !/Arrange\.prototype\.appendTreeNode\s*=/.test(arrangeTs));
check("window.d.ts 声明 ArrMaterialApi", windowTs.includes("interface ArrMaterialApi") && windowTs.includes("ArrMaterial: ArrMaterialApi"));

// 4.4 规格要点
check("页签切换内容上滑淡入（0.22s）", materialTs.includes('classList.add("mat-in")') && styleCss.includes("@keyframes mat-in"));
check("条目 Stagger 30ms", materialTs.includes("0.03 + \"s\""));
check("波形缩略图尺寸 48×22", materialTs.includes("var THUMB_W = 48") && materialTs.includes("var THUMB_H = 22"));
check("波形按主题令牌取色", materialTs.includes('getPropertyValue("--color-primary")'));
check("波形分析不进发声链（离屏解码）", materialTs.includes("analysisContext()") && materialTs.includes("decodeAudioData"));
check("进视口才解码（IntersectionObserver）", materialTs.includes("new IntersectionObserver"));
check("展开态用 localStorage 记忆", materialTs.includes("melodymate-material-tree"));
check("超量文件折叠（>100 条给显示更多）", materialTs.includes("MAX_CHILDREN = 100") && materialTs.includes("显示更多"));
check("音源卡片带 [已在架]/[＋ 入架] 胶囊", materialTs.includes('"已在架" : "＋ 入架"'));
check("音源页签接机架分配", materialTs.includes("assignRackItem"));
check("效果器页签引导到混音台装载（不再是 P4/P5 占位文案）",
  materialTs.includes("效果器装载到混音台 FX 槽") &&
  !materialTs.includes("P5 VST3 宿主就绪后可用"));
check("拖到轨道=分配并自动入架（载荷带 sourceId）", materialTs.includes("sourceId: src.id") &&
  arrangeTs.includes("先入架再分配"));

const keyHandlers = ['e.key === "Enter"', 'e.key === " "', 'e.key === "ArrowLeft"', 'e.key === "ArrowRight"', '"Escape"'];
check("键盘可达（Enter/Space/←→/Esc）", keyHandlers.every((k) => materialTs.includes(k)));

// CSS 约束
const cssStart = styleCss.indexOf("素材库三页签（P3，4.4 节规格）");
const matCss = styleCss.slice(cssStart);
check("新增 CSS 段存在", cssStart > 0 && matCss.length > 2000, matCss.length);
check("CSS 段花括号平衡",
  (matCss.match(/\{/g) || []).length === (matCss.match(/\}/g) || []).length,
  [(matCss.match(/\{/g) || []).length, (matCss.match(/\}/g) || []).length]);
const rawHex = [...matCss.matchAll(/^\s*[a-z-]+:\s*[^;]*#[0-9a-fA-F]{3,6}/gm)].map((m) => m[0].trim());
check("新增 CSS 无硬编码 hex 颜色", rawHex.length === 0, rawHex.slice(0, 3));
check("动效只用 transform/opacity（无 height 过渡）",
  !/\.mat-[a-z-]+[^{]*\{[^}]*transition:[^}]*height/.test(matCss));
check("reduced-motion 降级覆盖新组件",
  matCss.includes("prefers-reduced-motion") && matCss.includes(".mat-body.mat-in"));
check("六态齐备（hover/focus-visible/active/missing/empty/dragging）",
  matCss.includes(":hover") && matCss.includes(":focus-visible") && matCss.includes(":active") &&
  matCss.includes(".mat-wave-missing") && matCss.includes(".mat-empty") && matCss.includes(".dragging"));
check("等宽字体用于技术性文本", matCss.includes("Cascadia Mono"));

/* ═══════════ B. 纯逻辑（真实产物） ═══════════ */

console.log("\n[B] 纯逻辑（VM 加载 frontend/js/arrangement/material.js）");

const sandbox = {
  console,
  setTimeout: () => 0,
  clearTimeout: () => {},
  localStorage: { getItem: () => null, setItem: () => {} },
  sessionStorage: { getItem: () => null, setItem: () => {} },
  UI: {
    qsa: () => [], toast: () => {}, fmtSize: (b) => (b >= 1024 ? Math.round(b / 1024) + "KB" : b + "B"),
    getJSON: () => Promise.resolve({}), postJSON: () => Promise.resolve({}), delJSON: () => Promise.resolve({}),
  },
  document: { createElement: () => ({ classList: { add() {}, remove() {}, toggle() {} }, style: {}, appendChild() {}, setAttribute() {} }), getElementById: () => null },
  IntersectionObserver: function () { this.observe = () => {}; this.unobserve = () => {}; },
  fetch: () => Promise.resolve({ ok: false, status: 404 }),
};
sandbox.window = sandbox;
sandbox.self = sandbox;
vm.createContext(sandbox);
vm.runInContext(read("frontend/js/arrangement/material.js"), sandbox, { filename: "material.js" });

const M = sandbox.window.ArrMaterial;
check("material.js 注册 window.ArrMaterial", !!M);
check("页签表三态", JSON.stringify(M.TABS.map((t) => t.id)) === JSON.stringify(["sample", "source", "fx"]));
check("nodeKey 拼目录与子路径", M.nodeKey("D:/a", "") === "D:/a" && M.nodeKey("D:/a", "sub/x") === "D:/a/sub/x");
check("joinAbs 生成绝对路径", M.joinAbs("D:/a", "sub", "k.wav") === "D:/a/sub/k.wav");
check("joinAbs 兼容无子目录", M.joinAbs("D:/a", "", "k.wav") === "D:/a/k.wav");
check("fmtDuration 输出 m:ss.s", M.fmtDuration(0.3) === "0:00.3" && M.fmtDuration(65.4) === "1:05.4", [M.fmtDuration(0.3), M.fmtDuration(65.4)]);
check("fmtDuration 未知时长给占位", M.fmtDuration(undefined) === "—");

// 峰值计算：直流满幅应得出接近 1 的峰；静音应接近 0
function fakeBuffer(data) {
  return { getChannelData: () => Float32Array.from(data), duration: data.length / 44100, sampleRate: 44100, numberOfChannels: 1 };
}
const loud = M.computePeaks(fakeBuffer(new Array(4800).fill(0.9)));
const quiet = M.computePeaks(fakeBuffer(new Array(4800).fill(0)));
check("computePeaks 输出 48 个桶（对应 48px 缩略图）", loud.length === 48, loud.length);
check("computePeaks 满幅→峰值≈0.9", Math.abs(Math.max(...loud) - 0.9) < 0.01, Math.max(...loud));
check("computePeaks 静音→0", Math.max(...quiet) === 0);
const ramp = M.computePeaks(fakeBuffer(Array.from({ length: 4800 }, (_, i) => i / 4800)));
check("computePeaks 递增信号单调不减", ramp.every((v, i) => i === 0 || v >= ramp[i - 1] - 1e-6));

check("hintFor 三页签各有提示语",
  M.hintFor("sample").includes("音频 Clip") && M.hintFor("source").includes("轨道") && M.hintFor("fx").includes("FX"));

// 采样包归属：用户自建优先于内置「默认素材」
M.state.packs = [
  { name: "默认素材", origin: "builtin", dirs: ["D:/a"] },
  { name: "我的鼓组", origin: "user", dirs: ["D:/a"] },
];
check("packNameFor 同目录优先显示用户自建采样包", M.packNameFor("D:/a") === "我的鼓组", M.packNameFor("D:/a"));
M.state.packs = [{ name: "默认素材", origin: "builtin", dirs: ["D:/x"] }];
check("packNameFor 无用户包时回落内置默认素材", M.packNameFor("D:/x") === "默认素材");
check("packNameFor 未登记目录返回空", M.packNameFor("D:/nope") === "");
check("packNameFor 大小写不敏感（Windows 语义）", M.packNameFor("d:/a") === M.packNameFor("D:/A"));

/* ═══════════ C. 活体冒烟（可选） ═══════════ */

function httpFetch(url, opts = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    /* 显式给 Content-Length：Node 默认对写出的 body 用 chunked，
       而 Go 侧 handler 在这种情形下读不到 DELETE 的请求体（浏览器 fetch
       发字符串 body 时带的是 Content-Length，这里对齐浏览器行为） */
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

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mm-mat-check-"));
  fs.writeFileSync(path.join(dir, "kick_808.wav"), Buffer.alloc(128));
  fs.writeFileSync(path.join(dir, "notes.txt"), "not audio");

  const added = await api("POST", "/api/arrangement/dirs", { path: dir });
  check("登记素材目录", added.status === 200 && (added.json.dirs || []).includes(dir), added.json);

  const listed = await api("GET", "/api/arrangement/files?dir=" + encodeURIComponent(dir));
  check("列举目录内容（音频白名单过滤）",
    listed.status === 200 && (listed.json.files || []).length === 1 &&
    listed.json.files[0].name === "kick_808.wav", listed.json);

  const audio = await api("GET", "/api/arrangement/audio?p=" + encodeURIComponent(path.join(dir, "kick_808.wav")));
  check("提供音频字节流（波形解码来源）", audio.status === 200, audio.status);

  const outside = await api("GET", "/api/arrangement/audio?p=" + encodeURIComponent(path.join(os.tmpdir(), "evil.wav")));
  check("越界路径被拒（不在已注册目录内）", outside.status === 403 || outside.status === 400, outside.status);

  const pack = await api("POST", "/api/sound/sources", { kind: "samplepack", name: "检查用采样包", dirs: [dir] });
  check("登记为采样包（供分组标签）", pack.status === 200 && pack.json.source, pack.json);

  const dirsAfter = await api("GET", "/api/arrangement/dirs");
  check("目录镜像同步到素材树", (dirsAfter.json.dirs || []).includes(dir), dirsAfter.json);

  if (pack.json && pack.json.source) await api("DELETE", `/api/sound/sources/${pack.json.source.id}`);
  const removed = await api("DELETE", "/api/arrangement/dirs", { path: dir });
  check("移除素材目录", removed.status === 200 && !(removed.json.dirs || []).includes(dir), removed.json);
  try { fs.rmSync(dir, { recursive: true, force: true }); } catch {}
}

console.log(failures ? `\n结果: ${failures} 项失败` : "\n结果: 全部通过");
process.exit(failures ? 1 : 0);
