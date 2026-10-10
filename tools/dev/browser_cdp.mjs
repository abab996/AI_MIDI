import fs from "node:fs";import path from "node:path";
const outDir=path.resolve("output/audit-2026-10-01/browser");
export function findBrowser() {
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
export class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.errors = [];
    this.sessionId = null;
    ws.addEventListener("close", () => {
      for (const { reject } of this.pending.values()) reject(new Error("CDP connection closed"));
      this.pending.clear();
    });
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
    if(m==='Network.loadingFailed')this.errors.push('network: '+msg.params.errorText+' '+(msg.params.blockedReason||''));
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
      const timer=setTimeout(()=>{this.pending.delete(id);reject(new Error("CDP timeout: "+method));},30000);
      this.pending.set(id, { resolve: value=>{clearTimeout(timer);resolve(value);}, reject: error=>{clearTimeout(timer);reject(error);} });
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
