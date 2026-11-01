/* 宣传片片头（demo/intro.html）——原内联脚本，CSP 收紧后搬出（手写 js，与 js/demo 既有文件同规） */
  /* 时间轴按 85 BPM 节拍对齐（1 拍 = 0.7059s）：
     0.71 拍2: 大字弹入 | 2.12 拍3: 划线+叮 | 2.82 拍4: Not anymore
     4.24 拍6: 副标     | 7.06 小节3(8.47s 切出前): 品牌落版于 5.65(拍8) */
  const B = (n) => n * 0.70588 * 1000;
  const p1 = document.getElementById("phase1");
  const p2 = document.getElementById("phase2");
  const sub = document.getElementById("sub1");
  const brand = document.getElementById("brand");
  const stage = document.querySelector(".stage");
  setTimeout(() => { p1.classList.add("striking"); }, B(3));      /* 2.12s */
  setTimeout(() => {
    p1.classList.add("out");
    p2.style.opacity = 1;
    p2.querySelector(".not-anymore").classList.add("show");
  }, B(4));                                                        /* 2.82s */
  setTimeout(() => sub.classList.add("show"), B(6));               /* 4.24s */
  setTimeout(() => {
    stage.classList.add("out");
    brand.classList.add("show");
  }, B(8) + 400);                                                  /* ~6.07s */
