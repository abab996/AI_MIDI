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
  type?: string;
  wave?: string;
  label?: string;
  [key: string]: unknown;
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

/** Arrange.prototype.cleanState 的返回值。 */
interface ArrangeState {
  version: number;
  bpm: number;
  snap: number;
  ppb: number;
  loop: ArrangeLoop;
  tracks: ArrangeTrack[];
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
  createdAt?: number;
}

interface MidiInputSettings {
  enabled: boolean;
  deviceId: string;
  channel: string;
  velocityCurve: string;
  typingKeyboard: boolean;
}
