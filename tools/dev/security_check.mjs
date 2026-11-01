/* 安全不变量检查（无需服务、无需浏览器）。
 *
 * 覆盖本轮安全加固中"容易在后续修改里被悄悄回退"的静态契约：
 *   A. 更新链路 fail-closed：缺 sha256 / 白名单外主机 / 跟随重定向
 *   B. 引擎 IPC 鉴权：令牌环境变量、hello 校验、握手前闸门
 *   C. MM 编译/安装授权：不再接受模型传入的 confirm
 *   D. 插件 UI 与链接：CSP、sandbox、相对链接拒绝、令牌引导脚本
 *
 * 行为级验证由 Go 测试承担（internal/{update,server,engine,mcp}），
 * 这里只钉住"源码里必须存在/必须不存在"的形态，防止整段被改回去。
 *
 * 用法：node tools/dev/security_check.mjs
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const read = (p) => fs.readFileSync(path.join(repoRoot, p), "utf8");
const exists = (p) => fs.existsSync(path.join(repoRoot, p));

let failures = 0;
function check(name, ok, extra) {
  if (ok) console.log("  ok   " + name);
  else {
    failures++;
    console.log("  FAIL " + name + (extra !== undefined ? "  → " + JSON.stringify(extra).slice(0, 300) : ""));
  }
}

/* ═══════════ A. 更新链路 ═══════════ */
console.log("[A] 更新链路 fail-closed");

const updateGo = read("internal/update/update.go");
const downloadGo = read("internal/update/download.go");
const handlerUpdate = read("internal/server/handler_update.go");

check("清单/下载请求禁止跟随重定向",
  updateGo.includes("noRedirectPolicy") && downloadGo.includes("CheckRedirect: noRedirectPolicy"));
check("存在下载主机白名单与 URL 校验",
  updateGo.includes("IsTrustedDownloadURL") && updateGo.includes("trustedHosts"));
check("存在 sha256 强校验",
  updateGo.includes("IsValidSHA256") && downloadGo.includes("ErrSHARequired"));
check("Start 在缺 sha256 时直接拒绝（不发起请求）",
  downloadGo.includes("if !IsValidSHA256(wantSHA)"));
check("apply 拒绝缺 sha256 的清单",
  handlerUpdate.includes("IsValidSHA256(mani.SHA256Windows)"));
check("apply 拒绝白名单外下载地址",
  handlerUpdate.includes("IsTrustedDownloadURL(dlURL)"));
check("清单不可验证时不启用强制更新",
  handlerUpdate.includes("mandatory := available && mani.Mandatory && shaOK"));

const configGo = read("internal/config/config.go");
check("白名单与清单域名同源维护",
  configGo.includes("UpdateManifestHosts") && configGo.includes("melodymate-r2.baimoo.top"));

/* ═══════════ B. 引擎 IPC 鉴权 ═══════════ */
console.log("[B] 引擎 IPC 鉴权");

const supGo = read("internal/engine/supervisor.go");
const clientGo = read("internal/engine/client.go");
const mainCpp = read("engine/Source/Main.cpp");

check("令牌经环境变量注入引擎",
  supGo.includes("engineTokenEnv") && supGo.includes(`engineTokenEnv+"="+token`));
check("启动令牌用密码学随机源",
  supGo.includes("crypto/rand") && supGo.includes("rand.Read"));
check("客户端握手携带令牌",
  clientGo.includes("DialWithToken") && clientGo.includes(`params["token"] = c.token`));
check("引擎常量时间比较令牌",
  mainCpp.includes("constantTimeEquals"));
check("引擎在握手前拒绝业务方法",
  mainCpp.includes("ctx.authToken.isNotEmpty() && !ctx.authenticated"));
check("引擎读环境变量令牌",
  mainCpp.includes("MelodyMate_ENGINE_TOKEN"));

/* ═══════════ C. MM 编译/安装授权 ═══════════ */
console.log("[C] MM 编译/安装授权来自用户确认");

const mmToolsGo = read("internal/mcp/mm_tools.go");
const approvalGo = read("internal/mcp/approval.go");

check("授权走一次性批准令牌",
  mmToolsGo.includes("mm_request_build_approval") && mmToolsGo.includes("consumeApproval"));
check("模型传入的 confirm 不再构成授权",
  !mmToolsGo.includes(`mapBool(args, "confirm")`));
check("批准绑定参数摘要（防换参数复用）",
  approvalGo.includes("ArgsDigest") && approvalGo.includes("参数与用户确认的内容不一致"));
check("批准一次性且会过期",
  approvalGo.includes("ApprovalTTL") && approvalGo.includes("已被使用"));
check("批准按项目隔离",
  read("internal/chat/approval.go").includes("不属于当前项目"));
check("工具执行上下文注入批准闸门",
  read("internal/chat/pipeline.go").includes("Approval: ApprovalGateFor(projectID)"));

/* ═══════════ D. 插件 UI 与链接 ═══════════ */
console.log("[D] 插件 UI 承载与链接收紧");

const mmUi = read("frontend/mm-ui.html");
const appTs = read("frontend/src/app.ts");
const appJs = read("frontend/js/app.js");

/* CSP 必须真实生效：meta 必须在注释之外（此前 TMP-CSP-DISABLED 把整段
   meta 包进 HTML 注释，对原始文本做 regex 仍会误通过）。这里剥掉 HTML
   注释后再断言，并逐页确认 script-src 不含 'unsafe-inline'。 */
const stripHtmlComments = (s) => s.replace(/<!--[\s\S]*?-->/g, "");
const activeCsp = (page) => {
  const m = stripHtmlComments(read(page)).match(/http-equiv="Content-Security-Policy"\s+content="([^"]*)"/);
  return m ? m[1] : "";
};
check("承载页 CSP 存在且未被注释禁用",
  (() => { const c = activeCsp("frontend/mm-ui.html"); return c !== "" && !mmUi.includes("TMP-CSP-DISABLED"); })());
check("承载页 script-src 无 unsafe-inline",
  (() => { const c = activeCsp("frontend/mm-ui.html"); return c.includes("script-src") && !/script-src[^;]*'unsafe-inline'/.test(c); })());
for (const page of ["frontend/index.html", "frontend/chat.html", "frontend/settings.html",
  "frontend/audio_selftest.html", "frontend/demo/intro.html", "frontend/demo/stage.html",
  "frontend/demo/transition.html", "frontend/demo/outro.html"]) {
  check(`${page} script-src 无 unsafe-inline`,
    (() => { const c = activeCsp(page); return c.includes("script-src") && !/script-src[^;]*'unsafe-inline'/.test(c); })());
}
/* 只校验 sandbox 属性本身，注释里提到 allow-same-origin 不算违规 */
const sandboxAttr = (mmUi.match(/sandbox="([^"]*)"/) || [])[1] || "";
check("插件 iframe 仍为 allow-scripts（无 allow-same-origin）",
  sandboxAttr.includes("allow-scripts") && !sandboxAttr.includes("allow-same-origin"), sandboxAttr);
check("插件 iframe 地址带一次性 ui_ticket",
  (() => {
    const carrier = exists("frontend/js/mm_ui_page.js") ? read("frontend/js/mm_ui_page.js") : mmUi;
    return carrier.includes("ui-ticket") && carrier.includes("&ticket=");
  })());
check("Markdown 不再放行相对链接",
  appTs.includes("return false;") && !appTs.includes(`lower.indexOf(":") === -1`));
check("Markdown 注册失败不再 fail-open（_mdSanitized 强制走 mdMini）",
  appTs.includes("_mdSanitized") && appJs.includes("_mdSanitized"));
check("页面启动/承载脚本已外置（CSP 收紧配套）",
  exists("frontend/js/boot.js") && exists("frontend/js/index_page.js") &&
  exists("frontend/js/chat_page.js") && exists("frontend/js/settings_page.js") &&
  exists("frontend/js/audio_selftest.js") && exists("frontend/js/mm_ui_page.js"));
check("已编译产物与源码一致（app.js 同为收紧后逻辑）",
  appJs.includes("/^https:/.test(lower)") && !appJs.includes(`lower.indexOf(":") === -1`));

check("存在启动令牌引导脚本", exists("frontend/js/authfetch.js"));
const authJs = exists("frontend/js/authfetch.js") ? read("frontend/js/authfetch.js") : "";
check("引导脚本注入 X-MM-Token 且只对同源 /api/",
  authJs.includes("X-MM-Token") && authJs.includes("isSameOriginApi"));
check("sendBeacon 也被接管（页面卸载保存需带令牌）",
  authJs.includes("sendBeacon") && authJs.includes("keepalive"));
for (const page of ["frontend/chat.html", "frontend/index.html", "frontend/settings.html"]) {
  check(`${page} 最先加载 authfetch.js`,
    read(page).indexOf("/js/authfetch.js") > 0);
}
check("插件 UI 响应补安全响应头",
  read("internal/server/plugin_resources.go").includes("X-Content-Type-Options"));

console.log("");
if (failures > 0) {
  console.log(`security_check: ${failures} 项不通过`);
  process.exit(1);
}
console.log("security_check: 全部通过");
