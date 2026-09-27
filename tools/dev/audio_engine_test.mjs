/* 编曲窗音频引擎回归测试（无浏览器、无真引擎）。
 * 用法: node tools/dev/audio_engine_test.mjs
 *
 * 覆盖（均为实测发现过的引擎模式缺陷）：
 *   1. 同批触发的原生音符参数必须各自独立 —— var 共享闭包会让和弦塌缩成
 *      最后一个音（_triggerDue 的原生 noteOn/noteOff 路径）
 *   2. 停止后不留挂音 —— noteOff 已排入 timer 却随 _clearNativeTimers 被
 *      撤销的音符，必须在 stopSchedule 里补发（引擎 Transport.stop 不杀声部）
 *   3. 采样调度签名必须随 BPM 变化 —— 播放中改速不重发会让音频轨与
 *      MIDI 轨渐进失同步
 *   4. 混音收缩尾槽 —— 删轨后对已不存在的槽位必须发 active=false
 *      （残留 solo 会让引擎 anySolo 判定掐掉全部轨）
 *
 * 通过标准：全部断言通过，退出码 0。
 */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(here, "..", "..");

/* 引擎文件路径可覆盖：便于用旧版本（git show HEAD:...）验证测试确实能
   抓到回归，而不是恒绿 */
const ENGINE_JS = process.env.AUDIO_ENGINE_JS || join(repoRoot, "frontend", "js", "arrangement", "audio_engine.js");
const ARRANGE_JS = process.env.AUDIO_ARRANGE_JS || join(repoRoot, "frontend", "js", "arrangement", "arrange.js");

let passed = 0;
const failures = [];

function check(name, cond, detail) {
  if (cond) {
    passed++;
    console.log(`  ✓ ${name}`);
  } else {
    failures.push(`${name}${detail ? " — " + detail : ""}`);
    console.log(`  ✗ ${name}${detail ? " — " + detail : ""}`);
  }
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

/** 构造一个最小可用的 window 环境并加载 audio_engine.js */
function loadEngine(opts) {
  const calls = { noteOn: [], noteOff: [], panic: 0, scheduleSamples: [] };
  const sandbox = {
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    console,
    performance: { now: () => Date.now() },
    URL: { createObjectURL: () => "blob:stub", revokeObjectURL: () => {} },
    Worker: class {
      constructor() {}
      postMessage() {}
      terminate() {}
    },
    Blob: class {},
    fetch: () => Promise.reject(new Error("no network in test")),
  };
  const tracks = opts.tracks || [];
  const win = {
    AudioBackend: {
      isEngine: () => true,
      isNativePreferred: () => true,
      isEngineReady: () => true,
    },
    EngineBridge: {
      available: true,
      SF2_TRACK: 30,
      noteOnTrack: (idx, midi, vel) => calls.noteOn.push({ idx, midi, vel }),
      noteOffTrack: (idx, midi) => calls.noteOff.push({ idx, midi }),
      panic: () => {
        calls.panic++;
        return Promise.resolve();
      },
      stop: () => Promise.resolve(),
      clearSamples: () => Promise.resolve(),
      scheduleSamples: (samples, bpm) => {
        calls.scheduleSamples.push({ samples, bpm });
        return Promise.resolve();
      },
      setTrackMix: () => Promise.resolve(),
      setTrackVoice: () => Promise.resolve(),
      loadSoundFont: () => Promise.resolve(),
      setTrackPreset: () => Promise.resolve(),
    },
    UI: { toast: () => {} },
    MidiParse: { noteNameToNumber: (n) => 60 },
    AudioContext: undefined,
    SharedAudio: { get: () => null, now: () => Date.now() / 1000 },
  };
  sandbox.window = win;
  win.window = win;

  const code = readFileSync(ENGINE_JS, "utf8");
  const ctx = vm.createContext(sandbox);
  vm.runInContext(code, ctx, { filename: "audio_engine.js" });

  const Engine = win.ArrangeEngine;
  const engine = new Engine(() => tracks);
  engine.getTracks = () => tracks;
  return { engine, win, calls, tracks };
}

async function testChordNotCollapsed() {
  console.log("\n[1] 同批原生音符参数独立（和弦不塌缩）");
  const track = { id: "t1", mute: false, solo: false, clips: [], volume: 0.8 };
  const { engine, calls } = loadEngine({ tracks: [track] });
  const nodes = { _useNative: true, gain: null, synth: null, soundfont: null };
  const now = 0;
  engine._anchorCtxTime = 0;
  engine._playStartBeat = 0;
  engine.isPlaying = true;
  // 三音和弦：不同音高，同一时刻
  const pitches = [60, 64, 67];
  for (const p of pitches) {
    engine._pendingOn.push({ t: now, midi: p, vel: 100, dur: 1, nodes, trackId: "t1" });
  }
  engine._triggerDue(now);
  await sleep(60);
  const got = calls.noteOn.map((c) => c.midi);
  check(
    "三个音符各自发声（参数不共享）",
    got.length === 3 && new Set(got).size === 3,
    `发出的音高 = [${got.join(",")}]，期望 [${pitches.join(",")}]`
  );
  check(
    "轨道序号正确",
    calls.noteOn.every((c) => c.idx === 0),
    JSON.stringify(calls.noteOn)
  );

  // 停止：三音均未到 off 时刻 → 必须全部补发 noteOff
  engine.stopSchedule();
  const offs = calls.noteOff.map((c) => c.midi).sort((a, b) => a - b);
  check(
    "停止后三音全部补发 noteOff（无挂音）",
    offs.length >= 3 && [60, 64, 67].every((p) => offs.includes(p)),
    `补发的 off = [${offs.join(",")}]`
  );
}

async function testNoteOffAfterTimerScheduled() {
  console.log("\n[2] off 已排入 timer 时停止也不挂音");
  const track = { id: "t1", mute: false, solo: false, clips: [], volume: 0.8 };
  const { engine, calls } = loadEngine({ tracks: [track] });
  const nodes = { _useNative: true, gain: null };
  engine.isPlaying = true;
  engine._anchorCtxTime = 0;
  engine._playStartBeat = 0;

  // 音符：立即触发、时值 0.1s —— off 会立刻排入 timer
  engine._pendingOn.push({ t: 0, midi: 72, vel: 100, dur: 0.1, nodes, trackId: "t1" });
  engine._triggerDue(0);
  await sleep(30); // noteOn 已发出；off 的 timer 已在队列（将在 100ms 后触发）

  engine.stopSchedule(); // 撤销 timer 的时机：off 尚未发出
  const offs = calls.noteOff.map((c) => c.midi);
  check(
    "timer 被撤销的 off 已由 stopSchedule 补发",
    offs.includes(72),
    `noteOff 调用 = [${offs.join(",")}]`
  );
}

async function testSf2TrackAdmitted() {
  console.log("\n[5] 引擎模式下 SF2/内置音色轨不被丢弃");
  for (const type of ["sf2", "builtin", "synth"]) {
    const track = { id: "t1", mute: false, solo: false, volume: 0.8, source: { type }, clips: [] };
    const { engine } = loadEngine({ tracks: [track] });
    engine._pitchCache = {};
    // 引擎模式：ctx 为 null，ensureTrack 不会创建 Web 节点
    const nodes = { _useNative: true, gain: null, synth: null, soundfont: null };
    const clip = { notes: [{ note: "C4", velocity: 90, start: 0, end: 1 }], start: 0 };
    engine._pendingOn = [];
    engine._queueMidiClip(clip, track, nodes, 0, 4, { posFrom: 0, beatFrom: 0 }, 0.5);
    check(
      `${type} 轨音符进入调度队列`,
      engine._pendingOn.length === 1,
      `_pendingOn = ${engine._pendingOn.length} 条（0 表示整轨被丢弃 → 静音）`
    );
  }
}

function testScheduleSigIncludesBpm() {
  console.log("\n[3] 采样调度签名随 BPM 变化");
  const track = {
    id: "t1", mute: false, solo: false, volume: 0.8,
    clips: [{ type: "audio", start: 0, length: 4, offset: 0, fadeIn: 0, fadeOut: 0, src: { p: "/x.wav" } }],
  };
  const { win } = loadEngine({ tracks: [track] });
  const src = readFileSync(ARRANGE_JS, "utf8");
  // arrange.js 依赖较重（大量 DOM），这里只验证其签名函数片段：
  // 通过提取函数体在最小上下文中执行，避免整套 UI 依赖
  const m = src.match(/Arrange\.prototype\.sampleScheduleSig = function[\s\S]*?\n  \};/);
  if (!m) {
    check("能在 arrange.js 中定位 sampleScheduleSig", false);
    return;
  }
  const sandbox = { console };
  sandbox.window = { AudioBackend: win.AudioBackend };
  const ctx = vm.createContext(sandbox);
  // 只提取签名函数单独求值：arrange.js 其余部分依赖大量 DOM，全量加载代价过高
  const factory = new Function("window", "Arrange", m[0] + "\nreturn Arrange;");
  class StubArrange {}
  const Arrange = factory(sandbox.window, StubArrange);
  const obj = new Arrange();
  obj.tracks = [track];
  obj.bpm = 120;
  const sigA = obj.sampleScheduleSig();
  obj.bpm = 90;
  const sigB = obj.sampleScheduleSig();
  check("BPM 变化使签名变化（触发重排）", sigA !== sigB, `${sigA} vs ${sigB}`);
  check("BPM 相同时签名稳定", obj.sampleScheduleSig() === sigB);
}

async function testMixSlotCleanup() {
  console.log("\n[4] 混音槽位收缩（删轨后清尾槽）");
  const mkTrack = (id) => ({ id, mute: false, solo: false, volume: 0.8, clips: [] });
  const tracks = [mkTrack("t1"), mkTrack("t2"), mkTrack("t3")];
  const mixCalls = [];
  const sandbox = { console, setTimeout, clearTimeout };
  const win = {
    AudioBackend: { isEngine: () => true, isNativePreferred: () => true },
    EngineBridge: {
      setTrackMix: (idx, gain, pan, mute, solo, active) => {
        mixCalls.push({ idx, active });
        return Promise.resolve();
      },
    },
    UI: { toast: () => {} },
  };
  sandbox.window = win;
  const src = readFileSync(ARRANGE_JS, "utf8");
  const m = src.match(/Arrange\.prototype\.applyMixSafe = function[\s\S]*?\n  \};/);
  if (!m) {
    check("能在 arrange.js 中定位 applyMixSafe", false);
    return;
  }
  const ctx = vm.createContext(sandbox);
  const factory = new Function("window", "Arrange", m[0] + "\nreturn Arrange;");
  class StubArrange {}
  const Arrange = factory(win, StubArrange);
  const obj = new Arrange();
  obj.tracks = tracks;
  obj.engine = { applyMix: () => {}, updateTrackMix: win.EngineBridge.setTrackMix };
  obj.rescheduleSamplesDebounced = () => {};
  obj._lastMixTrackCount = 3;
  obj.applyMixSafe();          // 三轨正常下发
  await sleep(120);
  mixCalls.length = 0;
  obj.tracks = tracks.slice(0, 1);   // 删除两条轨
  obj.applyMixSafe();
  await sleep(120);
  const deactivated = mixCalls.filter((c) => !c.active).map((c) => c.idx).sort();
  check(
    "被删槽位收到 active=false（防残留 solo 掐全轨）",
    deactivated.length === 2 && deactivated[0] === 1 && deactivated[1] === 2,
    `active=false 的槽位 = [${deactivated.join(",")}]`
  );
}

console.log("编曲窗音频引擎回归测试");
try {
  await testChordNotCollapsed();
  await testNoteOffAfterTimerScheduled();
  await testSf2TrackAdmitted();
  testScheduleSigIncludesBpm();
  await testMixSlotCleanup();
} catch (e) {
  failures.push("测试自身异常: " + (e && e.stack ? e.stack : e));
}

console.log(`\n通过 ${passed} 项，失败 ${failures.length} 项`);
if (failures.length) {
  console.log("失败明细：");
  failures.forEach((f) => console.log("  - " + f));
  process.exit(1);
}
