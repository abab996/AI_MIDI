/* 页面脚本加载完成后的 window 契约。签名按当前 JS 的导出和调用点写。
   迁移某个文件时只收紧类型，不改这些名字。 */

interface UIApi {
  qs(sel: string, root?: ParentNode): Element | null;
  /* 现有调用都把结果当 HTML 元素（hidden、style）。迁 app.ts 时，
     querySelectorAll 的 Element[] 要在 return 上断言成 HTMLElement[]。 */
  qsa(sel: string, root?: ParentNode): DomEl[];
  toast?(message: unknown, kind?: string): void;
  /* 应用内确认弹窗（替代 window.confirm）：确认 true，取消/Esc/点遮罩 false */
  confirm(opts: string | {
    text: string;
    title?: string;
    okText?: string;
    cancelText?: string;
    danger?: boolean;
  }): Promise<boolean>;
  /* 应用内输入弹窗（替代 window.prompt）：确定返回输入值，取消/Esc 为 null */
  prompt(title: string, defaultValue?: string): Promise<string | null>;
  esc(s: unknown): string;
  fmtSize(bytes: number): string;
  fmtDate(s: string): string;
  friendlyText(text: unknown): string;
  /* 本机接入点（localhost/127.0.0.1/::1）：本地推理服务不需要 API Key */
  isLocalEndpoint?(baseURL: unknown): boolean;
  getJSON<T = unknown>(url: string): Promise<T>;
  postJSON<T = unknown>(url: string, body?: unknown): Promise<T>;
  putJSON<T = unknown>(url: string, body?: unknown): Promise<T>;
  delJSON<T = unknown>(url: string, body?: unknown): Promise<T>;
  openExternal?(url: string): Promise<unknown>;
  ssePost<T = unknown>(
    url: string,
    body: unknown,
    onEvent: (ev: T) => void,
    opts?: SseOpts
  ): Promise<unknown>;
  md(text: unknown): string;
  mdInline(text: unknown): string;
  projectCard(proj: ProjectSummary): HTMLElement;
  streamStart(
    kind: string,
    body: StreamStartBody,
    onEvent: (ev: unknown) => void,
    onEnd: () => void,
    opts?: SseOpts
  ): Promise<unknown>;
  transportPrefs(): TransportPrefs;
  setTransportPref(key: string, val: boolean): void;
  onTransportPrefs(fn: (prefs: TransportPrefs) => void): void;
}

interface ShortcutBinding {
  action: string;
  keys: string[];
  group: string;
  label: string;
}

interface ShortcutsApi {
  all(): ShortcutBinding[];
  groups(): { key: string; label: string; items: ShortcutBinding[] }[];
  get(action: string): string[];
  matches(e: KeyboardEvent, action: string): boolean;
  normalizeKey(e: KeyboardEvent): string;
  pretty(specOrList: string | string[]): string;
  set(action: string, spec: string | string[]): string | null;
  reset(action: string): void;
  resetAll(): void;
  label(action: string): string;
  STORAGE_KEY: string;
}

interface TasksApi {
  refresh(): Promise<unknown>;
  applyBadges(): void;
  setCurrentProject(pid: string | null): void;
  markProjectRead(pid: string): void;
  hasActiveTasks(pid: string | null): boolean;
  findRunningTask(pid: string): BackgroundTask | null;
  onProjectTaskChange: ((changed: BackgroundTask[]) => void) | null;
}

interface SharedAudioApi {
  get(): AudioContext | null;
  resume(): Promise<void>;
  now(): number;
  dispose(): void;
}

interface AudioBackendApi {
  getMode(): string;
  setMode(mode: string): Promise<{ ok: boolean } | void>;
  isNativeAvailable(): boolean;
  mode(): "engine" | "webaudio";
  isEngine(): boolean;
  isWebAudio(): boolean;
  isEngineReady(): boolean;
  isNativePreferred(): boolean;
}

interface SynthEngineInstance {
  ctx: AudioContext | null;
  waveform: string;
  attack: number;
  decay: number;
  sustain: number;
  release: number;
  cutoff: number;
  resonance: number;
  volume: number;
  isMuted: boolean;
  init(): void;
  resume(): void | Promise<void>;
  midiToFreq(midiNote: number): number;
  setWaveform(type: string): void;
  setVolume(vol: number): void;
  setFilter(cutoff: number, res: number): void;
  noteOn(midiNote: number, velocity: number, when?: number): void;
  noteOff(midiNote: number, when?: number): void;
  stopAll(): void;
  playClick(isHigh: boolean, when?: number): void;
}

interface SynthEngineConstructor {
  new (sharedCtx?: AudioContext | null, outNode?: AudioNode | null): SynthEngineInstance;
  prototype: SynthEngineInstance;
}

interface SoundFontPlayerInstance {
  init(): void;
  resume(): void | Promise<void>;
  setVolume(vol: number): void;
  parseSF2(arrayBuffer: ArrayBuffer): { name: string; presets: Sf2Preset[] };
  setPreset(presetOrBuiltinId: string): void;
  noteOn(midiNote: number, velocity: number, when?: number): void;
  noteOff(midiNote: number, when?: number): void;
  stopAll(): void;
  loadedPresets?: Sf2Preset[];
  currentPreset?: Sf2Preset | null;
}

interface SoundFontPlayerConstructor {
  new (sharedCtx?: AudioContext | null, outNode?: AudioNode | null): SoundFontPlayerInstance;
  prototype: SoundFontPlayerInstance;
}

interface SoundLibraryApi {
  db: IDBDatabase | null;
  _cachedFonts: SoundFontRecord[];
  _initPromise: Promise<IDBDatabase> | null;
  init(): Promise<IDBDatabase>;
  getCachedSoundFonts(): SoundFontRecord[];
  listSoundFonts(): Promise<SoundFontRecord[]>;
  saveSoundFont(name: string, arrayBuffer: ArrayBuffer, presets: Sf2Preset[]): Promise<unknown>;
  getSoundFont(id: string): Promise<unknown>;
  deleteSoundFont(id: string, name?: string): Promise<unknown>;
  showImportDialog?(onDone?: () => void): void;
}

interface EngineBridgeApi {
  available: boolean;
  PERF_TRACK: number;
  SF2_TRACK: number;
  PREVIEW_TRACK: number;
  setTrackVoice(track: number, voice: TrackVoice | null | undefined): Promise<void>;
  setTrackPreset(track: number, bank: number, program: number): Promise<void>;
  click(track: number, high: boolean): Promise<void>;
  noteOn(channel: number, key: number, velocity: number): Promise<void> | undefined;
  noteOff(channel: number, key: number): Promise<void> | undefined;
  loadSoundFont(path: string, track?: number): Promise<void>;
  noteOnTrack(track: number, key: number, velocity: number): Promise<void> | undefined;
  noteOffTrack(track: number, key: number): Promise<void> | undefined;
  setTrackMix(track: number, gain: number | undefined, pan: number | undefined, mute: boolean, solo: boolean, active: boolean | undefined): Promise<void>;
  play(): Promise<void>;
  stop(): Promise<void>;
  locate(beat: number): Promise<void>;
  setTempo(bpm: number): Promise<void>;
  getTimecode(): Promise<EngineTimecodeWire>;
  scheduleSamples(clips: Array<Record<string, unknown>>, bpm: number): Promise<void>;
  clearSamples(): Promise<void>;
  scheduleNotes(notes: Array<Record<string, unknown>>, bpm: number): Promise<void>;
  bounce(params: Record<string, unknown>): Promise<string>;
  setLoop(on: boolean, start: number, end: number): Promise<void>;
  getLevels(): Promise<number[]>;
  panic(): Promise<void>;
  getBackend(): string;
  isNativePreferred(): boolean;
}

interface MidiInputRouterApi {
  settings: MidiInputSettings;
  heldNotes: { [note: number]: boolean };
  baseOctave: number;
  loadSettings(): MidiInputSettings;
  updateSettings(next: Partial<MidiInputSettings>): void;
  addListener(fn: (type: string, note: number, velocity: number) => void): void;
  removeListener(fn: (type: string, note: number, velocity: number) => void): void;
  emit(type: string, note: number, velocity: number): void;
}

interface ScaleDef {
  name: string;
  intervals: number[];
}

interface PianoRollToolsApi {
  SCALES: { [key: string]: ScaleDef };
  CHORDS: { [key: string]: ScaleDef };
  NOTE_NAMES: string[];
  noteNameToNumber(name: string): number;
  numberToNoteName(num: number): string;
  isNoteInScale(midiNote: number, rootPitch: number, scaleKey: string): boolean;
  stampChord(rootMidiNote: number, chordTypeKey: string, startBeat: number, durationBeat: number, velocity: number): MidiNote[];
  strum(notes: MidiNote[] | null | undefined, timeOffsetBeat: number, velRamp: number, alternateDir: boolean): MidiNote[] | null | undefined;
  arpeggiate(notes: MidiNote[], pattern: string, stepBeat: number, gate?: number): MidiNote[];
  levelScale(notes: MidiNote[], multiply: number, offset: number, rampStart: number, rampEnd: number): MidiNote[];
  randomize(notes: MidiNote[], velRange: number, pitchRange: number, timeRange: number): MidiNote[];
  flip(notes: MidiNote[], mode: string): MidiNote[];
  quantize(notes: MidiNote[], gridStepBeat: number, quantizeEnd: boolean, swing?: number): MidiNote[];
  transpose(notes: MidiNote[] | null | undefined, semitones: number): MidiNote[] | null | undefined;
  legato(notes: MidiNote[] | null | undefined): MidiNote[] | null | undefined;
}

interface SampleCacheEntry {
  promise: Promise<SampleCacheEntry | undefined>;
  buffer: AudioBuffer | null;
  peaks: Float32Array | null;
  lastUse: number;
}

interface ArrangeTrackNodes {
  gain: GainNode | null;
  analyser?: AnalyserNode | null;
  synth?: SynthEngineInstance | null;
  soundfont?: SoundFontPlayerInstance | null;
  sourceKey?: string | null;
  _useNative?: boolean;
  _isNativeSynth?: boolean;
  _voiceRetryAt?: number;
  _lvlBuf?: Float32Array;
}

interface ArrangeEngineInstance {
  ctx: AudioContext | null;
  bpm: number;
  metronome: boolean;
  loop: ArrangeLoop;
  isPlaying: boolean;
  trackNodes: { [trackId: string]: ArrangeTrackNodes };
  _nativeSamplesFailed: boolean;
  getTracks: (() => ArrangeTrack[]) | null;
  onSoundFontLoaded: ((trackId?: string, info?: any) => void) | null;
  onSoundFontError: ((trackId?: string, err?: any) => void) | null;
  onEngineLost: (() => void) | null;
  resume(): Promise<void>;
  applyMix(tracks: ArrangeTrack[]): void;
  stopTrackSources(trackId: string): void;
  play(startBeat: number): void | Promise<unknown>;
  currentBeat(): number;
  stopSchedule(): void;
  trackLevel(trackId: string): number;
  ensureTrack(track: ArrangeTrack): unknown;
  getSampleEntry(absPath: string): Promise<SampleCacheEntry | undefined>;
  updateTrackMix(trackIndex: number, gainVal: number, panVal: number, isMute: boolean, isSolo: boolean, isActive: boolean): void;
  previewSample(absPath: string): Promise<void>;
  click(when: number, isDownbeat: boolean): void;
}

interface ArrangeEngineConstructor {
  new (): ArrangeEngineInstance;
  prototype: ArrangeEngineInstance;
}

interface ArrangeController {
  projectId: string | null;
  bpm: number;
  tracks: ArrangeTrack[];
  setProject(projectId: string | null): void;
  refreshMidiList(files: unknown): void;
  syncFiles(files: unknown): void;
  cleanState(): ArrangeState;
  serialize(): ArrangeState;
  deserialize(data: unknown): void;
}

interface PianoRollController {
  isOpen: boolean;
  soundfont: SoundFontPlayerInstance;
  synth: SynthEngineInstance;
  openFile(projectId: string, fileName: string, filePath: string): void;
  closeModalAnimated(modalEl: HTMLElement): void;
  refreshSoundLibraryList(): void;
}

interface MarkedHtmlToken {
  text?: unknown;
  href?: unknown;
}

interface MarkedRendererInstance {
  html?: (this: unknown, token: MarkedHtmlToken | null) => string;
  link: (this: unknown, a?: MarkedHtmlToken | string | null, b?: unknown, c?: unknown) => string;
}

interface MarkedApi {
  parse(src: string, options?: { breaks?: boolean; gfm?: boolean }): string;
  use(options: { renderer?: MarkedRendererInstance }): void;
  Renderer: {
    new (): MarkedRendererInstance;
    prototype: MarkedRendererInstance;
  };
}

/** 品牌图标表（brand-icons.ts 生成，见 tools/dev/gen-brand-icons.mjs） */
interface BrandIconsApi {
  /** 建一个图标节点；查不到品牌时给首字母方块 */
  node(brand: string, fallbackText?: string): HTMLElement;
  /** 供应商行用：按预设 id 查图标 */
  forPreset(presetId: string, fallbackText?: string): HTMLElement;
  /** 模型行用：按模型 ID 认品牌，认不出退回所属供应商的预设 */
  forModel(modelId: string, fallbackPresetId?: string, fallbackText?: string): HTMLElement;
  matchBrand(modelId: string): string;
  brands(): string[];
}

/** 经典脚本里 window.UI 同时是裸全局。app.ts 在赋值完成前就会在函数体里写 UI.ssePost。 */
declare const UI: UIApi;
declare const AudioBackend: AudioBackendApi;
declare const Tasks: TasksApi;
declare const BrandIcons: BrandIconsApi;

/** 自动更新检查（update.js）：手动触发检查用 */
interface UpdateApi {
  /** 检查更新；有更新会弹自带通知卡。resolve 值：UpdateInfo 或 { error } */
  checkNow(): Promise<UpdateInfo | { error: string } | null>;
}

interface Window {
  UI: UIApi;
  BrandIcons: BrandIconsApi;
  Update?: UpdateApi;
  Shortcuts: ShortcutsApi;
  Tasks: TasksApi;
  SharedAudio: SharedAudioApi;
  AudioBackend: AudioBackendApi;
  SynthEngine: SynthEngineConstructor;
  SoundFontPlayer: SoundFontPlayerConstructor;
  SoundLibrary: SoundLibraryApi;
  EngineBridge: EngineBridgeApi;
  MidiInputRouter: MidiInputRouterApi;
  PianoRollTools: PianoRollToolsApi;
  MidiParse: {
    parseBytes(arrayBuffer: ArrayBuffer): MidiNote[];
    numberToNoteName(num: number): string;
    noteNameToNumber(name: string): number;
  };
  PianoRoll: PianoRollController;
  ArrangeEngine: ArrangeEngineConstructor;
  Arrange: ArrangeController;
  __openProject?: (projectId: string, taskId: string) => void;
  __engineTimecode?: EngineTimecode;
  __engineLevels?: number[];
  __engineBackend?: string;
  __engineState?: string;
  marked?: MarkedApi;
  chrome?: { webview?: object };
}
