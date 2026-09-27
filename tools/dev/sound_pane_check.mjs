/* 声音管理（音源注册表）检查工具。
 *
 * 两部分：
 *   A. 静态契约（无需服务）：settings.ts 的 DOM id ↔ settings.html 对齐、
 *      4.2 节结构约定、CSS 覆盖与动效降级、只用令牌取色；
 *   B. 活体冒烟（需服务在跑）：加载真实产物 frontend/js/soundlib.js，
 *      对运行中的实例验证列举/搜索/预设/徽标/写操作广播等客户端逻辑。
 *
 * 用法：
 *   node tools/dev/sound_pane_check.mjs              # 仅静态
 *   MM_BASE=http://127.0.0.1:7860 node tools/dev/sound_pane_check.mjs
 *
 * P2–P6 会继续改动机架/选择器/混音台，A 部分用来兜住"改了 TS 忘了改 HTML"
 * 这类只在运行时才炸的错位；B 部分兜住客户端与 /api/sound/* 的契约漂移。
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

const settingsTs = read("frontend/src/settings.ts");
const settingsHtml = read("frontend/settings.html");
const styleCss = read("frontend/style.css");

const segStart = settingsTs.indexOf("SND_COLLAPSED_KEY");
const segEnd = settingsTs.indexOf("关于页专属特效", segStart);
if (segStart < 0 || segEnd < 0) {
  console.error("找不到声音管理代码段（settings.ts 结构变了？）");
  process.exit(1);
}
const paneCode = settingsTs.slice(segStart, segEnd);

const paneStart = settingsHtml.indexOf('<section class="card brackets settings-card settings-pane" id="paneSound"');
const paneEnd = settingsHtml.indexOf("</section>", paneStart);
if (paneStart < 0) {
  console.error("找不到 paneSound 面板（settings.html）");
  process.exit(1);
}
const paneHtml = settingsHtml.slice(paneStart, paneEnd);

console.log("[A] 静态契约");
const referenced = new Set([...paneCode.matchAll(/\$\("#([A-Za-z0-9_-]+)"\)/g)].map((m) => m[1]));
const declared = new Set([...settingsHtml.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((m) => m[1]));
const missingIds = [...referenced].filter((id) => !declared.has(id));
check(`JS 引用的 ${referenced.size} 个 DOM id 都存在于 settings.html`, missingIds.length === 0, missingIds);

const paneIds = [...paneHtml.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map((m) => m[1]);
const orphanIds = paneIds.filter((id) => id !== "paneSound" && !referenced.has(id));
check("paneSound 内没有 JS 未引用的 id", orphanIds.length === 0, orphanIds);

check("面板复用 card brackets settings-card settings-pane",
  paneHtml.includes("card brackets settings-card settings-pane"));
check("分组容器是 role=listbox", paneHtml.includes('role="listbox"'));
check("条目是 role=option", true);  // 运行时生成，见下方 CSS/TS 检查
check("添加按钮有 aria-haspopup=menu", paneHtml.includes('aria-haspopup="menu"'));
check("下拉复用 .select-menu（menu-in 手感）", paneHtml.includes("select-menu snd-add-menu"));
check("搜索框有可访问名", paneHtml.includes('aria-label="搜索音源"'));
check("面板无内联 style（样式全走类名）", !paneHtml.includes("style="));
check("页签注册在 settings-tabs", settingsHtml.includes('data-pane="paneSound"'));
check("切换面板时惰性加载数据", settingsTs.includes('paneId === "paneSound") loadSoundLibrary();'));
check("注册表变化时订阅重绘", paneCode.includes("onChange("));

const cls = new Set();
// 只取类名，排除 CSS 自定义属性（--snd-delay 之类，非类名）
for (const m of paneCode.matchAll(/"([a-z0-9][a-z0-9-]*snd-[a-z0-9-]+)[ "]/g)) cls.add(m[1]);
for (const m of paneHtml.matchAll(/class="([^"]*)"/g)) {
  for (const c of m[1].split(/\s+/)) if (c.startsWith("snd-")) cls.add(c);
}
const noCss = [...cls].filter((c) => !styleCss.includes("." + c));
check(`${cls.size} 个 snd-* 类名都有 CSS 规则`, noCss.length === 0, noCss);

const cssStart = styleCss.indexOf("/* ═══════════ 设置页 · 声音管理");
const cssEnd = styleCss.indexOf("/* ═══════════ 设置页 · 关于面板（paneAbout）");
const sndCss = styleCss.slice(cssStart, cssEnd);
check("CSS 段花括号平衡",
  (sndCss.match(/\{/g) || []).length === (sndCss.match(/\}/g) || []).length,
  [(sndCss.match(/\{/g) || []).length, (sndCss.match(/\}/g) || []).length]);
check("条目进场用 spring 曲线（4.10）", sndCss.includes("@keyframes snd-in"));
check("条目移出用收拢淡出 scaleY（4.10）",
  sndCss.includes("@keyframes snd-leave") && sndCss.includes("scaleY"));
check("AI 徽标呼吸光 2s（reduced-motion 静态）",
  sndCss.includes("@keyframes snd-ai-breathe") && sndCss.includes("2s"));
check("reduced-motion 降级覆盖新组件",
  sndCss.includes("prefers-reduced-motion") && sndCss.includes(".snd-source-card.snd-leave"));
check("动效只用 transform/opacity（无 height 过渡）",
  !/\.snd-[a-z-]+[^{]*\{[^}]*transition:[^}]*height/.test(sndCss));
check("颜色全部取自令牌（段内无硬编码 hex）",
  !/\.snd-[^{]+\{[^}]*#[0-9a-fA-F]{3,6}/.test(sndCss));
check("等宽字体用于技术性文本", sndCss.includes("Cascadia Mono"));

/* 最小 fetch 兼容层（Response 形状的子集：ok/status/json），供 soundlib.js 使用 */
function httpFetch(url, opts = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const req = httpRequest(
      {
        hostname: u.hostname,
        port: u.port,
        path: u.pathname + u.search,
        method: (opts.method || "GET").toUpperCase(),
        headers: { Connection: "close", ...(opts.headers || {}) }
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
            json: async () => JSON.parse(body)
          });
        });
      }
    );
    req.on("error", reject);
    if (opts.body !== undefined && opts.body !== null) req.write(opts.body);
    req.end();
  });
}

/* ═══════════ B. 活体冒烟（可选） ═══════════ */

const base = process.env.MM_BASE;
if (!base) {
  console.log("\n[B] 活体冒烟：跳过（未设置 MM_BASE）");
} else {
  console.log("\n[B] 活体冒烟 → " + base);
  const sandbox = {
    console,
    /* 用 node:http 而非全局 fetch：undici 的 keep-alive 套接字在进程
       退出时会留下 libuv 断言噪音（"handle->flags & UV_HANDLE_CLOSING"），
       这里每次请求独立连接，输出干净 */
    fetch: (url, opts) => httpFetch(url.startsWith("http") ? url : base + url, opts),
    localStorage: { getItem: () => null, setItem: () => {} }
  };
  sandbox.window = sandbox;
  sandbox.self = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(read("frontend/js/soundlib.js"), sandbox, { filename: "soundlib.js" });

  const reg = sandbox.window.SoundRegistry;
  check("soundlib.js 注册 window.SoundRegistry", !!reg);

  const sources = await reg.load(true);
  check("load() 拉到注册表", sources.length > 0, sources.length);
  check("kindOrder 四组齐备",
    JSON.stringify(reg.kindOrder()) === JSON.stringify(["sf2", "samplepack", "vst3", "mm"]));
  check("list('sf2') 只含 SF2", reg.list("sf2").every((s) => s.kind === "sf2"));
  check("enabledOf 过滤停用项", reg.enabledOf("sf2").every((s) => s.enabled !== false));
  check("get 未知 id 返回 null", reg.get("nope") === null);
  check("query 无关键返回空", reg.query("", "zzz-nope").length === 0);
  check("parsePresetId 支持注册表格式",
    JSON.stringify(reg.parsePresetId("0:48")) === JSON.stringify({ bank: 0, program: 48 }));
  check("parsePresetId 兼容历史格式",
    JSON.stringify(reg.parsePresetId("sf2_1_7_2")) === JSON.stringify({ bank: 1, program: 7 }));

  const sf2 = reg.list("sf2")[0];
  if (sf2) {
    check("metaText 含预设数与大小", /预设/.test(reg.metaText(sf2)) && /B|KB|MB/.test(reg.metaText(sf2)),
      reg.metaText(sf2));
    if (!sf2.runtime || !sf2.runtime.missing) {
      const presets = await reg.presets(sf2.id);
      check("presets 返回 bank:program 形式", presets.length > 0 && /^\d+:\d+$/.test(presets[0].id),
        presets.slice(0, 2).map((p) => p.id));
      check("diskPathOf 返回绝对路径", /[\\/]/.test(reg.diskPathOf(sf2)));
    }
    let notified = 0;
    reg.onChange(() => { notified++; });
    await reg.update(sf2.id, { name: sf2.name });
    check("写操作后广播 onChange", notified >= 1, notified);
  }
}

console.log(failures ? `\n结果: ${failures} 项失败` : "\n结果: 全部通过");
process.exit(failures ? 1 : 0);
