/* 桌面模式由 Wails 注入的绑定。与 frontend/wailsjs/go/app/App.d.ts、
   frontend/wailsjs/runtime/runtime.d.ts 保持同一组方法名。
   这里手写是因为生成文件是 ESM，而页面用经典脚本，不能 import。
   浏览器模式没有 go / runtime，所以都是可选的。 */

interface WailsApp {
  AnswerStreamStart(arg1: string, arg2: string, arg3: unknown, arg4: string): Promise<void>;
  ChatStreamStart(arg1: string, arg2: string, arg3: boolean, arg4: string, arg5: string): Promise<void>;
  EngineBounce(arg1: Record<string, unknown>): Promise<string>;
  EngineClearSamples(): Promise<void>;
  EngineClick(arg1: number, arg2: boolean): Promise<void>;
  EngineGetLevels(): Promise<number[]>;
  EngineGetTimecode(): Promise<EngineTimecodeWire>;
  EngineLoadSoundFont(arg1: string): Promise<void>;
  EngineLoadSoundFontTrack(arg1: number, arg2: string): Promise<void>;
  EngineLocate(arg1: number): Promise<void>;
  EngineNoteOff(arg1: number, arg2: number): Promise<void>;
  EngineNoteOffTrack(arg1: number, arg2: number): Promise<void>;
  EngineNoteOn(arg1: number, arg2: number, arg3: number): Promise<void>;
  EngineNoteOnTrack(arg1: number, arg2: number, arg3: number): Promise<void>;
  EnginePanic(): Promise<void>;
  EnginePlay(): Promise<void>;
  EngineScheduleNotes(arg1: Array<Record<string, unknown>>, arg2: number): Promise<void>;
  EngineScheduleSamples(arg1: Array<Record<string, unknown>>, arg2: number): Promise<void>;
  EngineSetLoop(arg1: boolean, arg2: number, arg3: number): Promise<void>;
  EngineSetTempo(arg1: number): Promise<void>;
  EngineSetTrackMix(arg1: number, arg2: number, arg3: number, arg4: boolean, arg5: boolean, arg6: boolean): Promise<void>;
  EngineSetTrackPreset(arg1: number, arg2: number, arg3: number): Promise<void>;
  EngineSetTrackVoice(arg1: number, arg2: object): Promise<void>;
  EngineStop(): Promise<void>;
  SelectFolderDialog(): Promise<string>;
  Startup(arg1: unknown): Promise<void>;
}

interface WailsRuntime {
  EventsOn(eventName: string, callback: (...data: unknown[]) => void): () => void;
  EventsOff(eventName: string, ...additionalEventNames: string[]): void;
  EventsEmit(eventName: string, ...data: unknown[]): void;
  /* titlebar.js 用到的窗口控制。缺失时标题栏按方法粒度隐藏按钮。 */
  WindowMinimise(): void;
  WindowToggleMaximise(): void;
  WindowIsMaximised(): Promise<boolean>;
  Quit(): void;
}

interface Window {
  go?: {
    app?: {
      App?: WailsApp;
    };
  };
  runtime?: WailsRuntime;
}
