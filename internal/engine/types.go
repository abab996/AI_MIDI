package engine

import "encoding/json"

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
	StateDisabled  EngineState = "disabled"  // 配置禁用
	StateStopped   EngineState = "stopped"
	StateStarting  EngineState = "starting"
	StateReady     EngineState = "ready"
	StateRestarting EngineState = "restarting"
	StateFailed    EngineState = "failed"
)

// EngineStatus 引擎运行状态快照（/api/audio/status）
type EngineStatus struct {
	State         EngineState `json:"state"`
	PID           int         `json:"pid"`
	Restarts      int         `json:"restarts"`
	LastError     string      `json:"last_error,omitempty"`
	DeviceSummary string      `json:"device_summary,omitempty"`
	Protocol      uint32      `json:"protocol_version"`
}

// Event 引擎推送的事件
type Event struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data"`
}
