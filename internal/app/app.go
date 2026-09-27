package app

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"melodymate/internal/chat"
	"melodymate/internal/engine"
)

// App Wails 应用程序结构
type App struct {
	ctx   context.Context
	ctxMu sync.RWMutex

	// emitter 桥事件出口（测试注入用）；nil = 生产路径（Wails EventsEmit）
	emitter func(streamID string, event map[string]any)
}

// NewApp 创建 App 实例
func NewApp() *App {
	return &App{}
}

// Startup 在 Wails 初始化就绪时调用
func (a *App) Startup(ctx context.Context) {
	a.ctxMu.Lock()
	defer a.ctxMu.Unlock()
	a.ctx = ctx
}

// ===== 原生音频引擎绑定（M2）：前端经 window.go.app.App.* 直达，绕过 HTTP =====

var errEngineUnavailable = errors.New("音频引擎未启用")

// EngineNoteOn 演奏音符按下（实时路径，track 0）
func (a *App) EngineNoteOn(channel, key, velocity int) {
	if sup := engine.Get(); sup != nil {
		sup.NoteOn(channel, key, velocity)
	}
}

// EngineNoteOff 演奏音符抬起（track 0）
func (a *App) EngineNoteOff(channel, key int) {
	if sup := engine.Get(); sup != nil {
		sup.NoteOff(channel, key)
	}
}

// EngineNoteOnTrack 指定轨道演奏（编曲多轨）
func (a *App) EngineNoteOnTrack(track, key, velocity int) {
	if sup := engine.Get(); sup != nil {
		sup.NoteOnTrack(track, key, velocity)
	}
}

func (a *App) EngineNoteOffTrack(track, key int) {
	if sup := engine.Get(); sup != nil {
		sup.NoteOffTrack(track, key)
	}
}

// EnginePanic 全音符停止（卡音逃生口，见 Supervisor.PanicAll）
func (a *App) EnginePanic() error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.PanicAll()
}

// EngineLoadSoundFont 加载音色文件到引擎（track 0 兼容）
func (a *App) EngineLoadSoundFont(path string) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.LoadSoundFont(path)
}

// EngineLoadSoundFontTrack 指定轨道加载 SF2
func (a *App) EngineLoadSoundFontTrack(track int, path string) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.LoadSoundFontTrack(track, path)
}

// EngineSetTrackVoice 切换引擎轨道到内置波形声部（合成波音色原生路径，
// voice: {wave, attack, decay, sustain, release, cutoff, resonance, gain}）
func (a *App) EngineSetTrackVoice(track int, voice map[string]any) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.SetTrackVoice(track, voice)
}

// EngineSetTrackPreset 选择引擎轨道 SF2 的预设（多预设音色库；
// bank/program 为 General MIDI 编号，预设不存在由引擎侧拒绝）
func (a *App) EngineSetTrackPreset(track, bank, program int) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.SetTrackPreset(track, bank, program)
}

// EngineClick 节拍器木鱼音（引擎侧合成；high=true 重拍 1600Hz / false 弱拍 900Hz）
func (a *App) EngineClick(track int, high bool) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.Click(track, high)
}

// EngineSetTrackMix 设置音轨混音参数（M3 混音图）
func (a *App) EngineSetTrackMix(track int, gain, pan float32, mute, solo, active bool) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.SetTrackMix(engine.TrackMixParams{
		Track:  track,
		Gain:   gain,
		Pan:    pan,
		Mute:   mute,
		Solo:   solo,
		Active: active,
	})
}

// ===== 走带控制（M3 阶段一）=====

// EnginePlay 引擎走带播放
func (a *App) EnginePlay() error {
	if sup := engine.Get(); sup != nil {
		return sup.TransportPlay()
	}
	return errEngineUnavailable
}

// EngineStop 引擎走带停止
func (a *App) EngineStop() error {
	if sup := engine.Get(); sup != nil {
		return sup.TransportStop()
	}
	return errEngineUnavailable
}

// EngineLocate 引擎走带定位（拍）
func (a *App) EngineLocate(beat float64) error {
	if sup := engine.Get(); sup != nil {
		return sup.TransportLocate(beat)
	}
	return errEngineUnavailable
}

// EngineSetTempo 设置引擎走带 BPM
func (a *App) EngineSetTempo(bpm float64) error {
	if sup := engine.Get(); sup != nil {
		return sup.TransportSetTempo(bpm)
	}
	return errEngineUnavailable
}

// EngineGetTimecode 读取引擎走带时间码
func (a *App) EngineGetTimecode() (*engine.Timecode, error) {
	if sup := engine.Get(); sup != nil {
		return sup.Timecode()
	}
	return nil, errEngineUnavailable
}

// EngineScheduleSamples 批量调度音频素材（AUTO时全走JUCE，尾音自然不截断）
func (a *App) EngineScheduleSamples(clips []map[string]any, bpm float64) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.ScheduleSamples(clips, bpm)
}

// EngineClearSamples 清空素材调度
func (a *App) EngineClearSamples() error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.ClearSamples()
}

// EngineScheduleNotes 批量调度 MIDI
func (a *App) EngineScheduleNotes(notes []map[string]any, bpm float64) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.ScheduleNotes(notes, bpm)
}

// EngineBounce 离线渲染
func (a *App) EngineBounce(params map[string]any) (string, error) {
	sup := engine.Get()
	if sup == nil {
		return "", errEngineUnavailable
	}
	return sup.Bounce(params)
}

// EngineGetLevels 获取各轨电平
func (a *App) EngineGetLevels() ([]float32, error) {
	sup := engine.Get()
	if sup == nil {
		return nil, errEngineUnavailable
	}
	return sup.GetLevels()
}

// EngineSetLoop 设置循环区间
func (a *App) EngineSetLoop(on bool, start, end float64) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.SetLoop(on, start, end)
}

/* ===== P4：混音台（FX 链 / 主总线 / 主总线电平） ===== */

// EngineSetFxChain 下发一条 FX 链（track < 0 表示主总线）。
// slots 元素：{slot, plugin?, bypass, wet, off?}；plugin 非空在 P4 会被
// 引擎拒绝（装载在 P5/P6 开放）——如实返回错误，不静默忽略。
func (a *App) EngineSetFxChain(track int, slots []map[string]any) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	parsed := make([]engine.FxSlotParams, 0, len(slots))
	for _, m := range slots {
		slot := engine.FxSlotParams{}
		if v, ok := m["slot"].(float64); ok {
			slot.Slot = int(v)
		}
		if v, ok := m["plugin"].(string); ok {
			slot.Plugin = v
		}
		if v, ok := m["bypass"].(bool); ok {
			slot.Bypass = v
		}
		if v, ok := m["wet"].(float64); ok {
			slot.Wet = v
		} else {
			slot.Wet = 1.0
		}
		if v, ok := m["off"].(bool); ok {
			slot.Off = v
		}
		parsed = append(parsed, slot)
	}
	return sup.SetFxChain(track, parsed)
}

// EngineSetMaster 设置主总线增益（0.0 ~ 4.0）
func (a *App) EngineSetMaster(gain float64) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.SetMaster(gain)
}

// EngineGetMasterLevel 主总线峰值电平（混音台主通道表）
func (a *App) EngineGetMasterLevel() (float32, error) {
	sup := engine.Get()
	if sup == nil {
		return 0, errEngineUnavailable
	}
	return sup.GetMasterLevel()
}

// EngineGetLevelsWithMaster 一次 IPC 取回轨电平与主总线电平（P4 电平表轮询）
func (a *App) EngineGetLevelsWithMaster() (map[string]any, error) {
	sup := engine.Get()
	if sup == nil {
		return nil, errEngineUnavailable
	}
	levels, master, err := sup.GetLevelsSnapshot()
	if err != nil {
		return nil, err
	}
	return map[string]any{"levels": levels, "master": master}, nil
}

/* ===== P5：VST3 宿主 ===== */

// EngineListPlugins 已知插件清单（不扫描）
func (a *App) EngineListPlugins() (map[string]any, error) {
	sup := engine.Get()
	if sup == nil {
		return nil, errEngineUnavailable
	}
	list, err := sup.ListPlugins()
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": list.Count, "plugins": list.Plugins, "formats": list.Formats}, nil
}

// EngineScanPlugins 扫描插件目录（dirs 为空 = 系统默认目录；可能数十秒）
func (a *App) EngineScanPlugins(dirs []string) (map[string]any, error) {
	sup := engine.Get()
	if sup == nil {
		return nil, errEngineUnavailable
	}
	list, err := sup.ScanPlugins(dirs, 0)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": list.Count, "plugins": list.Plugins, "formats": list.Formats}, nil
}

// EngineLoadPlugin 装载插件：slot < 0 = 轨道乐器；否则 track<0 表主链 FX 槽
func (a *App) EngineLoadPlugin(track, slot int, pluginID string) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.LoadPlugin(track, slot, pluginID)
}

// EngineUnloadPlugin 卸载插件
func (a *App) EngineUnloadPlugin(track, slot int) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.UnloadPlugin(track, slot)
}

// EnginePluginParams 读插件参数表
func (a *App) EnginePluginParams(track, slot int) (map[string]any, error) {
	sup := engine.Get()
	if sup == nil {
		return nil, errEngineUnavailable
	}
	return sup.PluginParams(track, slot)
}

// EngineSetPluginParam 写插件参数（0~1 归一化）
func (a *App) EngineSetPluginParam(track, slot, index int, value float64) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.SetPluginParam(track, slot, index, value)
}

// EngineOpenPluginEditor 打开插件原生编辑器
func (a *App) EngineOpenPluginEditor(track, slot int) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.OpenPluginEditor(track, slot)
}

// EngineClosePluginEditor 关闭插件编辑器
func (a *App) EngineClosePluginEditor() error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.ClosePluginEditor()
}

// EnginePluginState 宿主状态（哪些轨有乐器 / 哪些槽有效果器）
func (a *App) EnginePluginState() (map[string]any, error) {
	sup := engine.Get()
	if sup == nil {
		return nil, errEngineUnavailable
	}
	return sup.PluginState()
}

// ===== 聊天流事件桥（桌面模式专用）=====
//
// Wails Windows 的 assetserver 用 bytes.Buffer 缓存整个响应体，到
// Finish() 才一次性 PutByteContent 交给 WebView2（responsewriter_windows.go），
// 且包装层不实现 http.Flusher——HTTP SSE 在桌面模式物理上无法流式，
// 所有 chat_delta 攒到生成结束一起到达。因此桌面模式经 Wails 事件桥
// （runtime.EventsEmit → 前端 EventsOn，原生 IPC 即时送达）推送流式事件；
// 浏览器模式（-browser）仍走 HTTP SSE。事件协议（type 字段与载荷）与
// SSE 完全一致，前端消费代码共用。

// emitChatEvent 向指定流的订阅者推送一条事件
func (a *App) emitChatEvent(streamID string, event map[string]any) {
	if a.emitter != nil {
		a.emitter(streamID, event)
		return
	}
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()
	if ctx == nil {
		return
	}
	wailsruntime.EventsEmit(ctx, "chat:evt:"+streamID, event)
}

// emitChatEnd 通知流结束并清理订阅
func (a *App) emitChatEnd(streamID string) {
	a.emitChatEvent(streamID, map[string]any{"type": "done"})
	if a.emitter != nil {
		a.emitter(streamID, map[string]any{"__end": true})
		return
	}
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()
	if ctx == nil {
		return
	}
	wailsruntime.EventsEmit(ctx, "chat:end:"+streamID)
}

// chatEventSink 把流式回调包装为事件桥推送（SSE 回调的 error 语义在
// 桥模式下不存在——没有可失败的客户端连接）
func (a *App) chatEventSink(streamID string) chat.StreamCallback {
	return func(event map[string]any) error {
		a.emitChatEvent(streamID, event)
		return nil
	}
}

// ChatStreamStart 启动一轮聊天流（桌面模式）。立即返回，事件经
// "chat:evt:<streamID>" 推送，结束时补发 done 并 emit "chat:end:<streamID>"。
// 同一 streamID 重复启动忽略（前端保证唯一）。
func (a *App) ChatStreamStart(projectID, message string, edit bool, taskID, streamID string) error {
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()
	if ctx == nil {
		return errors.New("应用尚未就绪")
	}
	if projectID == "" || streamID == "" {
		return errors.New("缺少 project_id 或 stream_id")
	}
	go func() {
		defer a.emitChatEnd(streamID)
		// 管线解析的是模型输出的不可信内容，panic 时不能拖垮整个应用：
		// 捕获后向前端补一条 error 事件（emitChatEnd 由上方 defer 保证）
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[chat] 聊天流 panic（已拦截）", "stream", streamID, "panic", r, "stack", string(debug.Stack()))
				a.emitChatEvent(streamID, map[string]any{"type": "error", "message": "内部错误，聊天流异常终止，请重试"})
			}
		}()
		// ctx 用 Background：桌面模式没有 SSE 连接可断，任务后台续跑；
		// 用户停止经 tasks.TaskCancelChan 传导（与 HTTP 路径一致）
		if err := chat.ChatStream(context.Background(), projectID, message, edit, false, &taskID, a.chatEventSink(streamID)); err != nil {
			slog.Warn("[chat] 事件桥聊天流异常结束", "stream", streamID, "err", err)
		}
	}()
	return nil
}

// AnswerStreamStart 启动提问回答流（桌面模式），语义同 ChatStreamStart
func (a *App) AnswerStreamStart(projectID, questionID string, answers interface{}, streamID string) error {
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()
	if ctx == nil {
		return errors.New("应用尚未就绪")
	}
	if projectID == "" || questionID == "" || streamID == "" {
		return errors.New("缺少 project_id/question_id/stream_id")
	}
	go func() {
		defer a.emitChatEnd(streamID)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[chat] 回答流 panic（已拦截）", "stream", streamID, "panic", r, "stack", string(debug.Stack()))
				a.emitChatEvent(streamID, map[string]any{"type": "error", "message": "内部错误，回答流异常终止，请重试"})
			}
		}()
		if err := chat.AnswerStream(context.Background(), projectID, questionID, answers, a.chatEventSink(streamID)); err != nil {
			slog.Warn("[chat] 事件桥回答流异常结束", "stream", streamID, "err", err)
		}
	}()
	return nil
}
