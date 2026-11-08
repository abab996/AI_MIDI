/* 跨文件数据形状。运行时脚本不要 import 本文件，这些名字在全局可见。
   字段跟现有 JS 走：存档里多出来的键用可选属性或索引签名收下。 */

interface MidiNote {
  note: string;
  velocity: number;
  start: number;
  end: number;
  muted?: boolean;
}

interface TransportPrefs {
  resumeOnPause: boolean;
  loaded: boolean;
  [key: string]: boolean;
}

interface ProjectSummary {
  id: string;
  name?: string;
  message_count?: number;
  updated_at?: string;
}

interface EngineTimecode {
  beat: number;
  samplePos: number;
  bpm: number;
  playing: boolean;
  /** 前端轮询写入的 performance.now()。Go 返回的时间码没有这一项。 */
  t: number;
}

interface EngineTimecodeWire {
  beat: number;
  samplePos: number;
  bpm: number;
  playing: boolean;
}

interface TrackVoice {
  wave?: string;
  attack?: number;
  decay?: number;
  sustain?: number;
  release?: number;
  cutoff?: number;
  resonance?: number;
  gain?: number;
  volume?: number;
}

interface TrackSource {
  /** v2：机架条目引用（音源只能来自机架） */
  itemId?: string;
  /** v2：该轨使用的预设（bank:program / 波形名 / 插件预设名） */
  presetId?: string;
  /* v1 遗留字段（迁移后不再写入，兼容读取老工程） */
  type?: string;
  wave?: string;
  tone?: string;
  libId?: string;
  label?: string;
  [key: string]: unknown;
}

/** 机架条目（乐器箱，arrangement.rack.items[i]） */
interface RackItem {
  id: string;
  kind: "synth" | "sf2" | "pack" | "vst3" | "mm" | "sample" | string;
  sourceId: string;
  presetId?: string;
  name: string;
  /** sample 专用：采样文件绝对路径 */
  path?: string;
  pinned?: boolean;
  /** 运行时标记：由工程内音频 clip 派生，未持久化 */
  derived?: boolean;
}

/** 机架音源解析结果（ArrRack.resolve） */
interface ResolvedVoice {
  ok: boolean;
  missing: boolean;
  item: RackItem | null;
  itemId: string;
  presetId: string;
  kind: string;
  label: string;
  detail: string;
  /** 发声链重建判据：itemId + presetId */
  key: string;
  voice: {
    type: "synth" | "sf2" | "builtin" | "sample" | "plugin" | "pack" | "none" | string;
    wave?: string;
    tone?: string;
    sourceId?: string;
    presetId?: string;
    path?: string;
    pluginKind?: string;
    missing?: boolean;
  };
}

/** arrangement.mixer 的通道（P4 混音台消费；P2 先落 pan/gain/mute/solo） */
interface MixerChannel {
  gain?: number;
  pan?: number;
  mute?: boolean;
  solo?: boolean;
  fx?: { slot: number; plugin: string; sourceId?: string; format?: string; bypass?: boolean; wet?: number }[];
}

interface ArrangeLoop {
  on: boolean;
  start: number;
  end: number;
  /** 标尺拖出循环时的临时锚点，松手后删掉。 */
  anchor?: number;
}

interface ArrangeClipBase {
  id: string;
  name: string;
  start: number;
  length: number;
  mute: boolean;
  fadeIn: number;
  fadeOut: number;
  gain?: number;
  /** 运行时字段，cleanState 不写出。 */
  _rev?: number;
  _peaks?: Float32Array | null;
}

interface ArrangeMidiClip extends ArrangeClipBase {
  type: "midi";
  fullName?: string;
  notes?: MidiNote[];
}

interface AudioClipSource {
  p: string;
  [key: string]: unknown;
}

interface ArrangeAudioClip extends ArrangeClipBase {
  type: "audio";
  src: AudioClipSource | null;
  offset?: number;
}

type ArrangeClip = ArrangeMidiClip | ArrangeAudioClip;

interface ArrangeTrack {
  id: string;
  name: string;
  color: string;
  mute: boolean;
  solo: boolean;
  volume: number;
  source: TrackSource;
  clips: ArrangeClip[];
}

/** Arrange.prototype.cleanState 的返回值（arrangement v2）。 */
interface ArrangeState {
  version: number;
  /** 服务端单调推进的修订号（SSE/保存回执带回，用于判断新鲜度） */
  rev?: number;
  bpm: number;
  snap: number;
  ppb: number;
  loop: ArrangeLoop;
  tracks: ArrangeTrack[];
  /** 项目乐器箱：轨道与钢琴窗的音源只能取自这里 */
  rack?: { items: RackItem[] };
  /** 钢琴窗音源选择（跨会话保持） */
  pianoRoll?: { itemId: string; presetId?: string };
  /** 混音台状态（P4；P2 起登记 gain/pan/mute/solo） */
  mixer?: { channels: { [trackId: string]: MixerChannel }; master?: { gain?: number } };
}

interface BackgroundTask {
  id: string;
  project_id: string;
  status: "running" | "needs_confirmation" | "completed" | string;
  read?: boolean;
}

interface Sf2Preset {
  id: string;
  name: string;
  bank: number;
  program: number;
  fontName?: string;
  zones?: any[] | null;
  sampleIndices?: number[];
}

interface SoundFontRecord {
  id: string;
  name: string;
  size?: number;
  presetsCount?: number;
  presets?: Sf2Preset[];
  diskPath?: string;
  /* 音源注册表条目 id：本地解码缓存与注册表之间的桥
     （浏览器模式发声时按它反查，历史记录可能没有） */
  sourceId?: string;
  createdAt?: number;
}

/* ===== 音源注册表（/api/sound/*，实现见 frontend/src/soundlib.ts） ===== */

interface SoundSourceInfo {
  id: string;
  kind: "sf2" | "samplepack" | "vst3" | "mm" | string;
  name: string;
  vendor?: string;
  origin: "builtin" | "user" | "ai" | string;
  path?: string;
  dirs?: string[];
  category?: string;
  classUid?: string;
  capabilities?: string[];
  hash?: string;
  ai?: { author?: string; createdBy?: string };
  enabled: boolean;
  tags?: string[];
  addedAt?: number;
  updatedAt?: number;
  /* 磁盘实况（后端每次列举重算，不落盘） */
  runtime?: {
    absPath?: string;
    fileName?: string;
    missing?: boolean;
    size?: number;
    presetCount?: number;
    sampleDirs?: number;
    sampleFiles?: number;
  };
}

interface SoundKindMeta {
  label: string;
  shortLabel: string;
  badge: string;
  icon: string;
  cls: string;
}

interface SoundRegistryApi {
  /* 内部状态（缓存/在途请求/订阅者） */
  _sources: SoundSourceInfo[];
  _loaded: boolean;
  _loading: Promise<SoundSourceInfo[]> | null;
  _presetCache: { [id: string]: Sf2Preset[] | undefined };
  _presetInflight: { [id: string]: Promise<Sf2Preset[]> | undefined };
  _listeners: Array<() => void>;
  _emit(): void;
  kindMeta(kind: string): SoundKindMeta;
  kindOrder(): string[];
  isPluginKind(kind: string): boolean;
  isAI(src: any): boolean;
  isBuiltin(src: any): boolean;
  originLabel(src: any): string;
  onChange(fn: () => void): void;
  load(force?: boolean): Promise<SoundSourceInfo[]>;
  reload(): Promise<SoundSourceInfo[]>;
  all(): SoundSourceInfo[];
  list(kind?: string): SoundSourceInfo[];
  get(id: string): SoundSourceInfo | null;
  enabledOf(kind?: string): SoundSourceInfo[];
  query(kind: string, q: string): SoundSourceInfo[];
  countByKind(kind: string): number;
  presets(id: string): Promise<Sf2Preset[]>;
  firstPreset(id: string): string | null;
  parsePresetId(presetId: any): { bank: number; program: number };
  importSf2(file: File, onProgress?: (stage: string) => void): Promise<any>;
  addSamplePack(name: string, dirs: string[]): Promise<any>;
  update(id: string, patch: any): Promise<any>;
  setEnabled(id: string, enabled: boolean): Promise<any>;
  remove(id: string, opts?: { file?: boolean }): Promise<any>;
  rescan(): Promise<any>;
  samples(packId: string, sub?: string): Promise<any>;
  diskPathOf(src: any): string;
  browserRecord(src: any): Promise<SoundFontRecord | null>;
  loadSf2(sourceId: string,libId?: string): Promise<any>;
  metaText(src: any): string;
}

interface MidiInputSettings {
  enabled: boolean;
  deviceId: string;
  channel: string;
  velocityCurve: string;
  typingKeyboard: boolean;
}
