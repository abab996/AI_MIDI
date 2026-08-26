/* ═══════════════════════════════════════════════════════════════════
 * AI_MIDI-go 宣传视频 · 演示剧本数据（Demo Script Data）
 * ---------------------------------------------------------------
 * 仅在 chat.html?demo=1 时由 showrunner.js 加载使用。
 * 本文件是纯数据：提示词、确定性 SSE 回放帧、各镜头编排参数。
 * 所有工具块字符串与后端 llm.FormatSingleToolEntry / FormatPendingToolEntry
 * 的输出格式逐字段对齐，保证前端渲染与真实 AI 响应完全一致。
 * ═══════════════════════════════════════════════════════════════════ */
(function (window) {
  "use strict";

  /* ---- 工具块模板（对齐 internal/llm/tool_format.go）---- */
  function toolPending(name, argsShort) {
    return "<details>\n<summary>🔧 调用 `" + name + "(" + argsShort + ")` ⏳</summary>\n\n" +
      '<div class="tool-pending">⏳ 正在执行 `' + name + "`…</div>\n</details>";
  }
  function toolDone(name, argsShort, result) {
    return "<details>\n<summary>🔧 调用 `" + name + "(" + argsShort + ")`</summary>\n\n" +
      "```\n" + result + "\n```\n</details>";
  }

  var ARGS_LIB_READ = 'filename="harmony_dorian_vamp.md"';
  var RESULT_LIB_READ =
    "✓ 已读取乐理指南 harmony_dorian_vamp.md（2.4 KB）\n" +
    "  · 多利亚色彩：i7 ↔ IV7 循环（Dm9 ↔ G13），自然大调降 3、升 6 音\n" +
    "  · 建议声位：中音区封闭排列，避免与贝斯低频冲突";

  /* 生成的 Note Table 摘要（展示用截断，与真实格式一致） */
  var NOTE_TABLE_CHORDS =
    '[note: "D4", velocity: "66", start: "0", end: "2.4"]\n' +
    '[note: "F4", velocity: "63", start: "0", end: "2.4"]\n' +
    '[note: "A4", velocity: "60", start: "0", end: "2.4"]\n' +
    '[note: "C5", velocity: "57", start: "0", end: "2.4"]\n' +
    '[note: "F4", velocity: "54", start: "2.56", end: "3.41"] …';

  /* 与真实后端一致：files 事件携带工程内全量文件清单（前端整体替换） */
  function allFilesEvent(newName, newSize, pid) {
    var base = [
      { name: S_NAME_CHORDS, path: "projects/" + pid + "/midi/" + S_NAME_CHORDS, size: 1925, note_table: NOTE_TABLE_CHORDS },
      { name: S_NAME_FULL, path: "projects/" + pid + "/midi/" + S_NAME_FULL, size: 4424, note_table: NOTE_TABLE_CHORDS },
      { name: S_NAME_BASS, path: "projects/" + pid + "/midi/" + S_NAME_BASS, size: 424, note_table: NOTE_TABLE_CHORDS },
    ];
    if (newName) {
      base = base.filter(function (f) { return f.name !== newName; });
      base.push({ name: newName, path: "projects/" + pid + "/midi/" + newName, size: newSize, note_table: NOTE_TABLE_CHORDS });
    }
    return { type: "files", files: base };
  }
  var S_NAME_CHORDS = "Lofi_Chords.mid";
  var S_NAME_FULL = "Lofi_Full.mid";
  var S_NAME_BASS = "Demo_Bass.mid";

  /* ═══════════════ 镜头 02：一句话生成 Lo-Fi 和弦 ═══════════════ */
  var PROMPT_1 = "来一段 85 BPM 温暖复古的 Lo-Fi 电钢和弦，D 多利亚调式，要能直接循环";

  function gen1Frames(pid) {
    var U = { role: "user", content: PROMPT_1 };
    var T1done = toolDone("read_library_file", ARGS_LIB_READ, RESULT_LIB_READ);
    return [
      { at: 600, frames: [{ type: "chat", messages: [U] }] },
      {
        at: 1400,
        frames: [{
          type: "chat",
          messages: [U, { role: "assistant", content: toolPending("read_library_file", ARGS_LIB_READ) }],
        }],
      },
      {
        at: 2500,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: T1done },
            { role: "assistant", content: toolPending("create_midi", 'filename="Lofi_Chords.mid"') },
          ],
        }],
      },
      { at: 3400, events: [allFilesEvent(null, 0, pid)] },
      {
        at: 3500,
        events: [{ type: "download", url: "/api/files/download?path=" + encodeURIComponent("projects/" + pid + "/midi/Lofi_Chords.mid") }],
      },
      {
        at: 3700,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: T1done },
            {
              role: "assistant",
              content: toolDone("create_midi", 'filename="Lofi_Chords.mid"',
                "✓ MIDI 已生成：Lofi_Chords.mid\n  · 16 小节 · 85 BPM · D Dorian\n  · 224 个音符 · 电钢和声垫"),
            },
          ],
        }],
      },
      {
        at: 4600,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: T1done },
            {
              role: "assistant",
              content: toolDone("create_midi", 'filename="Lofi_Chords.mid"',
                "✓ MIDI 已生成：Lofi_Chords.mid\n  · 16 小节 · 85 BPM · D Dorian\n  · 224 个音符 · 电钢和声垫"),
            },
            {
              role: "assistant",
              content:
                "✓ 已为你在 **D 多利亚** 调式上铺好一段温暖复古的 Lo-Fi 和弦进行 —— **85 BPM · 16 小节**：\n\n" +
                "- **和声**：Dm9 ↔ G13 律动交替，第 7-8 小节落在 Fmaj7 → A7sus，循环回来刚好解决\n" +
                "- **律动**：反拍切分 comping，力度按小节呼吸，自带 6% 摇摆\n" +
                "- **音色建议**：Rhodes 电钢 / 柔弦 Pad\n\n" +
                "文件已加入左侧列表，双击 `Lofi_Chords.mid` 即可进入钢琴卷帘细修每一个音符。",
            },
          ],
        }],
      },
      { at: 5200, frames: [], events: [{ type: "done" }], done: true },
    ];
  }

  /* ═══════════════ 镜头 07：快速任务（配和弦，紧凑回放）═══════════════
     userMessage = 拦截器捕获的真实首条消息（qtComposeMessage 的输出，
     含完整音符表）——原样回显，保证与本地气泡逐字一致、无闪变 */
  function gen3Frames(pid, userMessage) {
    var U = { role: "user", content: userMessage ||
      "【快速任务 · 配和弦】\n音符表（共 224 个音符）：…\n" +
      "具体要求：使用 1-6-4-5 和声进行，节奏舒缓，加一条对位旋律线\nBPM：85｜拍号：4/4" };
    return [
      { at: 500, frames: [{ type: "chat", messages: [U] }] },
      {
        at: 1400,
        frames: [{
          type: "chat",
          messages: [U, { role: "assistant",
            content: toolPending("read_library_file", ARGS_LIB_READ) }],
        }],
      },
      {
        at: 2500,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: toolDone("read_library_file", ARGS_LIB_READ,
              "✓ 已读取 harmony_dorian_vamp.md\n  · 多利亚 i7 ↔ IV7 循环要点已载入") },
            { role: "assistant", content: toolPending("create_midi", 'filename="output.mid"') },
          ],
        }],
      },
      {
        at: 3700,
        events: [{ type: "download", url: "/api/files/download?path=" + encodeURIComponent("output/output.mid") }],
      },
      {
        at: 3900,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: toolDone("read_library_file", ARGS_LIB_READ, "…") },
            { role: "assistant", content: toolDone("create_midi", 'filename="output.mid"',
              "✓ MIDI 已生成：output.mid\n  · 原旋律 + AI 和弦音轨合并\n  · Dm9 ↔ G13 律动 · 附对位旋律线"),
            },
          ],
        }],
      },
      {
        at: 5000,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: toolDone("read_library_file", ARGS_LIB_READ, "…") },
            { role: "assistant", content: toolDone("create_midi", 'filename="output.mid"',
              "✓ MIDI 已生成：output.mid\n  · 原旋律 + AI 和弦音轨合并\n  · Dm9 ↔ G13 律动 · 附对位旋律线"),
            },
            {
              role: "assistant",
              content: "✓ 快速任务完成 —— 已按 **D 多利亚** 为你的旋律配上和声（85 BPM）。\n\n" +
                "- **进行**：1-6-4-5 和声进行，Dm9 ↔ G13 律动交替\n- **对位旋律线**：新增一条五声对位，节奏舒缓\n\n" +
                "`output.mid` 已就绪，可下载或直接拖入编曲窗。",
            },
          ],
        }],
      },
      { at: 5800, frames: [], events: [{ type: "done" }], done: true },
    ];
  }

  /* ═══════════════ 镜头 06：发回聊天 → 全奏编曲 ═══════════════ */
  var PROMPT_2 = "很好！保留这段和弦与我手绘的旋律，补一条律动贝斯和 Boom-Bap 鼓组，做成完整编曲";
  var ARGS_CREATE_FULL = 'filename="Lofi_Full.mid"';

  function gen2Frames(pid) {
    var U = { role: "user", content: PROMPT_2 };
    return [
      { at: 500, frames: [{ type: "chat", messages: [U] }] },
      {
        at: 1300,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: toolPending("read_library_file", 'filename="arrangement_layers.md"') },
          ],
        }],
      },
      {
        at: 2300,
        frames: [{
          type: "chat",
          messages: [
            U,
            {
              role: "assistant",
              content: toolDone("read_library_file", 'filename="arrangement_layers.md"',
                "✓ 已读取乐理指南 arrangement_layers.md（3.1 KB）\n" +
                "  · 层次递进：A 段和声+贝斯铺底，B 段旋律进入，末段全奏\n" +
                "  · 贝斯遵循根音-五音骨架，鼓组 Boom-Bap：Kick 1/3.5 · Snare 2/4"),
              },
          ],
        }],
      },
      {
        at: 3200,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: toolDone("read_library_file", 'filename="arrangement_layers.md"', "…") },
            { role: "assistant", content: toolPending("create_midi", ARGS_CREATE_FULL) },
          ],
        }],
      },
      { at: 4200, events: [allFilesEvent("Lofi_Full.mid", 4424, pid)] },
      { at: 4300, events: [{ type: "download", url: "/api/files/download?path=" + encodeURIComponent("projects/" + pid + "/midi/Lofi_Full.mid") }] },
      {
        at: 4500,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: toolDone("read_library_file", 'filename="arrangement_layers.md"', "…") },
            {
              role: "assistant",
              content: toolDone("create_midi", ARGS_CREATE_FULL,
                "✓ MIDI 已生成：Lofi_Full.mid\n  · 16 小节 · 85 BPM · 4 声部\n  · 和弦垫 + 贝斯 + 主旋律 + 鼓组"),
            },
          ],
        }],
      },
      {
        at: 5400,
        frames: [{
          type: "chat",
          messages: [
            U,
            { role: "assistant", content: toolDone("read_library_file", 'filename="arrangement_layers.md"', "…") },
            {
              role: "assistant",
              content: toolDone("create_midi", ARGS_CREATE_FULL,
                "✓ MIDI 已生成：Lofi_Full.mid\n  · 16 小节 · 85 BPM · 4 声部\n  · 和弦垫 + 贝斯 + 主旋律 + 鼓组"),
            },
            {
              role: "assistant",
              content:
                "✓ 完整编曲已就绪 —— 在你的和弦与手绘旋律之上，补齐了 **贝斯、主奏对位与 Boom-Bap 鼓组**（85 BPM · 16 小节）：\n\n" +
                "- **贝斯**：A 段长音铺底，B 段八度弹跳走步线\n" +
                "- **鼓组**：经典 Boom-Bap 骨架 + 摇摆 Hat，第 9 小节全奏进入\n" +
                "- **动态**：结尾四小节逐步回落，循环无缝\n\n" +
                "双击左侧 `Lofi_Full.mid`，一键试听完整效果。",
            },
          ],
        }],
      },
      { at: 6000, frames: [], events: [{ type: "done" }], done: true },
    ];
  }

  /* ═══════════════ 各镜头编排参数（数据驱动）═══════════════ */
  var CHOREO = {
    /* 镜头 03：BPM / 音阶高亮 / 手绘旋律（拍位, MIDI 音高, 时值拍）*/
    shot3: {
      bpm: 85,
      scaleValue: "dorian",
      rootPitch: 62, /* D4 为调式主音 */
      drawNotes: [
        { beat: 64.0, pitch: 69, dur: 0.5 },   /* A4 */
        { beat: 64.5, pitch: 72, dur: 0.5 },   /* C5 */
        { beat: 65.0, pitch: 74, dur: 1.0 },   /* D5 */
        { beat: 66.0, pitch: 77, dur: 1.0 },   /* F5 */
        { beat: 67.0, pitch: 76, dur: 0.75 },  /* E5 */
        { beat: 68.0, pitch: 74, dur: 1.25 },  /* D5 */
      ],
      playheadBeat: 62,
      playMs: 5200,
    },
    /* 镜头 04：框选 → 扫弦 → 切片 → 粒子擦除 */
    shot4: {
      zoomOutWheelTicks: 6,
      selectBox: { fromBeat: 7.6, fromPitch: 79, toBeat: 11.9, toPitch: 60 }, /* 第 3 小节 Dm9 区域 */
      strum: { time: 46, velRamp: -10, altDir: false },
      sliceBeat: 9.0,
      eraseSweep: [
        { beat: 18.4, pitch: 65 }, { beat: 18.9, pitch: 69 },
        { beat: 19.4, pitch: 72 }, { beat: 19.9, pitch: 76 },
      ],
    },
    /* 镜头 05：幽灵参考轨 + 打字键盘即兴（FL 键位：Z=C4 X=D C=E V=F G=G B=A Q=C5）*/
    shot5: {
      ghostFile: "Demo_Bass.mid",
      jam: [
        ["KeyX", 280], ["KeyC", 210], ["KeyV", 250], ["KeyG", 330],
        ["KeyV", 210], ["KeyC", 250], ["KeyB", 460], ["KeyG", 250],
        ["KeyV", 210], ["KeyX", 500], ["KeyQ", 330], ["KeyB", 270],
        ["KeyG", 560],
      ],
      jamPauseMs: 170,
    },
    /* 导出按钮（镜头 07 前置素材，可选） */
    exportBtnId: "prExportBtn",
  };

  window.DEMO_SCRIPT = {
    projectName: "宣传片演示工程",
    prompt1: PROMPT_1,
    prompt2: PROMPT_2,
    gen1: gen1Frames,
    gen2: gen2Frames,
    gen3: gen3Frames,
    choreo: CHOREO,
    chordFileName: "Lofi_Chords.mid",
    fullFileName: "Lofi_Full.mid",
  };
})(window);
