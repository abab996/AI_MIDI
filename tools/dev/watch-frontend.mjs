/* 开发监视：tsc --watch 负责类型错误，源码一变就重新擦除 JS。
   正式门禁仍是 npm run build（类型检查失败时不写 JS）。 */
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { emitFrontend } from "./emit-frontend.mjs";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const srcDir = path.join(repoRoot, "frontend", "src");

function emit() {
  const result = emitFrontend();
  if (result.failed) console.error("emit-frontend: 擦除失败");
  else console.log("emit-frontend: " + result.count + " file(s)");
}

emit();

const tscBin = path.join(repoRoot, "node_modules", "typescript", "lib", "tsc.js");
const tsc = spawn(process.execPath, [tscBin, "-p", "frontend/tsconfig.json", "--noEmit", "--watch", "--pretty", "false"], {
  cwd: repoRoot,
  stdio: "inherit"
});

let timer = null;
function schedule() {
  clearTimeout(timer);
  timer = setTimeout(emit, 80);
}

const watcher = fs.watch(srcDir, { recursive: true }, (_event, filename) => {
  if (!filename) return;
  if (filename.endsWith(".ts")) schedule();
});

function shutdown() {
  watcher.close();
  tsc.kill();
  process.exit(0);
}

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
tsc.on("exit", (code) => process.exit(code == null ? 1 : code));
