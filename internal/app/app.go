package app

import (
	"context"
	"errors"
	"sync"

	"aimidi/internal/engine"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App Wails 应用程序结构
type App struct {
	ctx   context.Context
	ctxMu sync.RWMutex
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

// SelectFolderDialog 打开原生文件夹选择对话框
func (a *App) SelectFolderDialog() (string, error) {
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()

	if ctx == nil {
		return "", nil
	}

	return runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{
		Title: "选择工作区目录",
	})
}

// ===== 原生音频引擎绑定（M2）：前端经 window.go.app.App.* 直达，绕过 HTTP =====

var errEngineUnavailable = errors.New("音频引擎未启用")

// EngineNoteOn 演奏音符按下（实时路径）
func (a *App) EngineNoteOn(channel, key, velocity int) {
	if sup := engine.Get(); sup != nil {
		sup.NoteOn(channel, key, velocity)
	}
}

// EngineNoteOff 演奏音符抬起
func (a *App) EngineNoteOff(channel, key int) {
	if sup := engine.Get(); sup != nil {
		sup.NoteOff(channel, key)
	}
}

// EngineLoadSoundFont 加载音色文件到引擎
func (a *App) EngineLoadSoundFont(path string) error {
	sup := engine.Get()
	if sup == nil {
		return errEngineUnavailable
	}
	return sup.LoadSoundFont(path)
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
