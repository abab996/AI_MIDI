package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

// Request 控制请求（Go → Engine）
type Request struct {
	ID     float64        `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// Response 控制响应（Engine → Go）；ok=false 时 Error 非 nil
type Response struct {
	ID     *float64         `json:"id"`
	OK     bool             `json:"ok"`
	Result json.RawMessage  `json:"result"`
	Error  *EngineErrorBody `json:"error"`
}

// EngineErrorBody 引擎业务错误的载荷（协议文档 4.x：error 为 {message}）
type EngineErrorBody struct {
	Message string `json:"message"`
}

// Err ok=false 时返回业务错误
func (r *Response) Err() error {
	if r.OK {
		return nil
	}
	msg := "unknown error"
	if r.Error != nil {
		msg = r.Error.Message
	}
	return fmt.Errorf("engine: %s", msg)
}

// EngineInfo hello 应答内的引擎信息
type EngineInfo struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	ProtocolVersion uint32   `json:"protocolVersion"`
	Capabilities    []string `json:"capabilities"`
}

// AudioSettings 音频设置（settings.json 的 audio 段）
type AudioSettings struct {
	EngineEnabled bool   `json:"engine_enabled"`
	EnginePath    string `json:"engine_path,omitempty"` // 引擎可执行文件路径覆盖（开发期用）
	Driver        string `json:"driver,omitempty"`      // ASIO | Windows Audio | DirectSound ...
	Device        string `json:"device,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	BufferSize    int    `json:"buffer_size,omitempty"`
}

// DeviceType 一个驱动类型及其输出设备列表（listDevices 结果）
type DeviceType struct {
	Driver  string   `json:"driver"`
	Devices []string `json:"devices"`
}

// DeviceList listDevices 完整结果
type DeviceList struct {
	Drivers []DeviceType `json:"drivers"`
}

// EngineState 引擎进程状态
type EngineState string

const (
	StateDisabled   EngineState = "disabled" // 配置禁用
	StateStopped    EngineState = "stopped"
	StateStarting   EngineState = "starting"
	StateReady      EngineState = "ready"
	StateRestarting EngineState = "restarting"
	StateFailed     EngineState = "failed"
)

// EngineStatus 引擎运行状态快照（/api/audio/status）
type EngineStatus struct {
	ReplayErrors []string `json:"replay_errors,omitempty"`
	/* PluginWarnings / IsolatedPlugins 是"单插件"级别的告警（P-H3）：
	   某个插件缺失/装载失败/被隔离，不影响引擎就绪与其他插件播放，
	   前端不得据此把全局状态判成 failed（那样会拦下全部播放）。 */
	PluginWarnings  []string    `json:"plugin_warnings,omitempty"`
	IsolatedPlugins []string    `json:"isolated_plugins,omitempty"`
	State           EngineState `json:"state"`
	PID             int         `json:"pid"`
	Restarts        int         `json:"restarts"`
	LastError       string      `json:"last_error,omitempty"`
	DeviceSummary   string      `json:"device_summary,omitempty"`
	Protocol        uint32      `json:"protocol_version"`
	// 引擎是否已加载任何音色（冷启动默认加载或显式 loadSoundFont 成功）。
	// ready 只代表 IPC 通畅；SF2 轨要出声还得看这里。波形声部（setTrackVoice）
	// 不依赖音色，不受此字段影响
	SoundfontLoaded bool `json:"soundfont_loaded"`
	// 音色目录首个 SF2 的绝对路径（内置 piano/strings 在引擎模式的原生映射
	// 目标）；空 = 未安装内置音色库
	DefaultSoundfont string `json:"default_soundfont,omitempty"`
}

// Event 引擎推送的事件
type Event struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data"`
}

// PluginParamEvent 已缓存的参数变化事件（P-L1）。
// 引擎每 50ms 批量推送一次；Go 侧带递增序号供前端增量拉取。
type PluginParamEvent struct {
	Seq   int64           `json:"seq"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// SoundFontLoadState SF2 大音色异步加载状态（S-M1）。
// 引擎侧解析无法中断，Go 侧提供"已等待时长 + 取消（等待/恢复原音色）"，
// 避免固定 30s 超时把正在解析的引擎判成会话失效。
type SoundFontLoadState struct {
	Track      int       `json:"track"`
	Path       string    `json:"path,omitempty"`
	Size       int64     `json:"size,omitempty"`
	Running    bool      `json:"running"`
	Cancelling bool      `json:"cancelling"`
	Phase      string    `json:"phase"` // idle | loading | cancelling | restoring | done | cancelled | failed
	Error      string    `json:"error,omitempty"`
	ElapsedMS  int64     `json:"elapsed_ms"`
	StartedAt  time.Time `json:"-"`
	FinishedAt time.Time `json:"-"`
}

// TrackMixParams 多轨混音参数（M3 混音图）
type TrackMixParams struct {
	Track  int     `json:"track"`
	Gain   float32 `json:"gain"`
	Pan    float32 `json:"pan"`
	Mute   bool    `json:"mute"`
	Solo   bool    `json:"solo"`
	Active bool    `json:"active"`
}

// Timecode 走带时间码（引擎 0x05 二进制帧 / timecode 请求共用结构）
type Timecode struct {
	SamplePos int64   `json:"samplePos"`
	Beat      float64 `json:"beat"`
	BPM       float64 `json:"bpm"`
	Playing   bool    `json:"playing"`
}

// zero 是否从未收到过任何 timecode（区分"停在 0 拍"与"无数据"）
func (t Timecode) zero() bool {
	return t.SamplePos == 0 && t.Beat == 0 && t.BPM == 0 && !t.Playing
}
