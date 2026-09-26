/* 单个页面脚本里用到的类型。放在这里是为了不把 interface 写进运行时文件
   （擦除时 interface 会在 JS 里留下分号）。 */

/* titlebar.js 对 event.target 直接调用 closest。DOM 类型里 target 是 EventTarget，
   没有 closest。补上之后源码可以写成 e.target!.closest，擦除结果与原来的
   e.target.closest 只差空白。 */
interface Element {
  style: CSSStyleDeclaration;
  _mdSrc?: string;
  value: string;
  disabled?: boolean;
  checked: boolean;
  href?: string;
  files?: FileList | null;
  selectedIndex?: number;
  indeterminate?: boolean;
  src?: string;
  innerText?: string;
  dataset: DOMStringMap;
}

interface HTMLElement {
  select?(): void;
  open?: boolean;
}

interface EventTarget {
  closest?(selector: string): Element | null;
  classList?: DOMTokenList;
  files?: FileList | null;
  checked: boolean;
  /* 现有脚本把 event.target 当成元素或 IDB 请求来读。 */
  tagName?: string;
  isContentEditable?: boolean;
  result?: any;
  error?: any;
}

interface Sf2Reader {
  view: DataView;
  pos: number;
  length: number;
  readFourCC(): string;
  readUint32(): number;
  readUint16(): number;
  readInt16(): number;
  readUint8(): number;
  readFixedString(len: number): string;
}

interface Window {
  AudioContext: typeof AudioContext;
  webkitAudioContext?: typeof AudioContext;
}

/* 现有脚本把尚未启动的计时器句柄（null）直接交给 clearTimeout / clearInterval。 */
declare function clearTimeout(timeoutId: number | null | undefined): void;
declare function clearInterval(intervalId: number | null | undefined): void;

interface WorkbenchEl extends HTMLElement {
  value: string;
  disabled: boolean;
  files: FileList | null;
  href: string;
  checked: boolean;
}

interface RunEvent {
  type?: string;
  desc?: string;
  message?: string;
  status?: string;
  result?: string;
  download_url?: string;
  /* /api/run 注册进任务注册表后随事件下发，供停止按钮调用 stop 端点 */
  task_id?: string;
  /* 后端错误分类（auth/rate_limit/network/timeout/param/upstream） */
  code?: string;
  detail?: string;
}

interface SseOpts {
  signal?: AbortSignal | null;
  timeoutMs?: number;
}

interface StreamStartBody {
  project_id?: string | null;
  question_id?: string | null;
  answers?: unknown;
  message?: string | null;
  edit?: boolean;
  task_id?: string | null;
}

interface SettingsResponse {
  transport_resume_on_pause?: boolean;
}

/* 页面脚本把 querySelector 的结果当成表单控件用。 */
interface DomEl extends HTMLElement {
  value: string;
  checked: boolean;
  disabled: boolean;
  files: FileList | null;
  href: string;
  selectedIndex: number;
  indeterminate: boolean;
  src: string;
  innerText: string;
}

interface SettingsStamp {
  api_key?: string;
}

interface ParseResult {
  note_table: unknown[];
  status: string;
}

interface UpdateInfo {
  available?: boolean;
  mandatory?: boolean;
  latest: string;
  current: string;
  date?: string;
  notes?: string;
}

interface UpdateApplyResult {
  mode?: string;
}

interface UpdateProgress {
  state?: string;
  downloaded?: number;
  total?: number;
  launched?: boolean;
  error?: string;
}
