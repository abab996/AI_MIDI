/* 把 frontend/src 下的运行时 .ts 擦成 frontend/js。
   类型被换成空白，语句本身保持原样。types/ 与 .d.ts 不输出。
   已有 JS 内容不变时不写盘，避免无意义地改 mtime。 */
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import tsBlankSpace from "ts-blank-space";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const srcDir = path.join(repoRoot, "frontend", "src");
const outDir = path.join(repoRoot, "frontend", "js");

function walk(dir, acc) {
  if (!fs.existsSync(dir)) return acc;
  for (const ent of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, ent.name);
    if (ent.isDirectory()) {
      if (ent.name === "types") continue;
      walk(full, acc);
    } else if (ent.isFile() && ent.name.endsWith(".ts") && !ent.name.endsWith(".d.ts")) {
      acc.push(full);
    }
  }
  return acc;
}

function hasImportOrExport(source) {
  return /^\s*(import|export)\b/m.test(source);
}

export function emitFrontend() {
  const files = walk(srcDir, []);
  let failed = false;
  for (const file of files) {
    const rel = path.relative(srcDir, file);
    const outPath = path.join(outDir, rel.replace(/\.ts$/, ".js"));
    const input = fs.readFileSync(file, "utf8");
    const errors = [];
    const output = tsBlankSpace(input, (node) => {
      const kind = node && node.kind;
      const text = node && typeof node.getText === "function" ? node.getText().slice(0, 120) : "";
      errors.push(kind + (text ? " " + text : ""));
    });
    if (errors.length) {
      console.error(rel + ": ts-blank-space 无法擦除 " + errors.length + " 处语法");
      for (const err of errors) console.error("  " + err);
      failed = true;
      continue;
    }
    if (hasImportOrExport(output)) {
      console.error(rel + ": 擦除结果含有 import/export，不能作为经典脚本加载");
      failed = true;
      continue;
    }
    fs.mkdirSync(path.dirname(outPath), { recursive: true });
    const prev = fs.existsSync(outPath) ? fs.readFileSync(outPath, "utf8") : null;
    if (prev !== output) fs.writeFileSync(outPath, output);
    const check = spawnSync(process.execPath, ["--check", outPath], { encoding: "utf8" });
    if (check.status !== 0) {
      console.error(rel + ": node --check 失败");
      if (check.stderr) console.error(check.stderr);
      if (check.stdout) console.error(check.stdout);
      failed = true;
    }
  }
  return { count: files.length, failed };
}

const isDirect = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isDirect) {
  const result = emitFrontend();
  if (result.failed) process.exit(1);
  console.log("emit-frontend: " + result.count + " file(s)");
}
