/* 功能过场（demo/transition.html）——原内联脚本，CSP 收紧后搬出。URL 参数一律 textContent/DOM 分段写入，不进 innerHTML（反射型 XSS 防护） */
  const q = new URLSearchParams(location.search);
  const step = parseInt(q.get("step") || "1", 10);
  document.getElementById("shot").src = "/demo/assets/shots/" + (q.get("img") || "pianoroll.png");
  document.getElementById("stamp").textContent = "[ " + (q.get("stamp") || "FEATURE 0" + step) + " ]";
  /* cn 支持 "|" 换行：用 DOM 分段而非 innerHTML 拼接——URL 参数直进
     innerHTML 是反射型 XSS（?cn=<img src=x onerror=...>） */
  const cnEl = document.getElementById("cn");
  (q.get("cn") || "标题").split("|").forEach((seg, i) => {
    if (i > 0) cnEl.appendChild(document.createElement("br"));
    cnEl.appendChild(document.createTextNode(seg));
  });
  document.getElementById("en").textContent = q.get("en") || "";
  document.getElementById("tag").textContent = q.get("tag") || "LIVE DEMO →";
  document.getElementById("wm").textContent = "0" + step;
  const chips = document.getElementById("chips");
  (q.get("chips") || "").split("|").filter(Boolean).forEach((c) => {
    const s = document.createElement("span");
    s.textContent = c;
    chips.appendChild(s);
  });
  const dots = document.getElementById("dots");
  for (let i = 1; i <= 4; i++) {
    const d = document.createElement("i");
    if (i === step) d.className = "on";
    dots.insertBefore(d, dots.querySelector("em"));
  }

  /* 时间轴（85 BPM，总长 ~3.53s）：入场 1s → 保持 → 由录制端在 ~3.1s 触发退场 */
  window.addEventListener("load", () => {
    const $ = (id) => document.getElementById(id);
    try { document.getElementById("shot").decode(); } catch (_) {}
    setTimeout(() => { $("slab").classList.add("show"); $("connector").classList.add("show"); }, 60);
    setTimeout(() => { $("stamp").classList.add("show"); $("wm").classList.add("show"); }, 150);
    document.querySelectorAll(".deco").forEach((el) => el.classList.add("show"));
    setTimeout(() => $("cn").classList.add("show"), 320);
    setTimeout(() => $("en").classList.add("show"), 550);
    document.querySelectorAll("#chips span").forEach((el, i) => {
      setTimeout(() => el.classList.add("show"), 750 + i * 170);
    });
    if (q.get("out") === "1") {
      setTimeout(() => document.getElementById("stage").classList.add("out"),
        parseInt(q.get("outat") || "3050", 10));
    }
  });
