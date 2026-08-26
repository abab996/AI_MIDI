/* AI_MIDI-go 宣传视频录制器 v2（4K 高画质）
 * ==========================================
 * 放弃 Playwright 自带 recordVideo（VP8 低码率导致文字发糊），
 * 改用 CDP Page.screencast 逐帧 JPEG(q93) 捕获 + deviceScaleFactor 2
 * （1920x1080 CSS 视口 → 3840x2160 物理渲染），按帧时间戳组装 CFR 视频。
 *
 * 用法：node record.js <url> <out.mp4> <durationSec> [setupUrl]
 */
const path = require("path");
const fs = require("fs");
const { execFileSync } = require("child_process");
const CANDIDATES = [
  path.join(__dirname, "node_modules", "playwright-core"),
  "D:/tmp_go/rec/node_modules/playwright-core",
];
const pwDir = CANDIDATES.find((p) => fs.existsSync(p));
if (!pwDir) { console.error("playwright-core 未安装"); process.exit(1); }
const { chromium } = require(pwDir);

/* C 盘满时把 Chrome 临时目录重定向到 D 盘 */
process.env.TMP = "D:\\tmp_go";
process.env.TEMP = "D:\\tmp_go";

const FFBIN = process.env.FFBIN || path.join(
  process.env.LOCALAPPDATA,
  "Microsoft/WinGet/Packages/Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe/ffmpeg-9.0-full_build/bin");
const FFMPEG = path.join(FFBIN, "ffmpeg.exe");

const SCALE = 1;   /* screencast 固定捕获 CSS 分辨率（1920x1080）；
                      强制 4K 会导致 canvas 按 1x 渲染再拉伸反而发糊，
                      4K 交付版本由后期 lanczos 超采样生成 */
const W = 1920, H = 1080, FPS = 60;

(async () => {
  const url = process.argv[2] || "http://127.0.0.1:7860/chat.html?demo=1&auto=1";
  const out = process.argv[3] || "take.mp4";
  const durationMs = parseInt(process.argv[4] || "75", 10) * 1000;
  const setupUrl = process.argv[5] || "";

  const frameDir = "D:/tmp_go/frames_" + Date.now();
  fs.mkdirSync(frameDir, { recursive: true });

  const browser = await chromium.launch({
    channel: "chrome",
    headless: true,
    args: [
      "--autoplay-policy=no-user-gesture-required",
      "--disable-features=TranslateUI,CalculateNativeWinOcclusion",
      "--mute-audio",
      "--enable-gpu",
      "--use-angle=default",
    ],
  });
  const ctx = await browser.newContext({
    viewport: { width: W, height: H },
    deviceScaleFactor: SCALE,
  });
  const page = await ctx.newPage();
  const t0 = Date.now();
  const stamp = () => "[" + ((Date.now() - t0) / 1000).toFixed(1) + "s] ";
  page.on("console", (m) => {
    const t = m.text();
    if (t.includes("[DEMO]")) console.log(stamp() + t.replace(/\[DEMO\]\s*/, ""));
  });
  page.on("pageerror", (e) => console.log(stamp() + "[pageerror] " + e.message));

  /* ── CDP 逐帧捕获 ── */
  const cdp = await ctx.newCDPSession(page);
  let idx = 0;
  const times = [];
  let writing = 0;
  cdp.on("Page.screencastFrame", async (ev) => {
    try {
      const f = path.join(frameDir, "f" + String(idx++).padStart(5, "0") + ".jpg");
      writing++;
      fs.writeFile(f, Buffer.from(ev.data, "base64"), () => writing--);
      times.push(Date.now());
    } catch (_) {}
    cdp.send("Page.screencastFrameAck", { sessionId: ev.sessionId }).catch(() => {});
  });
  await cdp.send("Page.startScreencast", {
    format: "jpeg", quality: 96, everyNthFrame: 1,
    maxWidth: W * SCALE, maxHeight: H * SCALE,
  });

  if (setupUrl) {
    await page.goto(setupUrl, { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(9000);
  }

  console.log("[rec] goto", url);
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(durationMs);

  /* 停止捕获并等待落盘 */
  await cdp.send("Page.stopScreencast").catch(() => {});
  await page.waitForTimeout(600);
  while (writing > 0) await new Promise((r) => setTimeout(r, 120));
  await page.close();
  await ctx.close();
  await browser.close();

  if (!times.length) { console.error("[rec] 未捕获到任何帧"); process.exit(1); }

  /* ── 按帧间隔生成 concat 清单（缺帧区间由时长延续；
     静止收尾页必须把末帧保持到目标时长，否则成片被截短）── */
  const list = path.join(frameDir, "list.txt");
  const spanSec = times.length > 1 ? (times[times.length - 1] - times[0]) / 1000 : 1;
  const targetSec = durationMs / 1000 + (setupUrl ? 9.5 : 0);
  const tailHold = Math.max(0.4, targetSec - spanSec);
  let lines = "";
  for (let i = 0; i < times.length; i++) {
    const dur = i < times.length - 1
      ? (times[i + 1] - times[i]) / 1000
      : tailHold;
    lines += "file 'f" + String(i).padStart(5, "0") + ".jpg'\n";
    lines += "duration " + Math.max(0.02, dur).toFixed(3) + "\n";
  }
  lines += "file 'f" + String(times.length - 1).padStart(5, "0") + ".jpg'\n";
  fs.writeFileSync(list, lines);

  /* ── 编码：4K CFR H.264 ── */
  console.log("[rec] 捕获", times.length, "帧，编码中…");
  execFileSync(FFMPEG, [
    "-y", "-v", "error", "-f", "concat", "-safe", "0", "-i", list,
    "-vf", `fps=${FPS},format=yuv420p`, "-an",
    "-c:v", "libx264", "-preset", "medium", "-crf", "15", out,
  ], { stdio: "inherit" });
  /* 清理帧目录 */
  fs.rmSync(frameDir, { recursive: true, force: true });
  console.log("[rec] saved", out, "(1920x1080 sharp)");
})().catch((e) => { console.error(e); process.exit(1); });
