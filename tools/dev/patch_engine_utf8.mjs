/* 一次性修复脚本：把引擎源码里"含非 ASCII 的字面量"包上 u8()。
 *
 * 背景：JUCE 的 String(const char*) 按 ASCII 解释，中文 IPC 文本会逐字节
 * 展开成乱码（详见 engine/Source/Text.h）。手动改 30+ 处容易漏，用脚本
 * 机械处理 + 人工 review diff。
 *
 * 规则：
 *   · 跳过整行注释（// * /*）与行内注释之后的字面量；
 *   · 跳过 printf/fprintf/DBG 家族（那是窄字符输出，不是 juce::String）；
 *   · 只处理**第一个**含 CJK 的字面量，且已带 u8( 前缀的跳过。
 *
 * 用法：node tools/dev/patch_engine_utf8.mjs [--apply]
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const apply = process.argv.includes("--apply");
const targets = [
  "engine/Source/Audio/EngineAudioDevice.cpp",
  "engine/Source/Audio/SamplePool.h",
  "engine/Source/Audio/SynthEngine.cpp",
  "engine/Source/Plugins/PluginHost.cpp",
  "engine/Source/Plugins/PluginWindow.h",
  "engine/Source/Main.cpp",
];
const CJK = /[\u3400-\u4dbf\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]/;

let changed = 0;
for (const rel of targets) {
  const file = path.join(repoRoot, rel);
  const src = fs.readFileSync(file, "utf8");
  const lines = src.split("\n");
  let touched = 0;
  const out = lines.map((line) => {
    const trimmed = line.trim();
    if (trimmed.startsWith("//") || trimmed.startsWith("*") || trimmed.startsWith("/*")) return line;
    if (/\b(fprintf|printf|DBG|snprintf)\s*\(/.test(line)) return line;

    // 逐字面量扫描，找到第一个含 CJK 且未被 u8( 包裹的
    let result = line;
    const re = /"(?:[^"\\]|\\.)*"/g;
    let m;
    while ((m = re.exec(line)) !== null) {
      const lit = m[0];
      if (!CJK.test(lit)) continue;
      const before = line.slice(0, m.index);
      const lineComment = before.lastIndexOf("//");
      const blockComment = before.lastIndexOf("/*");
      if (lineComment >= 0 && lineComment > before.lastIndexOf('"')) break;   // 在行内注释里
      if (blockComment >= 0 && blockComment > before.lastIndexOf('"')) break; // 在块注释里
      if (/u8\(\s*$/.test(before)) break;                                     // 已包裹
      const idx = m.index;
      result = line.slice(0, idx) + "u8(" + lit + ")" + line.slice(idx + lit.length);
      touched++;
      break; // 每行只处理第一个
    }
    return result;
  });
  if (touched > 0) {
    changed += touched;
    console.log(`${rel}: ${touched} 处`);
    if (apply) fs.writeFileSync(file, out.join("\n"), "utf8");
  }
}
console.log(apply ? `已写入（${changed} 处）` : `预览：共 ${changed} 处（加 --apply 写入）`);
