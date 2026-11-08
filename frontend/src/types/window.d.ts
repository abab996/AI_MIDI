/* 页面脚本加载完成后的 window 契约。签名按当前 JS 的导出和调用点写。
   迁移某个文件时只收紧类型，不改这些名字。 */

interface UIApi {
  openPluginUI(url: string, name: string, width?: number, height?: number): { readonly closed: boolean; close(): void; postMessage(message: unknown, targetOrigin: string): void } | null;
  download(url: string,name?: string): Promise<any>;
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
  /* 下载链接白名单：同源 + /api/ 前缀才返回可赋值地址，否则返回 "" */
  safeApiHref(u: unknown): string;
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
  getDecodeContext?(): BaseAudioContext | AudioContext | null;
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
  /* 引擎是否「明确不可用」（失败/停止/重启中）；unknown 与未取到状态不算 */
  isEngineDown(): boolean;
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
  saveSoundFont(name: string, arrayBuffer: ArrayBuffer, presets: Sf2Preset[], sourceId?: string): Promise<unknown>;
  getSoundFont(id: string): Promise<unknown>;
  recordForSource(sourceId: string, name?: string): Promise<SoundFontRecord | null>;
  deleteSoundFont(id: string, name?: string): Promise<unknown>;
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
  loadSoundFontWithProgress(track: number, path: string, onStatus?: (st: any) => void): Promise<any>;
  cancelSoundFontLoad(track: number): Promise<void>;
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
  clearNotes(): Promise<void>;
  bounce(params: Record<string, unknown>): Promise<string>;
  setLoop(on: boolean, start: number, end: number): Promise<void>;
  getLevels(): Promise<number[]>;
  /* P4 混音台：FX 链 / 主总线 / 主总线电平（track < 0 = 主总线） */
  setFxChain?(track: number, slots: Array<Record<string, unknown>>): Promise<void>;
  getFxChain?(track: number): Promise<Record<string, unknown> | null>;
  setMaster?(gain: number): Promise<void>;
  getMasterLevel?(): Promise<number>;
  getLevelsWithMaster?(): Promise<{ levels: number[]; master: number } | null>;
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
  /* 解码后占用字节估算（按内存上限淘汰用；解码完成前为 0） */
  bytes?: number;
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
  controller?: ArrangeController;
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

interface Window {
  ArrangementSync: { copy(x: any): any; equal(a: any, b: any): boolean; merge(b: any,l: any,r: any): {value:any;conflicts:string[]}; draft(method: string,project: string,value?: any): Promise<any>; chooseConflict(conflicts:string[]):Promise<string|null>;cancelConflict():void };
}

interface ArrangeController {
  projectId: string | null;
  projectEpoch: number;
  audioSync?: "waiting" | "ready" | "failed";
  pluginIssues?: { ref: string; trackId?: string; slot: number; master: boolean; error?: string }[];
  _audioSyncRequest?: number;
  _audioRequestedSignature?: string;
  _saves: { [project: string]: { base: any; inflight: Promise<any> | null; draft: any; lastError?: any } };
  saveConflict: { base: any; local: any; remote: any; conflicts: string[] } | null;
  _conflictDialog?: boolean;
  _stashDraft(): Promise<any>;
  _applyMergedState(data: any): void;
  _pauseForConflict(base: any, local: any, remote: any, conflicts: string[]): void;
  resolveSaveConflict(): void;
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
  /** 机架变化后刷新音色按钮显示（P2：钢琴窗音源也来自机架） */
  syncRackSource?(): void;
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

/** 输出机架（乐器箱）与音源选择器（arrangement/rack.ts） */
interface ArrRackApi {
  KIND_META: { [kind: string]: { label: string; short: string; badge: string; cls: string; icon: string } };
  RACK_FILTERS: { id: string; label: string }[];
  SYNTH_WAVES: { id: string; name: string }[];
  GM_PRESETS: { id: string; name: string; bank: number; program: number }[];
  BUILTIN_SYNTH: string;
  BUILTIN_GM: string;
  MAX_ITEMS: number;
  kindMeta(kind: string): { label: string; short: string; badge: string; cls: string; icon: string };
  kindLabel(kind: string): string;
  waveName(wave: string): string;
  gmPresetName(presetId: string): string;
  isBuiltinSourceId(sourceId: string): boolean;
  utf8Bytes(str: string): number[];
  fnv1a64(str: string): string;
  rackItemId(kind: string, sourceId: string, presetId: string, path: string): string;
  makeItem(kind: string, sourceId: string, presetId: string, name: string, path?: string): RackItem;
  synthItem(wave: string, name?: string): RackItem;
  gmItem(presetId: string, name?: string): RackItem;
  sampleItem(path: string, name?: string): RackItem;
  registryItem(src: any, presetId: string, name: string): RackItem;
  registryKindToRackKind(kind: string): string;
  rackKindToRegistryKind(kind: string): string;
  defaultPresetFor(kind: string): string;
  itemFromSourceId(sourceId: string, presetId: string, name: string): RackItem | null;
  migrateV1Source(src: any): RackItem | null;
  generalUserSourceId(): string;
  find(items: RackItem[] | any, itemId: string): RackItem | null;
  upsert(items: RackItem[] | any, item: RackItem): { item: RackItem | null; created: boolean };
  cloneItem(item: RackItem): RackItem;
  resolve(items: RackItem[] | any, source: TrackSource | null | undefined): ResolvedVoice;
  resolveSource(source: TrackSource | null | undefined): ResolvedVoice;
  presetList(item: RackItem | null): Promise<{ id: string; name: string }[]>;
  itemMetaText(item: RackItem | null): string;
  isAIItem(item: RackItem | null): boolean;
  isBuiltinItem(item: RackItem | null): boolean;
  voiceDetail(kind: string, item: RackItem, presetId: string): string;
  setItems(items: RackItem[] | any): void;
  items(): RackItem[];
  render(ctrl: any): void;
  visibleRows(ctrl: any, filter: string, query: string): { item: RackItem; derived: boolean }[];
  derivedSamples(ctrl: any): RackItem[];
  openPicker(opts: any): void;
  closePicker(): void;
  isPickerOpen(): boolean;
  refreshPicker(): void;
  openPickerFor(ctrl: any, opts: any): void;
  openPresetPane(item: RackItem): void;
  _picker: any;
  _pickerEls(): any;
}

declare const ArrRack: ArrRackApi;

/** 素材库三页签（arrangement/material.ts） */
interface ArrMaterialApi {
  TABS: { id: string; label: string }[];
  TREE_EXPANDED_KEY: string;
  MAX_CHILDREN: number;
  state: any;
  bind(ctrl: any): void;
  setTab(ctrl: any, tab: string, silent?: boolean): void;
  render(ctrl: any): void;
  loadDirs(ctrl: any): Promise<unknown>;
  refreshOpenNodes(ctrl: any): void;
  nodeKey(dir: any, sub: any): string;
  joinAbs(dir: any, sub: any, name: any): string;
  loadExpanded(): { [key: string]: boolean };
  saveExpanded(): void;
  toggleNode(ctrl: any, dir: any, sub: any): void;
  removeDir(ctrl: any, d: any): void;
  addFolder(ctrl: any): void;
  appendNode(ctrl: any, container: any, dir: any, sub: any, depth: number): void;
  buildSampleRow(ctrl: any, dir: any, sub: any, f: any, depth: number): HTMLElement;
  renderSamples(ctrl: any, body: any): void;
  renderSources(ctrl: any, body: any): void;
  renderEffects(ctrl: any, body: any): void;
  analyze(absPath: string): Promise<any>;
  computePeaks(buffer: any): number[];
  fmtDuration(sec: number): string;
  packNameFor(dir: string): string;
  hintFor(tab: string): string;
  _resetForTest(): void;
}

declare const ArrMaterial: ArrMaterialApi;

/** 通用插件参数面板 + 插件状态随工程保存（arrangement/plugin_panel.ts，P5） */
interface PluginPanelApi {
  /** 打开面板：target = { track, slot, master?, ref?, name? }；slot<0 表示该轨乐器 */
  open(ctrl: unknown, target: unknown): void;
  close(): void;
  /** 工程载入后把记录的插件装回引擎并恢复状态（浏览器模式静默跳过） */
  restoreAll(ctrl: unknown): Promise<any>;
  saveAll(ctrl: unknown): Promise<any>;
  retryMissing(ctrl: unknown): Promise<any>;
  flushAll(): void;
  duplicateState(ctrl: unknown, fromTrackId: string, toTrackId: string): Promise<any>;
  resolvePlugin(ref: string): Promise<any>;
  enqueue(ctrl: ArrangeController, task: ()=>Promise<any>, valid?: ()=>boolean): Promise<boolean>;
  isOpen(): boolean;
}

declare const PluginPanel: PluginPanelApi;

/** 混音台 v1（arrangement/mixer.ts，P4） */
interface ArrMixerApi {
  MAX_SLOTS: number;
  MIN_DB: number;
  MAX_DB: number;
  MASTER_TRACK: number;
  state: { open: boolean; raf: number; ids: string[]; peakHold: { [key: string]: number } };
  bind(ctrl: any): void;
  isOpen(): boolean;
  open(ctrl: any): void;
  close(ctrl: any): void;
  toggle(ctrl: any): void;
  render(ctrl: any): void;
  syncTracks(ctrl: any): void;
  refreshStrip(ctrl: any, idx: number): void;
  gainToDbText(gain: number): string;
  posToGain(pos: number): number;
  gainToPos(gain: number): number;
  levelToRatio(level: number): number;
  channelOf(ctrl: any, trackId: string): MixerChannel;
  masterOf(ctrl: any): MixerChannel;
  gainOf(ctrl: any, idx: number): number;
  panOf(ctrl: any, idx: number): number;
  setGain(ctrl: any, idx: number, gain: number, commit: boolean): void;
  setMasterGain(ctrl: any, gain: number, commit: boolean): void;
  toggleMute(ctrl: any, idx: number): void;
  toggleSolo(ctrl: any, idx: number): void;
  commitFx(ctrl: any, idx: number, slot: number, patch: any): void;
  paintMeters(ctrl: any): void;
  shortPluginName(ref: string): string;
  _resetForTest(): void;
}

declare const ArrMixer: ArrMixerApi;

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
  SoundRegistry: SoundRegistryApi;
  ArrRack: ArrRackApi;
  ArrMaterial: ArrMaterialApi;
  ArrMixer: ArrMixerApi;
  PluginPanel: PluginPanelApi;
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
  /* P4：主总线峰值电平（电平轮询一次取回轨电平 + 主电平） */
  __engineMasterLevel?: number;
  __engineState?: string;
  marked?: MarkedApi;
  chrome?: { webview?: object };
}
