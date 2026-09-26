/* 生成 frontend/src/brand-icons.ts：供应商与模型的品牌图标。
 *
 * 数据源：@lobehub/icons-static-svg（MIT）——一套专门收集 AI 供应商/模型
 * 品牌标识的图标库。品牌标识本身是各家公司的商标，这里只用于"标明这是哪家
 * 服务"，不表示任何隶属或背书关系。
 *
 * 用法：node tools/dev/gen-brand-icons.mjs
 * 需要联网（从 npm registry 拉指定版本的 tarball）。改图标清单或升级图标库
 * 版本时改下面的 ICONS / PRESETS / MODELS 再跑一次即可。
 *
 * 产物是普通的经典脚本（IIFE + 挂 window.BrandIcons），不能用 import/export
 * ——emit-frontend.mjs 会把含 import/export 的文件判为不可用。
 */
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const VERSION = "1.95.1";
const TARBALL = `https://registry.npmjs.org/@lobehub/icons-static-svg/-/icons-static-svg-${VERSION}.tgz`;

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const outFile = path.join(repoRoot, "frontend", "src", "brand-icons.ts");

/* 品牌 id → icons-static-svg 里的文件名（不含 .svg）。
   有 -color 变体的优先用彩色版；只有单色版的用单色版（fill=currentColor，
   跟随主题文字色，深浅两套主题都看得见）。 */
const ICONS = {
  deepseek: "deepseek-color",
  openai: "openai",
  anthropic: "anthropic",
  claude: "claude-color",
  gemini: "gemini-color",
  google: "google-color",
  xai: "xai",
  grok: "grok",
  mistral: "mistral-color",
  groq: "groq",
  cohere: "cohere-color",
  moonshot: "moonshot",
  kimi: "kimi-color",
  zhipu: "zhipu-color",
  chatglm: "chatglm-color",
  qwen: "qwen-color",
  alibaba: "alibaba-color",
  volcengine: "volcengine-color",
  bytedance: "bytedance-color",
  minimax: "minimax-color",
  stepfun: "stepfun-color",
  baidu: "baidu-color",
  wenxin: "wenxin-color",
  siliconcloud: "siliconcloud-color",
  openrouter: "openrouter-color",
  together: "together-color",
  fireworks: "fireworks-color",
  cerebras: "cerebras-color",
  sambanova: "sambanova-color",
  deepinfra: "deepinfra-color",
  novita: "novita-color",
  nebius: "nebius",
  huggingface: "huggingface-color",
  nvidia: "nvidia-color",
  featherless: "featherless-color",
  chutes: "chutes",
  perplexity: "perplexity-color",
  ollama: "ollama",
  lmstudio: "lmstudio",
  vllm: "vllm-color",
  meta: "meta-color",
};

/* 供应商预设 id → 品牌 id（预设见 internal/config/presets.go）。
   没列进来的（llama.cpp 这类图标库没有的）走首字母方块兜底。 */
const PRESETS = {
  deepseek: "deepseek",
  openai: "openai",
  anthropic: "anthropic",
  gemini: "gemini",
  xai: "xai",
  mistral: "mistral",
  groq: "groq",
  cohere: "cohere",
  moonshot: "moonshot",
  "moonshot-intl": "moonshot",
  zhipu: "zhipu",
  dashscope: "alibaba",
  "dashscope-intl": "alibaba",
  volcengine: "volcengine",
  minimax: "minimax",
  "minimax-intl": "minimax",
  stepfun: "stepfun",
  qianfan: "baidu",
  siliconflow: "siliconcloud",
  openrouter: "openrouter",
  together: "together",
  fireworks: "fireworks",
  cerebras: "cerebras",
  sambanova: "sambanova",
  deepinfra: "deepinfra",
  novita: "novita",
  nebius: "nebius",
  huggingface: "huggingface",
  nvidia: "nvidia",
  featherless: "featherless",
  chutes: "chutes",
  perplexity: "perplexity",
  ollama: "ollama",
  lmstudio: "lmstudio",
  vllm: "vllm",
};

/* 模型 ID 前缀 → 品牌 id。顺序即优先级（先匹配上的赢），长前缀写在前面。
   o1/o3/o4 这类短前缀在运行时还会做边界检查（见 brand-icons.ts 的 matchBrand），
   不会把 "o1x" 之类误判成 OpenAI。 */
const MODEL_PREFIXES = [
  ["gpt-", "openai"],
  ["chatgpt-", "openai"],
  ["o1", "openai"],
  ["o3", "openai"],
  ["o4", "openai"],
  ["claude-", "claude"],
  ["gemini-", "gemini"],
  ["deepseek-", "deepseek"],
  ["grok-", "grok"],
  ["qwen", "qwen"],
  ["glm-", "zhipu"],
  ["chatglm", "zhipu"],
  ["moonshot-", "moonshot"],
  ["kimi", "kimi"],
  ["mistral-", "mistral"],
  ["minimax", "minimax"],
  ["abab", "minimax"],
  ["llama-", "meta"],
  ["command-", "cohere"],
  ["hunyuan", "bytedance"],
  ["doubao", "bytedance"],
  ["ernie", "baidu"],
  ["step-", "stepfun"],
  ["sonar", "perplexity"],
];

/* 图标库的 svg 带 height/width="1em" 和 style，尺寸交给 CSS 控制；
   <title> 去掉（旁边就是名字，且会让浏览器弹多余 tooltip）。 */
function normalize(svg) {
  return svg
    .replace(/<title>[\s\S]*?<\/title>/g, "")
    .replace(/\s(?:height|width|style)="[^"]*"/g, "")
    .replace("<svg ", '<svg aria-hidden="true" focusable="false" ')
    .replace(/\s+/g, " ")
    .replace(/> </g, "><")
    .trim();
}

async function fetchIcons() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "lobe-icons-"));
  const res = await fetch(TARBALL);
  if (!res.ok) throw new Error(`下载图标库失败: HTTP ${res.status}`);
  fs.writeFileSync(path.join(dir, "icons.tgz"), Buffer.from(await res.arrayBuffer()));
  /* 用相对路径 + cwd 调 tar：Windows 的 tar 会把 "C:\..." 里的冒号当成
     远程主机名（Cannot connect to C），传绝对路径必然失败 */
  const untar = spawnSync("tar", ["-xzf", "icons.tgz"], { cwd: dir, encoding: "utf8" });
  if (untar.status !== 0) throw new Error("解压图标库失败: " + (untar.stderr || untar.error));
  return path.join(dir, "package", "icons");
}

function main() {
  return fetchIcons().then((iconsDir) => {
    const entries = [];
    for (const [brand, file] of Object.entries(ICONS)) {
      const p = path.join(iconsDir, file + ".svg");
      if (!fs.existsSync(p)) throw new Error(`图标库缺少 ${file}.svg（品牌 ${brand}）`);
      entries.push([brand, normalize(fs.readFileSync(p, "utf8"))]);
    }

    const lines = [];
    lines.push("/* 生成文件，请勿手改 —— 重新生成：node tools/dev/gen-brand-icons.mjs");
    lines.push(" *");
    lines.push(` * 品牌图标来自 @lobehub/icons-static-svg v${VERSION}（MIT）。`);
    lines.push(" * 图标是各家公司的商标，此处仅用于标明对应的服务来源。");
    lines.push(" * 单色图标用 fill=currentColor，跟随主题文字色；彩色图标保留品牌配色。 */");
    lines.push("(function () {");
    lines.push('  "use strict";');
    lines.push("");
    lines.push("  var ICONS: { [k: string]: string } = {");
    for (const [brand, svg] of entries) {
      lines.push(`    ${JSON.stringify(brand)}: ${JSON.stringify(svg)},`);
    }
    lines.push("  };");
    lines.push("");
    lines.push("  /* 供应商预设 id → 品牌 id */");
    lines.push("  var PRESETS: { [k: string]: string } = {");
    for (const [preset, brand] of Object.entries(PRESETS)) {
      lines.push(`    ${JSON.stringify(preset)}: ${JSON.stringify(brand)},`);
    }
    lines.push("  };");
    lines.push("");
    lines.push("  /* 模型 ID 前缀 → 品牌 id（顺序即优先级） */");
    lines.push("  var MODEL_PREFIXES: Array<[string, string]> = [");
    for (const [prefix, brand] of MODEL_PREFIXES) {
      lines.push(`    [${JSON.stringify(prefix)}, ${JSON.stringify(brand)}],`);
    }
    lines.push("  ];");
    lines.push("");
    lines.push(RUNTIME);
    lines.push("})();");

    fs.writeFileSync(outFile, lines.join("\n") + "\n");
    const size = fs.statSync(outFile).size;
    console.log(`gen-brand-icons: ${entries.length} 个品牌 → ${path.relative(repoRoot, outFile)} (${Math.round(size / 1024)} KB)`);
  });
}

/* 运行时部分：查表 + 前缀匹配 + 拼 DOM。手写在这里，跟着图标一起生成，
   避免生成物和逻辑分处两地。 */
const RUNTIME = `  /* 前缀匹配要卡边界，否则 "o1" 会把 "o1x-foo" 也算成 OpenAI。
     和后端 llm.MatchModelProfile 的规则基本一致，只多放行"数字"——
     模型 ID 常见把版本号直接贴在前缀后面（qwen3、qwen2.5、abab6.5），
     不放行的话这些都要单独列一条前缀。 */
  function hit(id: string, prefix: string) {
    if (!prefix || id.length < prefix.length || id.indexOf(prefix) !== 0) return false;
    if (id.length === prefix.length) return true;
    if (prefix.charAt(prefix.length - 1) === "-") return true;
    var c = id.charAt(prefix.length);
    return c === "-" || c === "." || c === ":" || c === "/" || (c >= "0" && c <= "9");
  }

  function matchBrand(modelId: string) {
    var id = String(modelId || "").toLowerCase().trim();
    if (!id) return "";
    /* 带厂商前缀的写法（OpenRouter 风格 vendor/model）先拆出最后一段再匹配 */
    var cands = [id];
    var slash = id.lastIndexOf("/");
    if (slash >= 0 && slash < id.length - 1) cands.push(id.slice(slash + 1));
    for (var i = 0; i < MODEL_PREFIXES.length; i++) {
      var prefix = MODEL_PREFIXES[i][0];
      for (var j = 0; j < cands.length; j++) {
        if (hit(cands[j], prefix)) return MODEL_PREFIXES[i][1];
      }
    }
    return "";
  }

  function svgFor(brand: string) {
    return brand && ICONS[brand] ? ICONS[brand] : "";
  }

  /* 建一个图标节点。查不到品牌时给一个首字母方块（与协议章同一套外观），
     保证每行的图标位都有东西，不会一行有一行没有。 */
  function node(brand: string, fallbackText?: string) {
    var key = String(brand || "");
    var wrap = document.createElement("span");
    wrap.className = "brand-icon";
    var svg = svgFor(key);
    if (svg) {
      wrap.innerHTML = svg;
      wrap.dataset.brand = key;
      return wrap;
    }
    wrap.classList.add("brand-icon-letter");
    wrap.textContent = (String(fallbackText || key || "?").trim().charAt(0) || "?").toUpperCase();
    return wrap;
  }

  /* 供应商行用：先按预设 id 查，查不到再拿名字当兜底字 */
  function forPreset(presetId: string, fallbackText?: string) {
    var brand = PRESETS[String(presetId || "")] || "";
    return node(brand, fallbackText || presetId);
  }

  /* 模型行用：按模型 ID 认品牌；认不出就用所属供应商的预设兜底 */
  function forModel(modelId: string, fallbackPresetId?: string, fallbackText?: string) {
    var brand = matchBrand(modelId);
    if (!brand && fallbackPresetId) brand = PRESETS[String(fallbackPresetId)] || "";
    return node(brand, fallbackText || modelId);
  }

  (window as any).BrandIcons = {
    node: node,
    forPreset: forPreset,
    forModel: forModel,
    matchBrand: matchBrand,
    brands: function () { return Object.keys(ICONS); },
  };`;

main().catch((err) => {
  console.error(err.message || err);
  process.exit(1);
});
