/* 电影舞台（demo/stage.html）——原内联脚本，CSP 收紧后搬出（手写 js，与 js/demo 既有文件同规） */
/* ══════════════════════════════════════════════════════════
 * MelodyMate-go · 电影舞台
 * scene=chat : 孤立输入 → 发送 → 3D 仰视流式回复 → 提问卡 → 结果定格
 * scene=loop : 卷帘薄片 → 音符飞回聊天 → 编曲扩展 → 导出徽章砸落
 * ══════════════════════════════════════════════════════════ */
const q = new URLSearchParams(location.search);
const SCENE = q.get("scene") || "chat";
const world = document.getElementById("world");
const bodyEl = document.body;

const EASE_OUT = "cubic-bezier(.05,.7,.1,1)";      // M3 emphasized decelerate
let cur = { x: innerWidth * 0.7, y: innerHeight * 0.72 };
let tgt = { x: cur.x, y: cur.y };
const cursor = document.getElementById("cursor");
const ripple = document.getElementById("ripple");
(function follow() {
  cur.x += (tgt.x - cur.x) * 0.42; cur.y += (tgt.y - cur.y) * 0.42;
  cursor.style.transform = `translate(${cur.x.toFixed(1)}px,${cur.y.toFixed(1)}px)`;
  requestAnimationFrame(follow);
})();
function cmove(x, y) { tgt.x = x; tgt.y = y; }
function cclick(x, y) {
  ripple.style.left = x + "px"; ripple.style.top = y + "px";
  ripple.style.transition = "none"; ripple.style.opacity = ".95";
  ripple.style.transform = "scale(.4)"; void ripple.offsetWidth;
  ripple.style.transition = "transform .5s cubic-bezier(.2,.8,.3,1), opacity .5s";
  ripple.style.transform = "scale(3.2)"; ripple.style.opacity = "0";
}
async function cursorTo(x, y, ms = 550) {
  const sx = cur.x, sy = cur.y;
  const t0 = performance.now();
  return new Promise((res) => {
    (function step() {
      const p = Math.min(1, (performance.now() - t0) / ms);
      const e = 1 - Math.pow(1 - p, 3);
      cur.x = sx + (x - sx) * e; cur.y = sy + (y - sy) * e;   /* 直接驱动，消除滞后 */
      tgt.x = cur.x; tgt.y = cur.y;
      if (p < 1) requestAnimationFrame(step); else res();
    })();
  });
}
async function cursorHide() {
  cursor.style.transition = "opacity .35s ease";
  cursor.style.opacity = "0";
  ripple.style.opacity = "0";
}

/* 摄像机：对 body 施加 scale/translate/rotateX/Y + 运动模糊（用后必复位）。
   3D 姿态只作瞬态运镜——Chromium 对 3D 变换做纹理映射不重栅格化，
   保持姿态会导致整页发糊；运镜结束解除合成层强制按终态重栅格化。 */
function cam(to, dur = 650, opts = {}) {
  if (opts.blur) {
    bodyEl.style.transition = "filter .14s ease";
    bodyEl.style.filter = `blur(${opts.blur}px)`;
  }
  bodyEl.style.transition =
    `transform ${dur}ms ${opts.ease || EASE_OUT}` +
    (opts.blur ? `, filter ${Math.round(dur * 0.85)}ms ease ${Math.round(dur * 0.12)}ms` : "");
  bodyEl.style.transformOrigin = opts.origin || "50% 58%";
  bodyEl.style.transform =
    `translate(${to.x || 0}px, ${to.y || 0}px) scale(${to.scale ?? 1})` +
    ` rotateX(${to.rx || 0}deg) rotateY(${to.ry || 0}deg)`;
  clearTimeout(cam._t);
  cam._t = setTimeout(() => {
    bodyEl.style.filter = "none";
    bodyEl.style.willChange = "auto";
    void bodyEl.offsetWidth;
    if (!opts.flat) bodyEl.style.willChange = "transform";
  }, dur + 60);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const el = (tag, cls, html) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (html != null) e.innerHTML = html;
  return e;
};

/* 工具块模板（与后端 llm.FormatSingleToolEntry 同构） */
function toolHTML(name, args, result, pending) {
  if (pending) {
    return `<details>\n<summary>🔧 调用 \`${name}(${args})\` ⏳</summary>\n\n` +
      `<div class="tool-pending">⏳ 正在执行 \`${name}\`…</div>\n</details>`;
  }
  return `<details>\n<summary>🔧 调用 \`${name}(${args})\`</summary>\n\n\`\`\`\n${result}\n\`\`\`\n</details>`;
}

/* ══════════════ 场景一：对话写歌 ══════════════ */
async function sceneChat() {
  /* 1. 孤立输入卡登场 */
  const stg = el("div", "input-stage");
  stg.innerHTML = `
    <div class="input-card" id="icard">
      <div class="input-label">[ PROMPT INPUT · 向你的编曲搭档描述灵感 ]</div>
      <div class="fake-input"><span id="typed"></span><span class="caret"></span></div>
      <div class="send-row"><button class="btn-send" id="sendBtn">▶ 发送 (Enter)</button></div>
    </div>`;
  world.appendChild(stg);
  document.getElementById("brandmark").classList.add("show");
  await sleep(300);
  document.getElementById("icard").classList.add("show");
  cam({ y: 0, scale: 1 }, 10);
  await sleep(900);

  /* 2. 光标飞入输入框，逐字打字 */
  const fake = stg.querySelector(".fake-input");
  const fr = fake.getBoundingClientRect();
  await cursorTo(fr.left + 30, fr.top + 28, 520);
  cclick(fr.left + 30, fr.top + 28);
  const text = "来一段 85 BPM 温暖复古的 Lo-Fi 电钢和弦，D 多利亚调式，要能直接循环";
  const typed = stg.querySelector("#typed");
  let shown = "";
  for (let i = 0; i < text.length; i++) {
    shown += text[i];
    typed.textContent = shown;
    let d = 42 + Math.random() * 46;
    if ("，。、".includes(text[i])) d += 130;
    await sleep(d);
  }
  await sleep(420);

  /* 3. 光标飞向发送键点击；镜头快速推近叠运动模糊，消息"发射"升空 */
  const btn = stg.querySelector("#sendBtn");
  const br = btn.getBoundingClientRect();
  await cursorTo(br.left + br.width / 2, br.top + br.height / 2, 430);
  cclick(br.left + br.width / 2, br.top + br.height / 2);
  cam({ x: 0, y: 60, scale: 1.22, rx: -6 }, 480, { blur: 7 });   // 快速推近 + 模糊
  const dot = el("div", "launch-dot");
  dot.style.left = (br.left + br.width / 2) + "px";
  dot.style.top = br.top + "px";
  document.body.appendChild(dot);
  dot.animate([
    { transform: "translateY(0) scale(1)", opacity: 1 },
    { transform: "translateY(-46vh) scale(.4)", opacity: 0 },
  ], { duration: 620, easing: EASE_OUT });
  await sleep(240);
  stg.querySelector("#icard").animate(
    [{ opacity: 1 }, { opacity: 0 }],
    { duration: 320, fill: "forwards" });

  /* 4. 镜头上仰 whip（瞬态 3D + 运动模糊）→ 回正平面，进入可读的流式阶段 */
  await sleep(260);
  cam({ x: 0, y: -26, scale: 1.05, rx: 8, ry: -4 }, 430, { blur: 5, origin: "50% 30%" });
  await sleep(470);
  cam({ x: 0, y: 0, scale: 1, rx: 0, ry: 0 }, 520);
  await sleep(560);
  const col = el("div", "chat-stage");
  col.innerHTML = `<div class="chat-col" id="ccol"></div>`;
  world.appendChild(col);
  const cc = col.querySelector("#ccol");

  const put = (node, at, blurLast) => new Promise((res) => {
    setTimeout(() => {
      cc.appendChild(node);
      requestAnimationFrame(() => node.classList.add("in"));
      res();
    }, at);
  });
  const userMsg = () => el("div", "msg user",
    `<div class="msg-label">你 · You</div><p>${text}</p>`);
  const toolMsg = (name, args, result, pending) => {
    const d = el("div", "tool-call" + (pending ? " pending" : ""));
    d.innerHTML = toolHTML(name, args, result, pending);
    return d;
  };

  const U = userMsg();
  await put(U, 200);

  /* 5. 流式工具链：先慢后快 */
  const T1P = toolMsg("read_library_file", 'filename="harmony_dorian_vamp.md"', "", true);
  await put(T1P, 900);
  await sleep(1250);
  T1P.classList.remove("pending");
  T1P.innerHTML = toolHTML("read_library_file", ARGS_LIB(),
    "✓ 已读取 harmony_dorian_vamp.md\n  · 多利亚色彩：i7 ↔ IV7 循环\n  · 中音区封闭排列声位建议", false);
  await sleep(700);

  /* 6. 提问卡：先展开显示问题 → 光标点选 → 折叠收起 → 光标退场
     （展开/折叠机制与产品 chat.js 一致：max-height 显式赋值 + open 类） */
  const qc = buildQuestionCard();
  await put(qc, 350);
  await sleep(280);
  const qbody = qc.querySelector(".details-body");
  qbody.style.maxHeight = qbody.scrollHeight + "px";
  qbody.classList.add("open");
  await sleep(1450);
  const optRow = qc.querySelectorAll(".q-option")[0];
  const orr = optRow.getBoundingClientRect();
  await cursorTo(orr.left + 26, orr.top + orr.height / 2, 460);
  cclick(orr.left + 26, orr.top + orr.height / 2);
  optRow.classList.add("selected");
  optRow.querySelector(".q-mark").textContent = "✓";
  await sleep(560);
  /* 折叠动画：正文收起 → 摘要条显示回答 */
  qbody.style.maxHeight = "0px";
  await sleep(430);
  qc.open = false;
  qc.querySelector(".q-sum-state").textContent = "✓ 温暖复古 Lo-Fi";
  await cursorHide();                                        /* 回答后光标退场 */
  await sleep(650);

  /* 7. 后半程加速：create_midi → 结果徽章 → 正文快刷 */
  const T2P = toolMsg("create_midi", 'filename="Lofi_Chords.mid"', "", true);
  await put(T2P, 250);
  await sleep(850);
  T2P.classList.remove("pending");
  T2P.innerHTML = toolHTML("create_midi", 'filename="Lofi_Chords.mid"',
    "✓ MIDI 已生成：Lofi_Chords.mid\n  · 16 小节 · 85 BPM · D Dorian\n  · 179 个音符 · 电钢和声垫", false);
  await sleep(450);
  const A = el("div", "msg ai",
    `<div class="msg-label">AI · 乐理专家</div><p>✓ 和弦垫已完成 —— Dm9 ↔ G13 多利亚律动，` +
    `反拍切分带 6% 摇摆。文件已加入左侧列表，双击即可进钢琴卷帘细修。</p>`);
  await put(A, 380);
  await sleep(700);

  /* 8. 结果聚焦：快速推近（运动模糊）后回平定格，波形逐条升起 */
  cam({ x: 0, y: 0, scale: 1.16, rx: -3 }, 480, { blur: 8 });
  const rc = el("div", "result-card");
  rc.innerHTML = `
    <div class="play-chip">
      <div class="play-btn"></div>
      <div><div class="play-name">Lofi_Chords.mid</div>
        <div class="play-sub">[ 16 BARS · 85 BPM · D DORIAN · 179 NOTES ]</div></div>
    </div>
    <div class="result-wave">${Array.from({ length: 42 }, (_, i) =>
      `<i style="--h:${(0.25 + 0.72 * Math.abs(Math.sin(i * 0.55))).toFixed(2)};animation-delay:${i * 18}ms"></i>`).join("")}</div>
    <div class="result-meta"><span>[ RHODES 电钢 ]</span><span>[ 反拍切分 ]</span><span>[ 6% SWING ]</span></div>`;
  world.appendChild(rc);
  await sleep(500);
  rc.classList.add("show");
  await sleep(2600);
  cam({ x: 0, y: 0, scale: 1 }, 900);
  await sleep(1200);
}

function ARGS_LIB() { return 'filename="harmony_dorian_vamp.md"'; }

/* 问题卡（复刻产品 question-card 结构） */
function buildQuestionCard() {
  const d = el("details", "question-card card-enter");
  d.open = true;
  d.innerHTML = `
    <summary class="q-summary-bar">
      <span class="q-sum-label">❓ AI · 向你提问</span>
      <span class="q-sum-state">待回答</span>
    </summary>
    <div class="details-body"><div class="details-inner q-inner">
      <div class="q-list"><div class="q-block">
        <div class="q-head"><span class="stamp q-stamp">VIBE</span>
          <span class="q-text">想要哪种整体氛围？我来定和声色彩与配器密度。</span></div>
        <div class="q-options">
          <label class="q-option"><span class="q-mark"></span>
            <span class="q-opt-txt"><span class="q-opt-name">温暖复古 Lo-Fi</span>
            <span class="q-opt-desc">Rhodes 电钢 + 磁带底噪，适合傍晚</span></span></label>
          <label class="q-option"><span class="q-mark"></span>
            <span class="q-opt-txt"><span class="q-opt-name">清亮爵士 · 城市夜曲</span>
            <span class="q-opt-desc">行走贝斯 + 刷鼓，微凉夜色</span></span></label>
          <label class="q-option"><span class="q-mark"></span>
            <span class="q-opt-txt"><span class="q-opt-name">电影感弦乐</span>
            <span class="q-opt-desc">绵长弓弦 + 混响尾音</span></span></label>
        </div>
      </div></div>
    </div></div>`;
  return d;
}

/* ══════════════ 场景二：闭环导出 ══════════════ */
async function sceneLoop() {
  document.getElementById("brandmark").classList.add("show");
  const stg = el("div", "loop-stage on");
  stg.innerHTML = `
    <div class="loop-slab">
      <div class="screen" style="border-radius:6px;overflow:hidden;border:1px solid rgba(0,240,255,.5);
           box-shadow:0 26px 70px rgba(10,20,36,.25),0 0 38px rgba(0,240,255,.14)">
        <img id="loopshot" src="/demo/assets/shots/pianoroll.jpg" style="display:block;width:100%">
      </div>
    </div>
    <div class="loop-chat"><div class="chat-col" id="lcc"></div></div>
    <div class="export-badges" id="exp" style="top:auto;bottom:16%">
      <span class="exp-badge">↓ 导出 MIDI</span>
      <span class="exp-badge">↓ 导出 WAV</span>
    </div>`;
  stg.style.display = "block";
  world.appendChild(stg);
  await sleep(400);

  const lcc = stg.querySelector("#lcc");
  const put = (node, at) => new Promise((res) => setTimeout(() => {
    lcc.appendChild(node); requestAnimationFrame(() => node.classList.add("in")); res();
  }, at));

  /* 手修旋律卡片（薄片代表卷帘中的修改） */
  await sleep(500);
  cam({ scale: 1.04, x: -20 }, 700);

  /* 音符粒子从薄片飞向聊天列（发送到聊天） */
  const slabR = stg.querySelector(".loop-slab").getBoundingClientRect();
  const flyN = 7;
  for (let i = 0; i < flyN; i++) {
    const n = el("div", "fly-note");
    n.style.left = (slabR.right - 60) + "px";
    n.style.top = (slabR.top + 120 + i * 26) + "px";
    document.body.appendChild(n);
    n.animate([
      { transform: "translate(0,0) scale(1)", opacity: 0 },
      { opacity: 1, offset: 0.25 },
      { transform: `translate(${innerWidth * 0.36}px, ${-40 - i * 12}px) scale(.5)`, opacity: 0 },
    ], { duration: 900, delay: i * 70, easing: EASE_OUT, fill: "forwards" });
  }
  await sleep(1000);

  const U = el("div", "msg user", `<div class="msg-label">你 · You</div>` +
    `<p>手绘的旋律改好了，发给你 —— 补一条律动贝斯和 Boom-Bap 鼓组</p>`);
  await put(U, 200);
  await sleep(700);

  const T = el("div", "tool-call");
  T.innerHTML = toolHTML("read_library_file", 'filename="arrangement_layers.md"', "",
    true);
  await put(T, 300);
  await sleep(900);
  T.classList.remove("pending");
  T.innerHTML = toolHTML("read_library_file", 'filename="arrangement_layers.md"',
    "✓ 层次递进：A 段铺底 → B 段旋律进入 → 末段全奏", false);
  await sleep(550);

  const A = el("div", "msg ai",
    `<div class="msg-label">AI · 乐理专家</div><p>✓ 完整编曲已扩展 —— <b>贝斯 · 鼓组 · 副歌和声</b>全部就位，` +
    `播放头走到第 9 小节自动进入全奏。</p>`);
  await put(A, 420);
  await sleep(900);

  /* 导出徽章按节拍砸落 */
  const badges = stg.querySelectorAll(".exp-badge");
  badges[0].classList.add("slam");
  await sleep(360);
  badges[1].classList.add("slam");
  cam({ scale: 1.07 }, 600);
  await sleep(2200);
}

/* ══════════════ 启动 ══════════════ */
window.addEventListener("load", async () => {
  try { await document.getElementById("loopshot").decode(); } catch (_) {}
  await sleep(500);
  if (SCENE === "loop") await sceneLoop();
  else await sceneChat();
});
