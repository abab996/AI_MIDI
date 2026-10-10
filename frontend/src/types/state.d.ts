/* 控制器内部状态。由各构造函数与原型方法整理，供 window 契约合并。 */

interface DragState {
  mode?: string;
  type?: string;
  historyPushed?: boolean;
  isRightClick?: boolean;
  isSelectionMode?: boolean;
  startVel?: number;
  lastVel?: number;
  curVel?: number;
  lastBeat?: number;
  origEnd?: number;
  initialVelocities?: { note: MidiNote; vel: number }[];
  clipEl?: Element | null;
  found?: LocatedClip | null;
  startX?: number;
  startY?: number;
  curX?: number;
  curY?: number;
  grabOffsetPx?: number;
  origStart?: number;
  origLength?: number;
  origFadeIn?: number;
  origFadeOut?: number;
  spb?: number;
  lastThumbDraw?: number;
  changed?: boolean;
  movedToTrackIdx?: number;
  clonePending?: boolean;
  peOff?: boolean;
  pendingHistory?: string | null;
  freeSnap?: boolean;
  startBeat?: number;
  startPitch?: number;
  curBeat?: number;
  curPitch?: number;
  startClientX?: number;
  startClientY?: number;
  origScrollX?: number;
  origScrollY?: number;
  anchored?: boolean;
  note?: MidiNote;
  snapshot?: string;
  origNotes?: { note: MidiNote; start: number; end: number; pitch: number }[];
  /* 新建音符的延迟 resize：拖动超过阈值前不改变音符长度 */
  pendingResize?: boolean;
  pendingStartBeat?: number;
  pendingStartPitch?: number;
}

interface RecordConfig {
  countIn: number;
  replaceMode: boolean;
  quantizeOnRecord: boolean;
  metronomeOnRecord: boolean;
}

interface GridPos {
  mx: number;
  my: number;
  beat: number;
  pitch: number;
  inRuler: boolean;
}

interface ArrangeEls {
  arrRulerCanvas?: HTMLCanvasElement | null;
  [id: string]: HTMLElement | null | undefined;
}

interface ArrangeClipboardEntry {
  clip: ArrangeClip;
  srcTrack: number;
}

interface ArrangeClipboard {
  entries: ArrangeClipboardEntry[];
  baseSrc: number;
  type: string;
}

interface RulerDrag {
  mode: string;
  anchor?: number;
  wasPlaying?: boolean;
}

interface LiveRecordNote {
  noteObj: MidiNote;
  startBeat: number;
  pressTime: number;
}

interface DeleteEffect {
  start: number;
  end: number;
  pitch: number;
  startTime: number;
  duration: number;
}

interface PrDropdown {
  setValue(val: string, label?: string): void;
  updateOptions(newOpts: { value: string; label: string }[], val?: string): void;
  close(): void;
}

interface VelocityGroup {
  start: number;
  maxEnd: number;
  notes: MidiNote[];
  zoneStart: number;
  zoneEnd: number;
}

interface PosBeatSeg {
  beatFrom: number;
  beatTo: number;
  posFrom: number;
}

interface PosSpan {
  posFrom: number;
  beatFrom: number;
  span: number;
}

interface PendingNoteOn {
  t: number;
  midi: number;
  vel: number;
  dur: number;
  nodes: ArrangeTrackNodes;
  trackId: string | null;
}

interface PendingNoteOff {
  t: number;
  midi: number;
  nodes: ArrangeTrackNodes;
  trackId: string | null;
}

interface PendingClick {
  t: number;
  downbeat: boolean;
}

interface PendingAudioClip {
  t: number;
  tEnd: number;
  playFromBeat: number;
  clipRemain: number;
  clip: ArrangeClip;
  track: ArrangeTrack;
  nodes: ArrangeTrackNodes;
}

interface HeartbeatHandle {
  worker: Worker;
  start(cb: (ev: MessageEvent) => void): void;
  stop(): void;
  dispose(): void;
}

interface ActiveAudioSource {
  src: AudioBufferSourceNode;
  gain: GainNode;
  trackId: string;
}

interface EngineNativeTimer {
  fired: boolean;
  timer: number;
}

interface PianoNativeTimer {
  trk: number;
  p: number;
  noteOnFired: boolean;
  noteOffFired: boolean;
  noteOnTimer?: number;
  noteOffTimer?: number;
}

interface SynthVoice {
  osc: OscillatorNode;
  gain: GainNode;
  startTime: number;
  peakGain: number;
}

interface SfVoice {
  source: AudioBufferSourceNode;
  gain: GainNode;
  startTime: number;
  startGain: number;
}

interface SfRawSample {
  name: string;
  start: number;
  end: number;
  startLoop: number;
  endLoop: number;
  sampleRate: number;
  originalPitch: number;
  pitchCorrection: number;
}

interface SfSampleEntry {
  buffer: AudioBuffer;
  info: SfRawSample;
}

interface BuiltinSample {
  buffer: AudioBuffer;
  basePitch: number;
}

interface DropTarget {
  trackIdx: number;
  startBeat: number;
  zone: string;
}

interface TreeEntry {
  name: string;
  p?: string;
  [key: string]: unknown;
}

interface TreeCacheNode {
  dirs: TreeEntry[];
  files: TreeEntry[];
  _loading?: boolean;
}

interface ArrangeController {
  _confirmFn: (() => void) | null;
  _currentRackFilter: string;
  /* 机架窗口内的搜索关键词（P2） */
  _rackQuery: string;
  /* 已尝试拉过预设的音源 id：拉取失败后不再重试，避免重绘死循环 */
  _presetTried?: { [sourceId: string]: boolean };
  _exporting: boolean;
  _followSuspendUntil: number;
  _lastMixTrackCount?: number;
  _lastSampleSig: string;
  _mixSyncTimer: number | null;
  _modalToken: number;
  _pruneTrackNodes(): void;
  _renameIdx: number | null;
  _reschedTimer: number | null;
  _resumeSaveFailed: boolean;
  _rulerPointerBound: boolean;
  _srcDot(color: any): unknown;
  _thumbTimer: number | null;
  _topOverlay(): HTMLElement | null;
  _treeCache: { [key: string]: TreeCacheNode };
  addAudioClipFromDrop(trackIdx: any, absPath: any, name: any, startBeat: any): void;
  addMidiClipFromDrop(trackIdx: any, fileName: any, startBeat: any): unknown;
  addTrack(): void;
  applyMasterVolume(v: number): void;
  appendTreeNode(container: any, dir: any, sub: any, depth: any): void;
  applyMixSafe(): void;
  barSnap(beat: any): number;
  beginClipGesture(e: any, clipEl: any, mode: any): void;
  bindFocusManager(): void;
  bindKeyboard(): void;
  bindModals(): void;
  bindOutputRack(): void;
  bindPanels(): void;
  bindSnapDropdown(): void;
  bindToggle(): void;
  bindTracksArea(): void;
  bindTransport(): void;
  buildClipEl(track: any, clip: any): HTMLElement;
  buildSampleRow(dir: any, sub: any, f: any, depth: any): HTMLElement;
  buildTrackHead(track: any, idx: any): HTMLElement;
  canSplitRel(clip: any, rel: any): unknown;
  clearDropHighlight(): void;
  clearSelection(): void;
  clearSourceHighlight(): void;
  clearTrackClips(idx: any): void;
  clientXToBeat(clientX: any): number;
  clipboard: ArrangeClipboard | null;
  close(): void;
  closeMenu(): void;
  contentEndBeat(): number;
  copySelection(cut: any): void;
  cutSelection(): void;
  decayMeters(): void;
  defaultState(): void;
  deleteSelected(): unknown;
  deleteSelectedSilent(ids: any): unknown;
  dirs: string[];
  dirty: boolean;
  doSave(): Promise<any>;
  dragState: DragState | null;
  drawAudioThumb(ctx: any, track: any, clip: any, w: any, h: any, spb: any): void;
  drawMidiThumb(ctx: any, track: any, clip: any, w: any, h: any): void;
  drawThumbFor(el: any): void;
  dropGhostEl: HTMLElement | null;
  duplicateSelection(): void;
  duplicateTrack(idx: any): void;
  el: ArrangeEls;
  engine: ArrangeEngineInstance;
  expandedMap: { [key: string]: boolean } | null;
  exportWav(): HTMLElement;
  fetchTreeNode(dir: any, sub: any, cb: any): void;
  finishClipGesture(): void;
  finishRulerDrag(): void;
  fmtPos(beat: any): string;
  followPlayhead(beat: any): void;
  forEachSelectedClip(fn: any): void;
  frameCount: number;
  gestureSnap(beat: any, e: any): number;
  handleDrop(e: any): void;
  hideDropGhost(): void;
  hideOverlay(overlay: any): void;
  hoverDropEl: HTMLElement | null;
  hoverSourceEl: HTMLElement | null;
  hudTimer: number | null;
  init(): void;
  initialized: boolean;
  initRulerPointer(): void;
  invalidateThumbByClipId(clipId: any): void;
  isActivatableTarget(t: any): boolean;
  isArrangeDrag(e: any): boolean;
  isEditableTarget(t: any): boolean;
  isFocused: boolean;
  isOpen: boolean;
  isPlaying: boolean;
  isSourceDrag(e: any): boolean;
  joinAbs(dir: any, sub: any, name: any): string;
  loadedProjectId: string | null;
  loadExpandedMap(): { [key: string]: boolean };
  loadMaterialDirs(): void;
  locateClip(clipEl: any): LocatedClip | null;
  loop: ArrangeLoop;
  makeTrack(index: any): ArrangeTrack;
  menuEl: HTMLElement | null;
  menuOutsideHandler: ((ev: Event) => void) | null;
  meterEls: { [id: string]: HTMLElement | null };
  metronome: boolean;
  midiFiles: unknown[];
  moveTrack(idx: any, dir: any): void;
  moveTrackSelection(dir: any): void;
  nodeKey(dir: any, sub: any): string;
  open(): void;
  openBlankMenu(e: any): void;
  openClipMenu(e: any, clipEl: any): void;
  openColorMenu(x: any, y: any, idx: any): void;
  openConfirm(title: any, text: any, fn: any): void;
  openOutputRack(): void;
  openRenameModal(idx: any): void;
  openSourceMenu(track: any, x: any, y: any): unknown;
  openTrackMenu(e: any, idx: any): void;
  pasteClipboard(): void;
  pausePlayback(tempSuspend?: any): void;
  pianoRollOwnsKeys(): unknown;
  playbackOriginBeat: number;
  playheadBeat: number;
  positionClipEl(el: any, clip: any): void;
  ppb: number;
  previewRackItem(item: any, btn?: any, presetId?: any): void;
  previewRackSample(path: any): void;
  previewRackSource(source: any, btn?: any): unknown;
  /* ===== P2 机架（乐器箱）与编排 ops ===== */
  /* 项目乐器箱：轨道与钢琴窗的音源只能来自这里 */
  rack: { items: RackItem[] };
  /* 钢琴窗音源选择（持久化到 arrangement.pianoRoll） */
  pianoRollSource: { itemId: string; presetId: string };
  /* 混音台状态（P4；P2 起登记 gain/pan/mute/solo） */
  mixer: {
    channels: { [trackId: string]: MixerChannel };
    master?: MixerChannel;
  };
  /* 服务端修订号（SSE/保存回执带回） */
  rev: number;
  /* 定向修改与 AI 工具共用同一服务端实现（POST /arrangement/ops） */
  arrangeOps(ops: any[]): Promise<any>;
  ensureRackItem(sourceId: any, presetId: any, name: any): Promise<RackItem | null>;
  assignRackItem(item: any, trackIdx?: any, presetId?: any): void;
  removeRackItem(itemId: any, force?: any): Promise<void> | void;
  normalizeRack(): boolean;
  notifyRackChanged(): void;
  /* 本次载入发生了 v1→v2 迁移/引用修复，setProject 据此立即回写磁盘 */
  _needsMigrationSave?: boolean;
  /* 把服务端编排数据热应用（只覆盖 changed 点名的部分，不动本地未保存编辑） */
  applyRemoteArrangement(data: any, changed: any): void;
  /* 钢琴窗音源变化（PianoRoll 调用）：落盘 arrangement.pianoRoll */
  setPianoRollSource(itemId: any, presetId: any): void;
  /* SF2 音源解析：注册表优先，回退 IndexedDB 记录 */
  resolveSf2DiskPath(source: any): Promise<string>;
  loadSf2BufferForWebAudio(source: any): Promise<unknown>;
  pushHistory(): void;
  rafId: number | null;
  redo(): void;
  redoStack: string[];
  refreshClipEl(clipEl: any, clip: any): void;
  refreshOpenTreeNodes(): void;
  refreshSelectionClasses(): void;
  refreshTrackHeadStates(): void;
  removeMaterialDir(d: any): void;
  removeTrack(idx: any): void;
  renderAll(): void;
  renderAllThumbsSoon(): void;
  renderMaterialTree(): void;
  renderMidiList(): HTMLElement;
  renderOutputRack(filter: any): void;
  renderRuler(): void;
  renderTracks(): void;
  repeatRight(): void;
  replaceTrackSourceFromDrop(trackIdx: any, source: any): void;
  rescheduleSamplesDebounced(): void;
  resizeRulerCanvas(): void;
  resolveDropTarget(e: any): DropTarget;
  restoreSnapshot(json: any): void;
  rulerCtx: CanvasRenderingContext2D | null;
  rulerDrag: RulerDrag | null;
  sampleScheduleSig(): string;
  saveExpandedMap(): void;
  saveInFlight: boolean;
  saveTimer: number | null;
  scheduleSave(): void;
  secondsPerBeat(): number;
  seekTo(beat: any, restartIfPlaying: any): void;
  selectAllClips(): void;
  selectedClips: string[];
  selectedTrackIdx: number;
  selectOnly(clipId: any): void;
  selectTrack(idx: any): void;
  sendSampleSchedule(): unknown;
  setBpm(val: any): void;
  setFocus(focused: any): void;
  setPpb(val: any): void;
  setSaveStamp(text: any, warn: any): void;
  setTrackSource(track: any, source: any): void;
  showHUD(text: any): void;
  showMenu(items: any, x: any, y: any): void;
  showOverlay(overlay: any): void;
  snap: number;
  snapBeat(beat: any): number;
  snapshot(): string;
  sourceBadgeText(track: any): string;
  splitClipAtBeat(track: any, clip: any, atBeat: any): boolean;
  splitClipAtCursor(track: any, clip: any, beatAtCursor: any): boolean;
  splitSelectedAtPlayhead(): void;
  startPlayback(): void;
  startUILoop(): void;
  stopEngineClock(): void;
  stopPlayback(resetHead?: any): void;
  stopUILoop(): void;
  syncResumeUI(prefs: any): void;
  syncTransportUI(): void;
  thumbObserver: IntersectionObserver | null;
  toggle(): void;
  toggleClipSelect(clipId: any): void;
  toggleLoop(): void;
  toggleMetro(): void;
  toggleMute(idx: any): void;
  toggleOutputRack(): void;
  togglePlay(): void;
  toggleResumeOnPause(): void;
  toggleSolo(idx: any): void;
  toggleTreeNode(dir: any, sub: any): void;
  TREE_EXPANDED_KEY: string;
  undo(): void;
  undoStack: string[];
  updateClipGesture(e: any): void;
  updateContentWidth(): void;
  updateDropGhost(target: any): void;
  updateDropHighlight(e: any): void;
  updateMeters(): void;
  updatePlayButton(): void;
  updatePlayline(): void;
  updatePosDisplay(): void;
  updateRulerDrag(e: any): void;
  updateSourceHighlight(e: any): void;
  zoomStep(dir: any): void;
}

interface PianoRollController {
  _audioNow(): number;
  _clearNativeTimers(): void;
  _currentPlayBeat(): number;
  _engineSf2Load(trk: any, path: any, bank: any, program: any, finish: any): unknown;
  _engineSf2Prepare(): unknown;
  _expandPosWindow(from: any, to: any): PosSpan[];
  _lastCanvasMainH: number;
  _lastCanvasW: number;
  _nativeTimers: PianoNativeTimer[];
  _pitchCache: { [name: string]: number };
  _renderQueued: boolean;
  _resizeRaf: number | null;
  _resyncPlaybackTo(beat: any): void;
  _saveStampTimer: number | null;
  _schedCtxTime: number;
  _schedNativeNote(p: any, v: any, when: any, durSec: any, trk: any): void;
  _schedPos: number;
  _schedSegLen: number;
  _schedSegStart: number;
  _schedStartBeat: number;
  _schedTimer: number | null;
  _scheduleTick(): void;
  _sf2DiskPath: string | null;
  _sf2EngineFailAt: number;
  _sf2EngineFailed: boolean;
  _sf2EngineFailNotified: unknown;
  _sf2EngineKey: unknown;
  _sf2EngineLoadNotified: boolean;
  _sf2EnginePending: boolean;
  _sf2EnginePrepareAsync(key: any): unknown;
  _sf2EngineReady: boolean;
  _sf2LibName: string;
  activeRecordNotes: { [pitch: string]: LiveRecordNote };
  activeTabId: string | null;
  addDeleteEffect(note: any): void;
  applyPlaybackVolume(v: number): void;
  animFrameId: number | null;
  anyModalOpen(): boolean;
  applyVelocityBrush(b1: any, v1: any, b2: any, v2: any, groups: any): void;
  applyVelocityStraightLine(b1: any, v1: any, b2: any, v2: any, groups: any): void;
  autoCenterView(): void;
  bindCanvasEvents(): void;
  bindDrawerResizer(): void;
  bindFocusManager(): void;
  bindGlobalShortcuts(): unknown;
  bindHeaderControls(): void;
  bindMidiRouter(): void;
  bindToolModals(): void;
  bpm: number;
  chordDropdown: PrDropdown | null;
  chordStamp: string;
  clipboard: MidiNote[];
  close(): void;
  closeAllDropdowns(): void;
  closeTab(tabId: any, e: any): unknown;
  closeThreshold: number;
  closeTopModal(): boolean;
  copySelection(): void;
  countInRemaining: number;
  currentTool: string;
  cutSelection(): void;
  deleteEffects: DeleteEffect[];
  deleteSelection(): unknown;
  dragState: DragState | null;
  drawerEl: HTMLElement | null;
  drawerHeight: number;
  duplicateSelectionRight(): unknown;
  editingNote: MidiNote | null;
  effectRafId: number | null;
  escapeAction(): void;
  findNoteAt(beat: any, pitch: any): MidiNote | null;
  findVelocityGroupAt(beat: any, groups: any): VelocityGroup | null;
  flushAllOnUnload(): unknown;
  getActiveTab(): PianoTab | null;
  getGridCoords(e: any): GridPos;
  getVelocityNoteGroups(notesList: any): VelocityGroup[];
  ghostDropdown: PrDropdown | null;
  ghostTrackId: string | null;
  gridCanvas: HTMLCanvasElement | null;
  gridCtx: CanvasRenderingContext2D | null;
  handleGridDblClick(e: any): void;
  handleGridMouseDown(e: any): unknown;
  handleGridMouseMove(e: any): unknown;
  handleGridMouseUp(e: any): void;
  handleLiveNoteOff(note: any): void;
  handleLiveNoteOn(note: any, velocity: any): void;
  handleVelocityMouseDown(e: any): unknown;
  handleVelocityRightClick(e: any): void;
  hudBadge: HTMLElement | null;
  hudTimer: number | null;
  init(): void;
  isCountIn: boolean;
  isFocused: boolean;
  isLooping: boolean;
  isMaximized: boolean;
  isMetronome: boolean;
  isPlaying: boolean;
  isRecording: boolean;
  isTypingKeyboard: boolean;
  keysCanvas: HTMLCanvasElement | null;
  keysCtx: CanvasRenderingContext2D | null;
  keysWidth: number;
  loopEnd: number;
  loopStart: number;
  minHeight: number;
  modalFocusReturn: HTMLElement | null;
  noteRowHeight: number;
  open(): void;
  openArpModal(): void;
  openFlipModal(): void;
  openLevelScaleModal(): void;
  openModalAnimated(modalEl: any): void;
  openNotePropertiesModal(note: any): void;
  openRandomModal(): void;
  openSoundLibraryModal(): void;
  openStrumModal(): void;
  parseMidiBytes(arrayBuffer: any): MidiNote[];
  pasteSelection(): unknown;
  pitchOf(name: any): number;
  pixelsPerBeat: number;
  playbackOriginBeat: number;
  playheadBeat: number;
  playNoteSound(midiNote: any, velocity?: any, when?: any): void;
  prModalIds: string[];
  pushHistory(): void;
  pushHistoryForWheelGesture(): void;
  quickLegato(): void;
  quickQuantize(): void;
  recordConfig: RecordConfig;
  redo(): void;
  render(): void;
  renderGrid(): unknown;
  renderKeys(): void;
  renderRuler(ctx: any, w: any, isDark: any): void;
  renderTabs(): void;
  renderVelocity(): void;
  requestEffectFrame(): void;
  resizeCanvases(): void;
  roundRect(ctx: any, x: any, y: any, w: any, h: any, r: any, fill: any, stroke: any): void;
  rulerHeight: number;
  saveCurrentTab(): Promise<void>;
  saveDebounceTimer: number | null;
  saveTab(tab: any): Promise<void>;
  scaleDropdown: PrDropdown | null;
  scheduleAutoSave(): void;
  scheduleRender(): void;
  scrollX: number;
  scrollY: number;
  selectedNotes: MidiNote[];
  selectedRootPitch: number;
  selectedScale: string;
  sendToChat(): unknown;
  setFocus(focused: any): void;
  setSaveState(state: any): void;
  setTool(tool: any): void;
  setupCustomDropdown(wrapId: any, btnId: any, menuId: any, options: any, initialValue: any, onSelect: any): PrDropdown | null;
  showHUD(text: any, duration?: any): void;
  snapDropdown: PrDropdown | null;
  snapGrid: number;
  soundDropdown: PrDropdown | null;
  soundSource: string;
  /* ===== P2：钢琴窗音源同样来自机架（选择器三处共用） ===== */
  pianoRollSource: { itemId: string; presetId: string };
  bindSoundPicker(): void;
  setSoundLabel(text: any): void;
  openRackPicker(): void;
  setRackVoice(item: any, presetId?: any): boolean;
  loadBrowserSf2Into(item: any, bank: any, program: any): void;
  persistSource(itemId: any, presetId: any): void;
  syncRackSource(): void;
  applyRegistrySoundFontToRack(src: any, pick: any): Promise<boolean>;
  startPlayback(): void;
  startRecording(): unknown;
  stopAllSounds(): void;
  stopNoteSound(midiNote: any, when?: any): void;
  stopPlayback(): void;
  stopRecording(): void;
  switchTab(tabId: any): void;
  tabs: PianoTab[];
  toggleLoop(): void;
  toggleMaximize(): void;
  togglePlay(): void;
  toggleRecord(): void;
  toggleTypingKeyboard(forcedVal?: any): void;
  transposeSelection(semitones: any): void;
  undo(): void;
  updateGhostTrackSelect(): void;
  updateLoopBoundsFromNotes(notes: any): void;
  /* 音色下拉：parsed={name,presets}；meta 为注册表条目（可选）；
     preferId 指定选中哪个预设（默认首个） */
  updateSoundSelectOptions(parsed: any, meta?: any, preferId?: string): void;
  /* 把注册表音源应用到钢琴窗音色（拉预设 + 记住磁盘路径），
     返回是否真正切换成功（无可用预设时为 false） */
  applyRegistrySoundFont(src: any, presetId?: string): Promise<boolean>;
  velocityCanvas: HTMLCanvasElement | null;
  velocityCtx: CanvasRenderingContext2D | null;
  velocityHeight: number;
}

interface ArrangeEngineInstance {
  _anchorCtxTime: number;
  _anySolo(tracks: any): boolean;
  _beginPlay(startBeat: any): void;
  _builtinPrepared: { trackId: string; tone: string } | null;
  _builtinRetryAt: number;
  _clearNativeTimers(): void;
  _ensureTrackBuiltinPreset(track: any, src: any): unknown;
  _ensureTrackWaveVoice(track: any, nodes: any): void;
  _hbFallback: number | null;
  _heartbeat: HeartbeatHandle | null;
  _nativeTimers: EngineNativeTimer[];
  _nativeOn?: { [key: string]: number };
  _nativeOnPush(idx: any, midi: any, trackId: any): void;
  _nativeOnRemove(idx: any, midi: any): void;
  _nativeOnFlush(): void;
  _pruneBufferCache(): void;
  clearPendingClicks(): void;
  _segLimitWarnAt?: number;
  maxCachedBytes?: number;
  _now(): number;
  _onHeartbeat(): void;
  _pendingClicks: PendingClick[];
  _pendingClips: PendingAudioClip[];
  _pendingOff: PendingNoteOff[];
  _pendingOn: PendingNoteOn[];
  _pitchCache: { [note: string]: number };
  _playGen: number;
  _playStartBeat: number;
  _posRangeToBeats(from: any, to: any): PosBeatSeg[];
  _posToBeat(pos: any): unknown;
  _previewSrc: AudioBufferSourceNode | null;
  _queueAudioClip(clip: any, track: any, nodes: any, evFrom: any, evTo: any, seg: any, spb: any): void;
  _queueMidiClip(clip: any, track: any, nodes: any, evFrom: any, evTo: any, seg: any, spb: any): void;
  _queueSeekClips(startBeat: any): void;
  _releaseSource(rec: any, now: any): void;
  _scanWindow(from: any, to: any, spb: any): void;
  _schedNative(delaySec: any, fn: any): unknown;
  _schedPos: number;
  _startAudioClip(ev: any, when: any): void;
  _startHeartbeat(): void;
  _stopHeartbeat(): void;
  _triggerDue(now: any): void;
  activeSources: ActiveAudioSource[];
  bufferCache: Map<string, SampleCacheEntry>;
  computePeaks(buffer: any, buckets?: any): Float32Array;
  init(): void;
  limiter: DynamicsCompressorNode | null;
  loadTrackSoundFont(track: any, nodes: any, src: any): unknown;
  masterGain: GainNode | null;
  maxCachedBuffers: number;
  secondsPerBeat(): number;
  stopAllVoices(): void;
}

interface SynthEngineInstance {
  _clickTimers: number[];
  _debouncedVoiceSync(): void;
  _ensureNativeVoice(): boolean;
  _lastVoiceSig: string | null;
  _nativeVoices: { [midi: string]: number };
  _outNode: AudioNode | null;
  _sharedCtx: AudioContext | null;
  _voiceRetryAt: number;
  _voiceSyncTimer: number | null;
  activeVoices: { [midi: string]: SynthVoice[] };
  filterNode: BiquadFilterNode | null;
  masterGain: GainNode | null;
}

interface SoundFontPlayerInstance {
  _outNode: AudioNode | null;
  _rawSamples: SfRawSample[];
  _sampleData: Int16Array | null;
  _sharedCtx: AudioContext | null;
  activeVoices: { [midi: string]: SfVoice[] };
  builtinBuffers: { [name: string]: BuiltinSample } | null;
  builtinInstrument: string | null;
  ctx: AudioContext | null;
  ensureBuiltinPresets(): void;
  generateBuiltinPresets(): void;
  getSampleBuffer(idx: number): SfSampleEntry | null;
  isMuted: boolean;
  masterGain: GainNode | null;
  sampleBuffers: { [idx: number]: SfSampleEntry };
  volume: number;
}

interface MidiInputRouterApi {
  activeInput: MIDIInput | null;
  activeInputs: MIDIInput[];
  applyVelocityCurve(rawVel: number): number;
  bindDevice(deviceId: string): void;
  bindTypingKeyboard(): void;
  handleMidiMessage(e: MIDIMessageEvent): void;
  initWebMIDI(): void;
  listeners: ((type: string, note: number, velocity: number) => void)[];
  midiAccess: MIDIAccess | null;
  pressedKeys: { [code: string]: number };
}

interface LocatedClip {
  track: ArrangeTrack;
  trackIdx: number;
  clip: ArrangeClip;
  clipEl: HTMLElement;
}

interface PianoTab {
  id: string;
  name: string;
  fullName?: string;
  projectId: string;
  path?: string;
  filePath?: string;
  notes: MidiNote[];
  originalNotes?: MidiNote[];
  dirty?: boolean;
  undoStack: string[];
  redoStack: string[];
  bpm?: number;
}
