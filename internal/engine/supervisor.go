package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// engineTokenEnv 引擎 IPC 启动令牌的环境变量名。
// 引擎进程创建管道时要求 hello 携带该令牌，阻止同机其他进程（同用户
// 的其他程序）直连管道调用 plugin.load / plugin.scan / shutdown 等能力。
// 用环境变量而非命令行参数：命令行对同机进程可见，环境块不可读。
const engineTokenEnv = "MelodyMate_ENGINE_TOKEN"

// newEngineToken 生成 32 字节随机令牌的十六进制串。
// 失败时返回空串（引擎退化为不鉴权，行为与旧版一致，不阻断启动）。
func newEngineToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		slog.Warn("[engine] 生成 IPC 令牌失败，本次会话不启用鉴权", "err", err)
		return ""
	}
	return hex.EncodeToString(buf)
}

// Config 守护器配置
type Config struct {
	EnginePath       string // 引擎 exe 路径（已解析为绝对路径）
	SoundFontDir     string // 默认音色目录（Library/soundfonts）；由 main 注入，避免 config↔engine 循环导入
	PluginsDir       string // 已安装 MM 插件根目录（Library/plugins）；MM 原生库加载路径的包含性校验基准
	DialTimeout      time.Duration
	HandshakeTimeout time.Duration
	RequestTimeout   time.Duration
	PingInterval     time.Duration // 心跳周期；0 = 默认 2s
	// Dirs 返回路径白名单目录（音色库 / bounce 输出 / 已注册素材目录）。
	// 由 main 注入；未注入时路径校验 fail-closed（见 paths.go）。
	Dirs func() (soundFontDir, outputDir string, materialDirs []string)
	// OnAudioFallback 冷启动重放把音频设置回滚到系统默认设备时回调
	// （驱动卡死等传输层失败）。main 借此把回滚结果持久化到 settings.json，
	// 否则每次启动都会重演「重放坏设置 → 卡死 → 回滚」循环
	OnAudioFallback func(AudioSettings)
}

// Supervisor 引擎进程守护：启动、心跳、崩溃自动重启、优雅回收。
//
// M1 会话快照策略：记录最近一次 applySetup 的音频设置，重启成功后重放，
// 使崩溃恢复对上层透明。音色/走带快照随 M2/M3 扩展。
type Supervisor struct {
	cfg    Config
	audio  AudioSettings
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc

	status         EngineStatus
	client         *Client
	cmd            *exec.Cmd
	startedEnabled bool                   // 当前守护会话是否以启用状态启动
	parked         bool                   // 引擎文件缺失等不可重试错误：驻留失败态直至退出
	sessionCnt     int                    // 已建立的会话数（Restarts = sessionCnt - 1）
	lastApply      *map[string]any        // 最近一次 applySetup 参数（重启后重放）
	lastGood       AudioSettings          // 最近一次成功应用的音频设置（applySetup 超时后回退目标）
	lastSoundFont  string                 // 兼容旧单轨（track 0）
	lastSoundFonts map[int]string         // 每轨独立 SF2（重启后重放）
	lastPresets    map[int][2]int         // 每轨 SF2 预设选择 bank/program（重启后重放）
	lastVoices     map[int]map[string]any // 每轨内置波形声部参数（重启后重放）
	/* P4：FX 链槽位参数与主总线增益（重启后重放；插件实例由 P5/P6 装载，
	   这里存的是"槽位状态"——bypass/wet 与（P5 起）插件引用） */
	lastChains      map[int][]map[string]any
	lastMasterChain []map[string]any
	lastMasterGain  float64
	hasSoundfont    bool // 当前会话是否已加载任何音色（/api/audio/status 透出）
	exePath         string
	token           string    // IPC 启动令牌：随进程生成，注入引擎环境并用于握手
	lastReadyAt     time.Time // 最近一次进入 StateReady 的时间（秒崩判定用）
	stopOnce        sync.Once
	doneCh          chan struct{} // 关闭表示主循环退出
	wakeCh          chan struct{} // StateDisabled 驻留时由 ApplySettings 启用引擎唤醒
}

// NewSupervisor 创建守护器（不启动；调用 Start）
func NewSupervisor(cfg Config, audio AudioSettings) *Supervisor {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 20 * time.Second
	}
	if cfg.HandshakeTimeout <= 0 {
		// ASIO Link Pro 等驱动首次打开可达 10-20s，引擎就绪前无法应答握手
		cfg.HandshakeTimeout = 20 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second // listDevices 首次枚举可能达数秒
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 2 * time.Second
	}
	sup := &Supervisor{cfg: cfg, audio: audio, lastGood: audio, lastSoundFonts: make(map[int]string), wakeCh: make(chan struct{}, 1)}
	sup.exePath = ResolveEnginePath(audio.EnginePath)
	sup.startedEnabled = audio.EngineEnabled
	/* 鉴权令牌：优先取环境变量（tools/engine_smoke 等外部驱动方需要
	   预置同一令牌），否则每次启动新生成。令牌只在内存与子进程环境中
	   流转，绝不写日志、不落盘。 */
	sup.token = strings.TrimSpace(os.Getenv(engineTokenEnv))
	if sup.token == "" {
		sup.token = newEngineToken()
	}
	return sup
}

// applySetupTimeout 驱动切换专用超时：正常切换 2-5s 完成；个别驱动
// （如 ASIO Link Pro）首次打开可达 10-20s 甚至更久（见 HandshakeTimeout
// 注释），15s 时限会把它误判为卡死——切换/改缓冲永远「未响应」。60s
// 仍无响应才判定驱动卡死并回退
const applySetupTimeout = 60 * time.Second

// StartedWithEnabled 当前守护会话启动时的启用状态（判断设置变更是否需重启）
func (s *Supervisor) StartedWithEnabled() bool {
	return s.startedEnabled
}

// currentToken 读取 IPC 启动令牌（持锁：与守护循环并发读写安全）
func (s *Supervisor) currentToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// ResolveEnginePath 解析引擎可执行文件路径：
// 显式覆盖 → 主程序同级 bin/（生产布局）→ 当前工作目录 bin/（go run 开发态）。
func ResolveEnginePath(override string) string {
	candidates := []string{}
	if override != "" {
		candidates = append(candidates, override)
	}
	if exeDir, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exeDir), "bin", "melodymate-engine.exe"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "bin", "melodymate-engine.exe"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, filepath.Join("bin", "melodymate-engine.exe"))
	}
	slog.Warn("[engine] 未找到 melodymate-engine.exe，使用首选候选路径", "candidates", candidates)
	return candidates[0]
}

// Start 异步启动守护主循环（幂等）
func (s *Supervisor) Start() {
	s.mu.Lock()
	if s.ctx != nil { // 已启动
		s.mu.Unlock()
		return
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.doneCh = make(chan struct{})
	s.mu.Unlock()

	// 启动清场：收割历史孤儿引擎（主程序被强杀且 Job 兜底失效的残留），
	// 避免与本次新引擎并存（"一次启动两个引擎"的根因之一）
	cleanupOrphanEngines(0)

	go s.loop()
}

// Stop 优雅停止引擎并结束守护循环（幂等）
func (s *Supervisor) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		cancel := s.cancel
		cli := s.client
		s.mu.Unlock()

		// 先取消上下文，再尝试协议层优雅退出：shutdown 请求走 Request
		// 需要拿事务锁，而 bounce（最长 30s）等在途请求会占住锁——先发
		// 请求会让应用退出卡顿到该请求超时。TryRequest 拿不到锁立即返回，
		// 引擎进程由下方 doneCh 兜底 / killProcess / Job Object 收尾
		if cancel != nil {
			cancel()
		}
		if cli != nil {
			_, _ = cli.TryRequest("shutdown", nil, 1*time.Second)
			_ = cli.Close()
		}
		// 等主循环收尾（其内部会 Kill 残留进程）；从未 Start 时 doneCh
		// 为 nil，直接跳过等待。doneCh 的赋值在 Start（非持锁）与关闭在
		// loop 的 defer（持锁与否均可）——Stop 与 Start 并发时无锁读取
		// 是数据竞态，这里持锁快照
		s.mu.Lock()
		doneCh := s.doneCh
		s.mu.Unlock()
		if doneCh == nil {
			return
		}
		select {
		case <-doneCh:
		case <-time.After(3 * time.Second):
			s.killProcess()
		}
	})
}

// Status 返回状态快照
func (s *Supervisor) Status() EngineStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.status
	st.SoundfontLoaded = s.hasSoundfont
	// 内置 piano/strings 的原生映射目标：每次快照实时计算，
	// 音色目录增删后无需重启即生效
	st.DefaultSoundfont = s.defaultSoundFontPath()
	if s.audio.EngineEnabled {
		st.Protocol = ProtocolVersion
	} else {
		st.State = StateDisabled
	}
	return st
}

// Ready 等待引擎就绪或 ctx/超时；返回可用的客户端引用。
// 引擎禁用或驻留失败态（可执行文件缺失等不可重试错误）时立即返回，
// 避免每次调用都等满超时。
func (s *Supervisor) Ready(timeout time.Duration) (*Client, error) {
	deadline := time.After(timeout)
	for {
		s.mu.RLock()
		state, cli, parked := s.status.State, s.client, s.parked
		s.mu.RUnlock()
		if state == StateReady && cli != nil {
			return cli, nil
		}
		if state == StateDisabled || (state == StateFailed && parked) {
			return nil, fmt.Errorf("引擎不可用（state=%s, last_error=%q）",
				state, s.LastError())
		}
		select {
		case <-deadline:
			return nil, fmt.Errorf("引擎未就绪（state=%s, last_error=%q）",
				state, s.LastError())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// LastError 最近一次错误信息快照
func (s *Supervisor) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status.LastError
}

// ApplySettings 更新音频设置；引擎就绪时立即下发 applySetup
func (s *Supervisor) ApplySettings(audio AudioSettings) error {
	s.mu.Lock()
	wasEnabled := s.audio.EngineEnabled
	s.audio = audio
	lastGood := s.lastGood
	s.mu.Unlock()

	// 守护循环处于 StateDisabled 驻留时，启用引擎需要唤醒它拉起进程
	if audio.EngineEnabled && !wasEnabled {
		select {
		case s.wakeCh <- struct{}{}:
		default:
		}
	}

	params := map[string]any{}
	if audio.Driver != "" {
		params["driver"] = audio.Driver
	}
	if audio.Device != "" {
		params["device"] = audio.Device
	}
	if audio.SampleRate > 0 {
		params["sampleRate"] = audio.SampleRate
	}
	if audio.BufferSize > 0 {
		params["bufferSize"] = audio.BufferSize
	}

	// 设备参数去重：与上次成功下发的配置一致时跳过 applySetup——
	// 仅切换 backend（AUTO/WEBAUDIO）不该重开声卡设备（ASIO 慢驱动
	// 重开会中断播放 10-20s，期间音符照常入队，恢复后齐鸣）
	if applyParamsEqual(lastGood, params) {
		return nil
	}

	s.mu.Lock()
	p := params
	s.lastApply = &p
	s.mu.Unlock()

	if len(params) == 0 {
		return nil
	}

	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		// 未就绪：设置已保存，引擎就绪后会由 loop 自动重放
		return nil
	}
	// 用 TryRequest 而非 Request：bounce 等长请求在途（事务锁被占）时
	// 立即失败返回，不在锁上无界排队——此前 HTTP 处理器会挂起整段
	// 渲染（最长 20 分钟），且排队期间若引擎侧消息线程被渲染占住，
	// applySetup 无人处理还会拖到 60s 超时杀死会话
	resp, err := cli.TryRequest("applySetup", params, applySetupTimeout)
	if err != nil {
		// 超时 = 驱动在引擎内卡死（消息线程阻塞在驱动打开上，进程无法恢复）；
		// 引擎忙 = 长请求在途，设置未生效。两种情况都回滚内存设置到最近
		// 一次可用配置并清掉重放标记：否则会话判死重启后会重放这份坏设置
		// 再次卡死，UI 永远停在"检测中"。引擎进程由 supervisor 现有的
		// 会话失效→重启机制拉起，落在可用设备上。
		s.mu.Lock()
		s.audio = s.lastGood
		s.lastApply = nil
		s.mu.Unlock()
		return err
	}
	if err := resp.Err(); err != nil {
		// 业务失败（典型：设备不在枚举列表）。设备从未生效，内存同样
		// 回滚到最近可用配置——调用方据 CurrentAudio 持久化，避免无效
		// 设备名留在 settings.json 里每次启动重放失败
		s.mu.Lock()
		s.audio = s.lastGood
		s.lastApply = nil
		s.mu.Unlock()
		return err
	}
	// 成功：记录为新的回退目标
	s.mu.Lock()
	s.lastGood = audio
	s.mu.Unlock()
	return nil
}

// applyParamsEqual 判断两组音频设置的设备参数是否一致（仅比较会下发
// applySetup 的字段：driver/device/sampleRate/bufferSize；backend、
// EngineEnabled 等不触发设备重开的字段不参与比较）
func applyParamsEqual(a AudioSettings, params map[string]any) bool {
	pa := map[string]any{}
	if a.Driver != "" {
		pa["driver"] = a.Driver
	}
	if a.Device != "" {
		pa["device"] = a.Device
	}
	if a.SampleRate > 0 {
		pa["sampleRate"] = a.SampleRate
	}
	if a.BufferSize > 0 {
		pa["bufferSize"] = a.BufferSize
	}
	if len(pa) != len(params) {
		return false
	}
	for k, v := range pa {
		if pv, ok := params[k]; !ok || pv != v {
			return false
		}
	}
	return true
}

// CurrentAudio 返回当前生效的音频设置（ApplySettings 超时回退后
// 调用方可据此持久化 settings.json，保持 UI 与实际设备一致）
func (s *Supervisor) CurrentAudio() AudioSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.audio
}

// TestTone 测试音开关
func (s *Supervisor) TestTone(on bool, freq float64) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	resp, err := cli.Request("testTone", map[string]any{"on": on, "freq": freq}, 10*time.Second)
	if err != nil {
		return err
	}
	return resp.Err()
}

// ListDevices 设备枚举透传
func (s *Supervisor) ListDevices() (*DeviceList, error) {
	cli, err := s.Ready(10 * time.Second)
	if err != nil {
		return nil, err
	}
	resp, err := cli.Request("listDevices", nil, s.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var dl DeviceList
	// 引擎返回 {"drivers":[{"driver":..,"devices":[{"name":..}]}]}，拍平设备名
	var rawList struct {
		Drivers []struct {
			Driver  string `json:"driver"`
			Devices []struct {
				Name string `json:"name"`
			} `json:"devices"`
		} `json:"drivers"`
	}
	if err := json.Unmarshal(resp.Result, &rawList); err != nil {
		return nil, err
	}
	for _, d := range rawList.Drivers {
		dt := DeviceType{Driver: d.Driver}
		for _, dev := range d.Devices {
			dt.Devices = append(dt.Devices, dev.Name)
		}
		dl.Drivers = append(dl.Drivers, dt)
	}
	return &dl, nil
}

// SetTrackMix 设置指定轨道的混音参数（音量、声相、静音、独奏、激活状态）
func (s *Supervisor) SetTrackMix(p TrackMixParams) error {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("引擎未就绪")
	}
	resp, err := cli.Request("setTrackMix", map[string]any{
		"track":  p.Track,
		"gain":   p.Gain,
		"pan":    p.Pan,
		"mute":   p.Mute,
		"solo":   p.Solo,
		"active": p.Active,
	}, s.cfg.RequestTimeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

/* ===== P4：FX 链与主总线 ===== */

// FxSlotParams 一个 FX 槽的状态（协议 v2 的 fx.setChain 元素）
type FxSlotParams struct {
	Slot   int     `json:"slot"`
	Plugin string  `json:"plugin,omitempty"` // 插件引用：P4 传空（装载在 P5/P6 开放）
	Bypass bool    `json:"bypass"`
	Wet    float64 `json:"wet"`
	Off    bool    `json:"off,omitempty"` // 显式卸载该槽
}

// SetFxChain 下发一条 FX 链（轨或主总线）。track < 0 表示主总线。
// 成功时把槽位状态记入 lastChains/lastMasterChain，崩溃重启后重放。
func (s *Supervisor) SetFxChain(track int, slots []FxSlotParams) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	payload := make([]map[string]any, 0, len(slots))
	for _, sl := range slots {
		item := map[string]any{
			"slot":   sl.Slot,
			"bypass": sl.Bypass,
			"wet":    sl.Wet,
		}
		if sl.Plugin != "" {
			item["plugin"] = sl.Plugin
		}
		if sl.Off {
			item["off"] = true
		}
		payload = append(payload, item)
	}
	params := map[string]any{"slots": payload}
	if track < 0 {
		params["master"] = true
	} else {
		params["track"] = track
	}
	resp, err := cli.Request("fx.setChain", params, 10*time.Second)
	if err != nil {
		return err
	}
	if err := resp.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if track < 0 {
		s.lastMasterChain = payload
	} else {
		if s.lastChains == nil {
			s.lastChains = make(map[int][]map[string]any)
		}
		s.lastChains[track] = payload
	}
	s.mu.Unlock()
	return nil
}

// SetMaster 设置主总线增益（0.0 ~ 4.0）。崩溃重启后重放。
func (s *Supervisor) SetMaster(gain float64) error {
	if gain < 0 || gain > 4 {
		return fmt.Errorf("gain 超出范围（0.0 ~ 4.0）: %v", gain)
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	resp, err := cli.Request("mixer.setMaster", map[string]any{"gain": gain}, 10*time.Second)
	if err != nil {
		return err
	}
	if err := resp.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastMasterGain = gain
	s.mu.Unlock()
	return nil
}

// GetLevelsSnapshot 一次 IPC 取回全部轨电平与主总线电平（P4 电平表轮询用：
// 电平表 20~30Hz，两次请求会白翻一倍管道往返）。
func (s *Supervisor) GetLevelsSnapshot() ([]float32, float32, error) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return nil, 0, fmt.Errorf("引擎未就绪")
	}
	resp, err := cli.TryRequest("getLevels", nil, s.cfg.RequestTimeout)
	if err != nil {
		return nil, 0, err
	}
	if err := resp.Err(); err != nil {
		return nil, 0, err
	}
	var res struct {
		Levels []float32 `json:"levels"`
		Master float32   `json:"master"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, 0, err
	}
	return res.Levels, res.Master, nil
}

// GetMasterLevel 主总线峰值电平（P4 起 getLevels 的 master 字段）
func (s *Supervisor) GetMasterLevel() (float32, error) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return 0, fmt.Errorf("引擎未就绪")
	}
	resp, err := cli.TryRequest("getLevels", nil, s.cfg.RequestTimeout)
	if err != nil {
		return 0, err
	}
	if err := resp.Err(); err != nil {
		return 0, err
	}
	var res struct {
		Master float32 `json:"master"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return 0, err
	}
	return res.Master, nil
}

// GetChain 读取某条链的槽位状态（诊断/前端回读用）
func (s *Supervisor) GetChain(track int) (map[string]any, error) {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	if track < 0 {
		params["master"] = true
	} else {
		params["track"] = track
	}
	resp, err := cli.Request("fx.getChain", params, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	return out, nil
}

/* ═══════════ P5：VST3 宿主 ═══════════ */

// PluginInfo 插件清单条目（引擎侧 JSON 的原样映射，前端/AI 都读这一份）
type PluginInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Vendor     string `json:"vendor"`
	Category   string `json:"category"`
	Instrument bool   `json:"instrument"`
	HasEditor  bool   `json:"hasEditor"`
	Path       string `json:"path"`
}

// PluginList 清单 + 已注册格式（诊断"为什么扫不到"用）
type PluginList struct {
	Count   int          `json:"count"`
	Plugins []PluginInfo `json:"plugins"`
	Formats []string     `json:"formats"`
}

// PluginScanStatus 扫描进度（异步扫描：start 立即返回，轮询这里看进度）
type PluginScanStatus struct {
	Running bool   `json:"running"`
	Scanned int    `json:"scanned"`
	Total   int    `json:"total"`
	Current string `json:"current"`
	Dirs    string `json:"dirs"`
	Count   int    `json:"count"`
}

// validateScanDirs 校验调用方给出的扫描目录：必须是存在的绝对目录。
// dirs 为空表示"用系统默认插件目录"（合法）。扫描会让宿主加载/解析
// 目录内的模块，因此不接受相对路径或不存在的路径——避免拼写错误被
// 静默忽略，也避免把扫描范围交给不明确的相对基准。真正的信任边界是
// IPC 启动令牌：只有持有令牌的调用方（本应用）能走到这里。
func validateScanDirs(dirs []string) error {
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !filepath.IsAbs(d) {
			return fmt.Errorf("扫描目录必须是绝对路径: %s", d)
		}
		st, err := os.Stat(d)
		if err != nil {
			return fmt.Errorf("扫描目录不存在: %s", d)
		}
		if !st.IsDir() {
			return fmt.Errorf("扫描目录不是文件夹: %s", d)
		}
	}
	return nil
}

// ScanPlugins 扫描插件目录（dirs 为空 = 系统默认目录）。
//
// 引擎侧扫描是异步的（同步应答会把管道拖死），这里启动后轮询到结束，
// 结束时引擎会把清单一起带回；随后落一份缓存，下次启动可秒开。
func (s *Supervisor) ScanPlugins(dirs []string, timeout time.Duration) (*PluginList, error) {
	if err := validateScanDirs(dirs); err != nil {
		return nil, err
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	params := map[string]any{"cache": s.pluginCachePath()}
	if len(dirs) > 0 {
		params["dirs"] = dirs
	}
	resp, err := cli.Request("plugin.scan", params, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			_, _ = cli.TryRequest("plugin.scanCancel", nil, 5*time.Second)
			return nil, fmt.Errorf("插件扫描超时（%s）", timeout)
		}
		time.Sleep(500 * time.Millisecond)
		st, err := cli.Request("plugin.scanStatus", params, 30*time.Second)
		if err != nil {
			return nil, err
		}
		if err := st.Err(); err != nil {
			return nil, err
		}
		var status struct {
			Running bool `json:"running"`
			List    struct {
				Count   int          `json:"count"`
				Plugins []PluginInfo `json:"plugins"`
				Formats []string     `json:"formats"`
			} `json:"list"`
		}
		if err := json.Unmarshal(st.Result, &status); err != nil {
			return nil, err
		}
		if status.Running {
			continue
		}
		return &PluginList{Count: status.List.Count, Plugins: status.List.Plugins, Formats: status.List.Formats}, nil
	}
}

// LoadPluginCache 载入上次的扫描缓存（启动时调用，避免每次重扫）
func (s *Supervisor) LoadPluginCache() (int, error) {
	cli, err := s.Ready(3 * time.Second)
	if err != nil {
		return 0, err
	}
	resp, err := cli.TryRequest("plugin.loadCache", map[string]any{"path": s.pluginCachePath()}, s.cfg.RequestTimeout)
	if err != nil {
		return 0, err
	}
	if err := resp.Err(); err != nil {
		return 0, err
	}
	var res struct {
		Loaded bool `json:"loaded"`
		Count  int  `json:"count"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return 0, err
	}
	if !res.Loaded {
		return 0, fmt.Errorf("无插件缓存")
	}
	return res.Count, nil
}

// ListPlugins 已知插件清单（不触发扫描）
func (s *Supervisor) ListPlugins() (*PluginList, error) {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return nil, err
	}
	resp, err := cli.Request("plugin.list", nil, s.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var out PluginList
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LoadInstrumentPlugin 把 VST3 乐器装到轨道（track < 0 表示主链 FX 槽）
func (s *Supervisor) LoadPlugin(track, slot int, pluginID string) error {
	if strings.TrimSpace(pluginID) == "" {
		return fmt.Errorf("缺少插件 id")
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	params := map[string]any{"id": pluginID}
	if slot >= 0 {
		params["slot"] = slot
		if track < 0 {
			params["master"] = true
		} else {
			params["track"] = track
		}
	} else {
		params["track"] = track
	}
	// 插件实例化要加载 DLL，给足时间（大插件可能数秒）
	resp, err := cli.Request("plugin.load", params, 60*time.Second)
	if err != nil {
		return err
	}
	return resp.Err()
}

// ValidateMmPluginPath 导出的 MM 插件原生库路径校验：必须位于 pluginsDir
// 内（含符号链接解析后的复核）。供 HTTP 入口在调用前先行拒绝，形成双端校验。
func ValidateMmPluginPath(pluginsDir, dllPath string) error {
	return validateMmPluginPath(pluginsDir, dllPath)
}

// validateMmPluginPath 校验 MM 插件原生库路径必须位于已安装插件目录内。
// fail-closed：未配置 PluginsDir 时一律拒绝（宁可报错也不加载来路不明的
// 原生库）。先按词法包含性判定，再解析符号链接/目录联接后复核一次，
// 避免经由联接指向目录之外。
func validateMmPluginPath(pluginsDir, dllPath string) error {
	root := strings.TrimSpace(pluginsDir)
	if root == "" {
		return fmt.Errorf("未配置 MM 插件目录，拒绝加载原生库")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("插件目录无效: %w", err)
	}
	full, err := filepath.Abs(dllPath)
	if err != nil {
		return fmt.Errorf("插件库路径无效: %w", err)
	}
	contained := func(base, target string) bool {
		rel, rerr := filepath.Rel(base, target)
		return rerr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if !contained(rootAbs, full) {
		return fmt.Errorf("插件库必须位于已安装插件目录内（%s）", rootAbs)
	}
	// 解析真实路径后复核：目录联接/符号链接不能把加载带到目录之外。
	// 目标不存在时 EvalSymlinks 会失败，此时按词法结果拒绝（fail-closed）。
	realRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return fmt.Errorf("插件目录不可解析: %w", err)
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return fmt.Errorf("插件库不存在或不可解析: %w", err)
	}
	if !contained(realRoot, realFull) {
		return fmt.Errorf("插件库真实路径越出插件目录（%s）", realRoot)
	}
	return nil
}

// LoadMmPlugin 装载 MM 插件（.mm 包解包后的原生库）到 FX 槽。
// 引擎侧按 format=mm 分流：path 是库文件路径，不走 VST3 清单。
//
// 路径必须位于已安装插件目录（Config.PluginsDir）内：引擎对该路径直接
// 走 LoadLibrary，不做目录约束，因此包含性校验必须在信任方（本进程）完成。
func (s *Supervisor) LoadMmPlugin(track, slot int, dllPath string) (map[string]any, error) {
	dllPath = strings.TrimSpace(dllPath)
	if dllPath == "" {
		return nil, fmt.Errorf("缺少 MM 插件库路径")
	}
	if err := validateMmPluginPath(s.cfg.PluginsDir, dllPath); err != nil {
		return nil, err
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return nil, err
	}
	/* id 也带库路径：引擎的 plugin.load 先做 id 必填校验，format=mm 分支在其后 */
	params := map[string]any{"format": "mm", "path": dllPath, "id": dllPath}
	/* slot 只在真的给了槽位时才带：presence 决定"效果器进槽"还是"乐器挂轨道"（引擎按此分流） */
	if slot >= 0 {
		params["slot"] = slot
	}
	if track < 0 {
		params["master"] = true
	} else {
		params["track"] = track
	}
	resp, err := cli.Request("plugin.load", params, 60*time.Second)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var out map[string]any
	_ = json.Unmarshal(resp.Result, &out)
	return out, nil
}

// UnloadPlugin 卸载插件（slot < 0 卸乐器）
func (s *Supervisor) UnloadPlugin(track, slot int) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	params := map[string]any{}
	if slot >= 0 {
		params["slot"] = slot
		if track < 0 {
			params["master"] = true
		} else {
			params["track"] = track
		}
	} else {
		params["track"] = track
	}
	resp, err := cli.Request("plugin.unload", params, 30*time.Second)
	if err != nil {
		return err
	}
	return resp.Err()
}

// pluginTarget 组装插件定位参数（乐器 / 轨 FX 槽 / 主链 FX 槽）
func pluginTarget(track, slot int) map[string]any {
	params := map[string]any{}
	if slot >= 0 {
		params["slot"] = slot
		if track < 0 {
			params["master"] = true
		} else {
			params["track"] = track
		}
	} else {
		params["track"] = track
	}
	return params
}

// PluginParams 读插件参数表（±1 归一化值 + 显示文本）
func (s *Supervisor) PluginParams(track, slot int) (map[string]any, error) {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return nil, err
	}
	resp, err := cli.Request("plugin.params", pluginTarget(track, slot), 30*time.Second)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	if e, ok := out["error"].(string); ok && e != "" {
		return nil, fmt.Errorf("%s", e)
	}
	return out, nil
}

// SetPluginParam 写参数（0~1 归一化）
func (s *Supervisor) SetPluginParam(track, slot, index int, value float64) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	params := pluginTarget(track, slot)
	params["index"] = index
	params["value"] = value
	resp, err := cli.Request("plugin.setParam", params, 30*time.Second)
	if err != nil {
		return err
	}
	return resp.Err()
}

// OpenPluginEditor 打开插件原生编辑器（引擎侧消息线程开窗）
func (s *Supervisor) OpenPluginEditor(track, slot int) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	resp, err := cli.Request("plugin.openEditor", pluginTarget(track, slot), 30*time.Second)
	if err != nil {
		return err
	}
	return resp.Err()
}

// ClosePluginEditor 关闭插件编辑器
func (s *Supervisor) ClosePluginEditor() error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	resp, err := cli.TryRequest("plugin.closeEditor", nil, s.cfg.RequestTimeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// PluginState 宿主状态（哪些轨有乐器、哪些槽有效果器）
func (s *Supervisor) PluginState() (map[string]any, error) {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return nil, err
	}
	resp, err := cli.Request("plugin.state", nil, s.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// pluginCachePath 插件清单缓存的落盘位置（与音色目录同级的 Library/ 下）
func (s *Supervisor) pluginCachePath() string {
	sfDir, _, _ := s.dirs()
	if sfDir == "" {
		return ""
	}
	// sfDir 形如 <数据根>/Library/soundfonts；缓存放同级
	return filepath.Join(filepath.Dir(sfDir), "vst3-cache.xml")
}

// SetTrackVoice 把引擎轨道切换到内置波形声部（合成波音色的原生渲染路径，
// 不依赖 SF2）。voice 内容见协议文档 setTrackVoice；崩溃重启后随会话重放
func (s *Supervisor) SetTrackVoice(track int, voice map[string]any) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	params := make(map[string]any, len(voice)+1)
	for k, v := range voice {
		params[k] = v
	}
	params["track"] = track
	resp, err := cli.Request("setTrackVoice", params, 10*time.Second)
	if err != nil {
		return err
	}
	if err := resp.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.lastVoices == nil {
		s.lastVoices = make(map[int]map[string]any)
	}
	s.lastVoices[track] = voice
	s.mu.Unlock()
	return nil
}

// SetTrackPreset 选择轨道 SF2 的预设（多预设音色库）。bank/program 为
// General MIDI 编号；预设不存在由引擎侧拒绝。崩溃重启后随会话重放
// （loadSoundFont → setTrackPreset → setTrackVoice 顺序）
func (s *Supervisor) SetTrackPreset(track, bank, program int) error {
	if track < 0 || track >= 32 {
		return fmt.Errorf("track 超出范围（0-31）: %d", track)
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	resp, err := cli.Request("setTrackPreset", map[string]any{
		"track": track, "bank": bank, "program": program,
	}, 10*time.Second)
	if err != nil {
		return err
	}
	if err := resp.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.lastPresets == nil {
		s.lastPresets = make(map[int][2]int)
	}
	s.lastPresets[track] = [2]int{bank, program}
	s.mu.Unlock()
	return nil
}

// Click 节拍器木鱼音（引擎侧合成，见协议文档 click）。实时性要求高：
// 直呼当前会话，引擎未就绪/重启期间静默丢弃（节拍器可接受瞬断）
func (s *Supervisor) Click(track int, high bool) error {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("引擎未就绪")
	}
	resp, err := cli.Request("click", map[string]any{"track": track, "high": high}, s.cfg.RequestTimeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// OpenControlPanel 打开当前声卡的控制面板（ASIO 驱动专用）
func (s *Supervisor) OpenControlPanel() (bool, error) {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli == nil {
		return false, fmt.Errorf("引擎未就绪")
	}
	resp, err := cli.Request("openControlPanel", map[string]any{}, s.cfg.RequestTimeout)
	if err != nil {
		return false, err
	}
	if err := resp.Err(); err != nil {
		return false, err
	}
	var res struct {
		Opened bool `json:"opened"`
	}
	_ = json.Unmarshal(resp.Result, &res)
	return res.Opened, nil
}

// setState 更新状态（带日志）
func (s *Supervisor) setState(state EngineState, lastErr string) {
	s.mu.Lock()
	s.status.State = state
	if lastErr != "" {
		s.status.LastError = lastErr
	}
	s.mu.Unlock()
	if lastErr != "" {
		slog.Warn("[engine] 状态变更", "state", state, "err", lastErr)
	} else {
		slog.Info("[engine] 状态变更", "state", state)
	}
}

// killProcess 强制终止当前引擎进程
func (s *Supervisor) killProcess() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (s *Supervisor) loop() {
	defer close(s.doneCh)

	// 禁用启动时驻留，等待"设置页启用引擎"唤醒（此前一旦禁用启动，
	// 后续启用也不会拉起守护循环）。醒来后若仍禁用（唤醒间隙又被关掉）
	// 必须重新驻留而非退出：本 goroutine 是唯一能拉起引擎的执行流，
	// 退出后 wakeCh 再无读者，引擎直到重启应用都无法再启动
	for {
		// 快照后判读：此前无锁读 s.audio，与 ApplySettings 的持锁写构成数据竞态
		s.mu.RLock()
		enabled := s.audio.EngineEnabled
		s.mu.RUnlock()
		if enabled {
			break
		}
		s.setState(StateDisabled, "")
		select {
		case <-s.ctx.Done():
			return
		case <-s.wakeCh:
		}
	}

	consecutiveFail := 0
	for {
		select {
		case <-s.ctx.Done():
			s.setState(StateStopped, "")
			s.killProcess()
			return
		default:
		}

		reason := s.runOnce()
		// runOnce 返回即意味着会话失效（进程退出 / 心跳失败 / ctx 取消）
		// reason 为空 = 正常停止（外层随即 return），不参与失败计数。

		select {
		case <-s.ctx.Done():
			s.setState(StateStopped, "")
			s.killProcess()
			return
		default:
		}

		// 秒崩判据：上次会话在进入 Ready 后极短时间内就崩溃 = 引擎本体不可用
		// （驱动损坏/坏 exe），连续失败计数继续累计；若会话曾稳定运行过
		// （Ready 后存活超过秒崩窗口），崩溃属偶发，清零后重计——否则零星
		// 闪退会在多天里把计数累计到熔断阈值，误伤可恢复场景。
		s.mu.RLock()
		readyAt := s.lastReadyAt
		s.mu.RUnlock()
		if !readyAt.IsZero() && time.Since(readyAt) > crashWindow {
			consecutiveFail = 0
		}

		// 连续快速失败退避：500ms 起，封顶 3s。防无限重启风暴：引擎不可修复
		// 地秒崩（驱动损坏、exe 被替换成坏文件等）时若无熔断会每 3s kill→重启
		// 一次直到应用退出——CPU/日志/声卡抢占无意义且掩盖真实故障。达到
		// maxConsecutiveRestarts 后驻留失败态，不再自动拉起（用户重新启用
		// 引擎或重启应用后重新开始）。
		const (
			maxConsecutiveRestarts = 10
			maxRestartBackoff      = 3 * time.Second
		)
		consecutiveFail++
		s.setState(StateRestarting,
			fmt.Sprintf("第 %d 次重启: %s", consecutiveFail, reason))

		if consecutiveFail >= maxConsecutiveRestarts {
			slog.Error("引擎连续重启次数过多，已停止自动重启",
				"restarts", consecutiveFail, "reason", reason)
			s.setState(StateFailed, "引擎连续崩溃超过 10 次，已停止自动重启")
			s.parkUntilDone()
			return
		}

		backoff := time.Duration(consecutiveFail) * 500 * time.Millisecond
		if backoff > maxRestartBackoff {
			backoff = maxRestartBackoff
		}
		select {
		case <-s.ctx.Done():
			s.setState(StateStopped, "")
			s.killProcess()
			return
		case <-time.After(backoff):
		}
	}
}

// crashWindow 秒崩判定窗口：Ready 后存活超过该时长视为会话健康
const crashWindow = 30 * time.Second

// parkUntilDone 驻留直至守护器停止（用于不可重试的部署类错误）
func (s *Supervisor) parkUntilDone() {
	s.mu.Lock()
	s.parked = true
	s.mu.Unlock()
	<-s.ctx.Done()
	s.setState(StateStopped, "")
}

// isExecNotFound 判断启动失败是否因可执行文件不存在
func isExecNotFound(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return os.IsNotExist(execErr.Err)
	}
	return os.IsNotExist(err)
}

// openEngineLog 打开引擎输出日志（output/engine.log，追加模式）。
// 引擎 stdout/stderr 不能透传 os.Stdout/Stderr：windowsgui（-H windowsgui）
// 构建里这两个句柄无效，cmd.Start 直接报 "The handle is invalid"，
// 引擎永远启动失败并陷入重启风暴。打开失败返回 nil（调用方保持
// Stdout/Stderr 为 nil，等效丢弃）。outputDir 取自 cfg.Dirs 注入，
// 避免 engine→config 循环导入
func (s *Supervisor) openEngineLog() *os.File {
	if s.cfg.Dirs == nil {
		return nil
	}
	_, outputDir, _ := s.cfg.Dirs()
	if outputDir == "" {
		return nil
	}
	_ = os.MkdirAll(outputDir, 0755)
	f, err := os.OpenFile(filepath.Join(outputDir, "engine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Warn("[engine] 引擎日志文件打开失败，本次会话输出丢弃", "err", err)
		return nil
	}
	return f
}

// runOnce 单次完整会话：拉起进程 → 连接握手 → 心跳监控 → 会话失效返回。
// 返回失败原因（空串 = ctx 取消），供重启消息展示真实原因
func (s *Supervisor) runOnce() string {
	s.setState(StateStarting, "")

	cmd := exec.Command(s.exePath, "--parent", strconv.Itoa(os.Getpid()))
	/* 把启动令牌注入子进程环境：引擎要求 hello 携带同一令牌，
	   未持有令牌的本机进程无法直连管道调用业务方法。 */
	token := s.currentToken()
	if token != "" {
		cmd.Env = append(os.Environ(), engineTokenEnv+"="+token)
	}
	if outF := s.openEngineLog(); outF != nil {
		cmd.Stdout = outF
		cmd.Stderr = outF
		// 会话结束时关闭父进程侧句柄；子进程持有继承句柄，不受影响
		defer outF.Close()
	}
	// 隐藏引擎的控制台窗口（JUCE 引擎为 console 子系统，默认会弹出黑窗）；
	// 平台相关属性收敛到 engineSysProcAttr（非 Windows 返回 nil）
	cmd.SysProcAttr = engineSysProcAttr()
	if err := cmd.Start(); err != nil {
		reason := "启动失败: " + err.Error()
		s.setState(StateFailed, reason)
		// 引擎文件缺失属于部署问题，重试无意义：驻留失败态直至主程序退出
		// （parkUntilDone 不会检测文件重新出现；装好引擎后需重启应用）
		if os.IsNotExist(err) || isExecNotFound(err) {
			s.parkUntilDone()
			return reason
		}
		time.Sleep(time.Second)
		return reason
	}

	s.mu.Lock()
	s.cmd = cmd
	s.status.PID = cmd.Process.Pid
	s.mu.Unlock()

	// 挂入 Job Object：主程序异常退出时内核兜底收割引擎，杜绝孤儿进程
	attachProcessToJob(cmd.Process)

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	cli, err := DialWithToken(cmd.Process.Pid, s.currentToken(), s.cfg.DialTimeout, s.cfg.HandshakeTimeout)
	if err != nil {
		reason := "连接失败: " + err.Error()
		s.setState(StateFailed, reason)
		if kerr := cmd.Process.Kill(); kerr != nil {
			slog.Warn("[engine] 连接失败后终止引擎出错（可能已退出）", "pid", cmd.Process.Pid, "err", kerr.Error())
		}
		// 等待退出带 5 秒上限：进程若卡内核态（个别驱动）不再冻结守护循环；
		// 极端未退出场景由引擎侧父进程看门狗兜底
		select {
		case <-waitCh:
		case <-time.After(5 * time.Second):
			slog.Warn("[engine] 引擎终止确认超时，继续重启流程", "pid", cmd.Process.Pid)
			_ = cmd.Process.Kill()
		}
		return reason
	}

	s.mu.Lock()
	s.client = cli
	s.sessionCnt++
	if s.sessionCnt > 1 {
		s.status.Restarts = s.sessionCnt - 1
	}
	s.lastReadyAt = time.Now()
	s.mu.Unlock()
	s.setState(StateReady, "")

	// 重放最近的音频设置（崩溃恢复对上层透明）
	s.mu.RLock()
	lastApply := s.lastApply
	audio := s.audio
	s.mu.RUnlock()

	// 会话建立后应用音频设置：重启重放优先，否则应用持久化配置（首启）
	params := map[string]any{}
	if lastApply != nil && len(*lastApply) > 0 {
		params = *lastApply
	} else {
		if audio.Driver != "" {
			params["driver"] = audio.Driver
		}
		if audio.Device != "" {
			params["device"] = audio.Device
		}
		if audio.SampleRate > 0 {
			params["sampleRate"] = audio.SampleRate
		}
		if audio.BufferSize > 0 {
			params["bufferSize"] = audio.BufferSize
		}
	}
	failReason := ""
	if len(params) > 0 {
		if resp, err := cli.Request("applySetup", params, applySetupTimeout); err != nil {
			slog.Warn("[engine] 应用音频设置失败", "err", err)
			// 冷启动重放卡死（驱动消息线程阻塞无法恢复）：回滚到系统默认
			// 设备并清重放标记，本会话落在可用设备上；下次完整重启再试
			s.mu.Lock()
			s.audio = AudioSettings{
				EngineEnabled: s.audio.EngineEnabled,
				// 保留显式引擎路径覆盖：回滚的是设备配置，不该抹掉
				// engine_path（此前被静默清空并随 OnAudioFallback 落盘）
				EnginePath: s.audio.EnginePath,
			}
			s.lastApply = nil
			s.lastGood = s.audio
			fallback := s.audio
			s.mu.Unlock()
			// 持久化回滚结果（回调由 main 注入）：否则每次启动都会重演
			// 「重放坏设置 → 卡死 → 回滚」，冷启动每次多等一个超时周期
			if s.cfg.OnAudioFallback != nil {
				s.cfg.OnAudioFallback(fallback)
			}
			// 传输层失败意味着会话已判死（Request 出错路径必然 markDead）：
			// 立即走下方清场重启。此前会继续在死会话上逐轨重放音色（每轨
			// 烧满 30s），而 StateReady 已置位——UI 显示就绪实际引擎卡死，
			// 恢复延迟 15+30×N 秒
			select {
			case <-cli.Done():
				failReason = "冷启动重放失败: 应用音频设置超时"
			default:
			}
		} else if err := resp.Err(); err != nil {
			slog.Warn("[engine] 应用音频设置失败（业务拒绝）", "err", err)
			// 业务失败（典型：设备不在枚举列表）：设备从未生效，同样回滚
			// 到最近可用配置并持久化——否则坏设置留在 settings.json，
			// 每次冷启动重演失败（此前只写日志，UI 表现为改了什么没反应）
			s.mu.Lock()
			s.audio = s.lastGood
			s.lastApply = nil
			fallback := s.audio
			s.mu.Unlock()
			if s.cfg.OnAudioFallback != nil {
				s.cfg.OnAudioFallback(fallback)
			}
		} else {
			s.mu.Lock()
			p := params
			s.lastApply = &p
			s.lastGood = audio
			s.mu.Unlock()
		}
	}

	// 会话建立后加载音色：显式加载过的优先重放（崩溃恢复），
	// 否则取音色目录首个 SF2 作为默认——否则原生演奏路径静默
	// （引擎合成器无音色时 render 直接返回 false）
	// 多轨：逐轨重放 lastSoundFonts，track 0 兼容旧单值
	s.mu.RLock()
	lastSF := s.lastSoundFont
	fonts := make(map[int]string, len(s.lastSoundFonts))
	for k, v := range s.lastSoundFonts {
		fonts[k] = v
	}
	s.mu.RUnlock()
	if len(fonts) == 0 && lastSF != "" {
		fonts[0] = lastSF
	}
	if len(fonts) == 0 {
		if def := s.defaultSoundFontPath(); def != "" {
			fonts[0] = def
		}
	}
	if failReason == "" {
		loadedFonts := 0
		// 新会话重置音色状态：本会话能否加载成功以本次重放结果为准
		s.mu.Lock()
		s.hasSoundfont = false
		s.mu.Unlock()
		if len(fonts) > 0 {
			for tr, p := range fonts {
				if err := s.loadSoundFontWithTrack(cli, tr, p); err != nil {
					// 会话失效（如上一请求期间判死）：中止重放走清场重启，
					// 不再逐轨烧满 30s 超时
					select {
					case <-cli.Done():
						failReason = "冷启动重放失败: IPC 会话失效"
					default:
						slog.Warn("[engine] 加载音色失败（track)", "track", tr, "path", p, "err", err)
					}
				} else {
					loadedFonts++
					slog.Info("[engine] 已加载音色", "track", tr, "path", p)
				}
				if failReason != "" {
					break
				}
			}
		} else {
			slog.Info("[engine] 未找到默认音色（Library/soundfonts/*.sf2），SF2 轨将走逐轨回退或保持静默")
		}
		s.mu.Lock()
		s.hasSoundfont = loadedFonts > 0
		s.mu.Unlock()
	}

	// 重放预设选择（多预设 SF2）：音色重放之后、波形声部之前——
	// loadSoundFont 会重置预设状态，顺序颠倒预设会被默认 (0,0) 覆盖；
	// setTrackVoice 会撤下 SF2，必须排在最后
	s.mu.RLock()
	presets := make(map[int][2]int, len(s.lastPresets))
	for k, v := range s.lastPresets {
		presets[k] = v
	}
	s.mu.RUnlock()
	if failReason == "" {
		for tr, bp := range presets {
			if _, err := cli.Request("setTrackPreset", map[string]any{
				"track": tr, "bank": bp[0], "program": bp[1],
			}, 10*time.Second); err != nil {
				select {
				case <-cli.Done():
					failReason = "冷启动重放失败: IPC 会话失效"
				default:
					slog.Warn("[engine] 重放预设选择失败（track)", "track", tr, "err", err)
				}
			}
			if failReason != "" {
				break
			}
		}
	}

	// 重放波形声部（合成波音色的原生路径）：音色重放之后执行——loadSoundFont
	// 会撤下波形声部，顺序颠倒会让波形轨被默认音色覆盖
	s.mu.RLock()
	voices := make(map[int]map[string]any, len(s.lastVoices))
	for tr, v := range s.lastVoices {
		cp := make(map[string]any, len(v))
		for k, val := range v {
			cp[k] = val
		}
		voices[tr] = cp
	}
	s.mu.RUnlock()
	if failReason == "" {
		for tr, v := range voices {
			params := make(map[string]any, len(v)+1)
			for k, val := range v {
				params[k] = val
			}
			params["track"] = tr
			if _, err := cli.Request("setTrackVoice", params, 10*time.Second); err != nil {
				select {
				case <-cli.Done():
					failReason = "冷启动重放失败: IPC 会话失效"
				default:
					slog.Warn("[engine] 重放波形声部失败（track)", "track", tr, "err", err)
				}
			}
			if failReason != "" {
				break
			}
		}
	}

	/* P6：启动即载入插件清单缓存（存在时）。
	   价值有两层：① 用户不必每次启动重扫插件目录（扫描要逐个加载插件，几十秒起）；
	   ② 扫描会"加载再卸载"每个插件，某些插件在随后被真正实例化时会崩——
	   走缓存路径完全不碰插件二进制，能规避这一类问题。
	   缓存缺失/损坏时静默跳过（回落到显式扫描）。 */
	if failReason == "" {
		if n, err := s.LoadPluginCache(); err != nil {
			slog.Debug("[engine] 无插件清单缓存（首次启动属正常）", "err", err)
		} else {
			slog.Info("[engine] 插件清单缓存已载入", "count", n)
		}
	}
	/* 重放 FX 链（P4）：排在 setTrackVoice 之后——链是"轨声部之后"的处理位点。
	   master 链与各轨链都重放，顺序与前端一致性无关（链参数是绝对状态）。 */
	s.mu.RLock()
	chains := make(map[int][]map[string]any, len(s.lastChains))
	for tr, c := range s.lastChains {
		cp := make([]map[string]any, len(c))
		copy(cp, c)
		chains[tr] = cp
	}
	masterChain := append([]map[string]any(nil), s.lastMasterChain...)
	masterGain, hasMasterGain := s.lastMasterGain, s.lastMasterGain > 0
	s.mu.RUnlock()
	if failReason == "" {
		for tr, slots := range chains {
			if _, err := cli.Request("fx.setChain", map[string]any{
				"track": tr, "slots": slots,
			}, 10*time.Second); err != nil {
				select {
				case <-cli.Done():
					failReason = "冷启动重放失败: IPC 会话失效"
				default:
					slog.Warn("[engine] 重放 FX 链失败（track)", "track", tr, "err", err)
				}
			}
			if failReason != "" {
				break
			}
		}
	}
	if failReason == "" && len(masterChain) > 0 {
		if _, err := cli.Request("fx.setChain", map[string]any{
			"master": true, "slots": masterChain,
		}, 10*time.Second); err != nil {
			select {
			case <-cli.Done():
				failReason = "冷启动重放失败: IPC 会话失效"
			default:
				slog.Warn("[engine] 重放主总线 FX 链失败", "err", err)
			}
		}
	}
	if failReason == "" && hasMasterGain {
		if _, err := cli.Request("mixer.setMaster", map[string]any{
			"gain": masterGain,
		}, 10*time.Second); err != nil {
			select {
			case <-cli.Done():
				failReason = "冷启动重放失败: IPC 会话失效"
			default:
				slog.Warn("[engine] 重放主总线增益失败", "err", err)
			}
		}
	}

	// 心跳 + 进程退出 + IPC 失联三通道监控
	heartbeatStop := make(chan struct{})
	if failReason == "" {
		go func() {
			ticker := time.NewTicker(s.cfg.PingInterval)
			defer ticker.Stop()
			failures := 0
			for {
				select {
				case <-heartbeatStop:
					return
				case <-ticker.C:
					// 长请求（如 Bounce 30s）期间跳过本次，避免被大任务饿死
					ok, err := cli.TryPing(5 * time.Second)
					if !ok && err == nil {
						continue
					}
					if err != nil {
						failures++
						// 引擎阻塞在慢驱动操作（ASIO Link Pro 首次打开可达
						// 10-20s）时无法应答心跳，须给足容忍——阈值过低会把
						// 正常的设备切换误判为会话失效，陷入重启风暴。
						// 真崩溃走 waitCh 进程退出通道，秒级检测不受影响。
						if failures >= 15 {
							slog.Warn("[engine] 心跳连续失败，判定会话失效", "failures", failures)
							cli.Close() // 触发下方 cli.Done() 路径
							return
						}
					} else {
						failures = 0
					}
				}
			}
		}()

		select {
		case <-s.ctx.Done():
			failReason = ""
		case werr := <-waitCh:
			failReason = fmt.Sprintf("引擎进程退出: %v", werr)
		case <-cli.Done():
			failReason = "IPC 会话失效"
		}
	}

	close(heartbeatStop)

	s.mu.Lock()
	s.client = nil
	s.mu.Unlock()
	cli.Close()

	if failReason != "" {
		s.setState(StateFailed, failReason)
		// 进程若还活着（如 IPC 失联），强杀以让下一轮重启接管
		s.killProcess()
		select {
		case <-waitCh:
		case <-time.After(2 * time.Second):
		}
	}
	return failReason
}

// LoadSoundFont 加载音色文件到引擎（track 0 兼容）
func (s *Supervisor) LoadSoundFont(path string) error {
	return s.LoadSoundFontTrack(0, path)
}

// LoadSoundFontTrack 指定轨道加载音色
func (s *Supervisor) LoadSoundFontTrack(track int, path string) error {
	// 引擎合成器 32 轨上限：越界 track 会被引擎静默钳到 0（音色被加载到
	// 错误轨道，前端以为加载到目标轨）——入口直接拒绝
	if track < 0 || track >= 32 {
		return fmt.Errorf("track 超出范围（0-31）: %d", track)
	}
	if err := s.validateSoundFontPath(path); err != nil {
		return err
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	if err := s.loadSoundFontWithTrack(cli, track, path); err != nil {
		return err
	}
	s.mu.Lock()
	if s.lastSoundFonts == nil {
		s.lastSoundFonts = make(map[int]string)
	}
	s.lastSoundFonts[track] = path
	s.hasSoundfont = true
	if track == 0 {
		s.lastSoundFont = path
	}
	s.mu.Unlock()
	return nil
}

// loadSoundFontWith 在指定会话上加载音色并解析结果（track 0）
func (s *Supervisor) loadSoundFontWith(cli *Client, path string) error {
	return s.loadSoundFontWithTrack(cli, 0, path)
}

// loadSoundFontWithTrack 指定轨道
func (s *Supervisor) loadSoundFontWithTrack(cli *Client, track int, path string) error {
	resp, err := cli.Request("loadSoundFont", map[string]any{"path": path, "track": track}, 30*time.Second)
	if err != nil {
		return err
	}
	if err := resp.Err(); err != nil {
		return err
	}
	var res struct {
		Loaded bool   `json:"loaded"`
		Error  string `json:"error"`
	}
	if jsonErr := json.Unmarshal(resp.Result, &res); jsonErr != nil {
		return jsonErr
	}
	if !res.Loaded {
		if res.Error != "" {
			return fmt.Errorf("引擎加载音色失败: %s", res.Error)
		}
		return fmt.Errorf("引擎加载音色失败: %s", path)
	}
	return nil
}

// defaultSoundFontPath 返回音色目录（按文件名排序）的首个 SF2；无则空串。
// 目录约定与 handler_soundfont.go 的上传落盘位置一致。
func (s *Supervisor) defaultSoundFontPath() string {
	if s.cfg.SoundFontDir == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(s.cfg.SoundFontDir, "*.sf2"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[0]
}

// NoteOn/NoteOff 演奏事件：走二进制 Midi 帧（协议规定实时消息禁止 JSON 化），
// 尽力而为——引擎未就绪时静默丢弃，不阻塞前端键盘路径。
// 兼容旧单轨（track 0）；新多轨请用 NoteOnTrack/NoteOffTrack。
func (s *Supervisor) NoteOn(channel, key, velocity int) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return
	}
	_ = cli.NoteOn(channel, key, velocity)
}

func (s *Supervisor) NoteOff(channel, key int) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return
	}
	_ = cli.NoteOff(channel, key)
}

// NoteOnTrack/NoteOffTrack 每轨独立 SF2 的演奏事件（编曲多轨）
func (s *Supervisor) NoteOnTrack(track, key, velocity int) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return
	}
	_ = cli.NoteOnTrack(track, key, velocity)
}

func (s *Supervisor) NoteOffTrack(track, key int) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return
	}
	_ = cli.NoteOffTrack(track, key)
}

// ScheduleSamples 批量调度音频素材（走带位置驱动，统一尾音）
func (s *Supervisor) ScheduleSamples(clips []map[string]any, bpm float64) error {
	_, _, materialDirs := s.dirs()
	if err := validateClipsPaths(materialDirs, clips); err != nil {
		return err
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	return cli.ScheduleSamples(clips, bpm, s.cfg.RequestTimeout)
}

// ClearSamples 清空素材调度
func (s *Supervisor) ClearSamples() error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	return cli.ClearSamples(s.cfg.RequestTimeout)
}

// ScheduleNotes 批量调度 MIDI
func (s *Supervisor) ScheduleNotes(notes []map[string]any, bpm float64) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	return cli.ScheduleNotes(notes, bpm, s.cfg.RequestTimeout)
}

// ClearNotes 清空 MIDI 调度
func (s *Supervisor) ClearNotes() error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	return cli.ClearNotes(s.cfg.RequestTimeout)
}

// Bounce 离线渲染
func (s *Supervisor) Bounce(params map[string]any) (string, error) {
	cli, err := s.Ready(10 * time.Second)
	if err != nil {
		return "", err
	}
	// 输出路径白名单：缺省时注入默认 output 目录，显式传入则必须位于 output 内
	_, outputDir, materialDirs := s.dirs()
	outPath, _ := params["path"].(string)
	if strings.TrimSpace(outPath) == "" {
		if outputDir == "" {
			return "", fmt.Errorf("bounce 输出目录未配置，已拒绝渲染")
		}
		params["path"] = filepath.Join(outputDir, fmt.Sprintf("bounce_%s.wav",
			time.Now().Format("20060102_150405.000")))
		outPath, _ = params["path"].(string)
	} else if outputDir == "" || !isSubPathFS(outputDir, outPath) {
		return "", fmt.Errorf("bounce 输出路径不在允许的输出目录内，已拒绝: %s", outPath)
	}
	_ = os.MkdirAll(filepath.Dir(outPath), 0755)
	if err := validateClipsPaths(materialDirs, mapsFromAny(params["clips"])); err != nil {
		return "", err
	}
	if err := validateBounceTracksPaths(materialDirs, mapsFromAny(params["tracks"])); err != nil {
		return "", err
	}
	return cli.Bounce(params, estimateBounceTimeout(params))
}

// estimateBounceTimeout 按渲染时长估算 bounce 超时。此前固定 30s，而引擎允许
// 渲染最长 600s 音频：长工程导出必然超时 → 会话判死 → supervisor 把正在写
// WAV 的引擎进程杀掉重建。离线渲染通常远快于实时，但逐块混音/重采样开销
// 无上界，按 2 倍音频时长 + 60s 余量兜底（下限 30s，上限 20min）。
func estimateBounceTimeout(params map[string]any) time.Duration {
	anyFloat := func(v any) (float64, bool) {
		switch n := v.(type) {
		case float64:
			return n, true
		case int:
			return float64(n), true
		case int64:
			return float64(n), true
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			return f, err == nil
		}
		return 0, false
	}
	bpm, _ := anyFloat(params["bpm"])
	if bpm < 20 {
		bpm = 120 // 与引擎 (bpm > 20 ? bpm : 120) 一致
	}
	maxEnd, _ := anyFloat(params["beats"])
	if maxEnd < 0 {
		maxEnd = 0
	}
	// 引擎侧 computeBeats 会按显式素材动态放宽 beats，这里取相同上界。
	// 统一经 mapsFromAny 归一化：HTTP/Go 调用方构造的 notes/clips 为
	// []map[string]any，直接断言 .([]any) 恒失败——此前时长放宽分支被
	// 静默跳过，长工程按固定值估算可能误杀渲染中的会话
	for _, m := range mapsFromAny(params["notes"]) {
		if e, ok := anyFloat(m["end"]); ok && e > maxEnd {
			maxEnd = e
		}
	}
	for _, m := range mapsFromAny(params["clips"]) {
		sv, _ := anyFloat(m["start"])
		lv, _ := anyFloat(m["length"])
		if e := sv + lv; e > maxEnd {
			maxEnd = e
		}
	}
	for _, t := range mapsFromAny(params["tracks"]) {
		for _, c := range mapsFromAny(t["clips"]) {
			sv, _ := anyFloat(c["start"])
			lv, _ := anyFloat(c["length"])
			if e := sv + lv; e > maxEnd {
				maxEnd = e
			}
			for _, n := range mapsFromAny(c["notes"]) {
				ns, _ := anyFloat(n["start"])
				ne, ok := anyFloat(n["end"])
				if !ok {
					ne = ns + 1
				}
				if absEnd := sv + ne; absEnd > maxEnd {
					maxEnd = absEnd
				}
			}
		}
	}
	if maxEnd <= 0 {
		maxEnd = 16 // 与引擎缺省 beats=16 一致
	}
	tail, _ := anyFloat(params["tailSec"])
	if tail < 0 {
		tail = 0
	}
	audioSec := maxEnd*60/bpm + tail
	timeout := time.Duration(audioSec*2*float64(time.Second)) + 60*time.Second
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	// 上限须覆盖最坏情形（600s 音频 × 2 + 60s 余量 = 21min），此前 20min
	// 会把它截断——临界长工程可能在渲染将完成时被判超时杀掉会话
	if timeout > 25*time.Minute {
		timeout = 25 * time.Minute
	}
	return timeout
}

// GetLevels 获取电平。用 TryRequest：bounce 之类长请求占用事务锁时
// 立即返回错误（前端 50ms 轮询本就容忍丢帧），而不是把 UI 的电平/走带
// 轮询挂起最长 20 分钟（此前 Request 排队等锁的后果）
func (s *Supervisor) GetLevels() ([]float32, error) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return nil, fmt.Errorf("引擎未就绪")
	}
	resp, err := cli.TryRequest("getLevels", nil, s.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var res struct {
		Levels []float32 `json:"levels"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, err
	}
	return res.Levels, nil
}

// PanicAll 全音符停止（卡音逃生口，见 Client.Panic）。
// 用 TryRequest：panic 是紧急静音路径，bounce 等长请求在途时不得在
// 事务锁上无界排队（此前最长等 20 分钟才执行，逃生口形同虚设）；
// 引擎忙时跳过并返回错误，由调用方提示用户稍后再试。
func (s *Supervisor) PanicAll() error {
	cli, err := s.Ready(2 * time.Second)
	if err != nil {
		return err
	}
	resp, err := cli.TryRequest("panic", nil, s.cfg.RequestTimeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// SetLoop 设置循环
func (s *Supervisor) SetLoop(on bool, start, end float64) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	return cli.SetLoop(on, start, end, s.cfg.RequestTimeout)
}

// TransportPlay/TransportStop/TransportLocate/TransportSetTempo 走带控制（M3 阶段一）
func (s *Supervisor) TransportPlay() error {
	return s.transportCall("play", nil)
}

func (s *Supervisor) TransportStop() error {
	return s.transportCall("stop", nil)
}

func (s *Supervisor) TransportLocate(beat float64) error {
	return s.transportCall("locate", map[string]any{"beat": beat})
}

func (s *Supervisor) TransportSetTempo(bpm float64) error {
	return s.transportCall("setTempo", map[string]any{"bpm": bpm})
}

func (s *Supervisor) transportCall(method string, params map[string]any) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	resp, err := cli.Request(method, params, s.cfg.RequestTimeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// Timecode 返回引擎走带位置：优先用推送帧的锁存值（随任意请求刷新），
// 无锁存时发一次 timecode 请求拉取。
func (s *Supervisor) Timecode() (*Timecode, error) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return nil, fmt.Errorf("引擎未就绪")
	}
	if tc, ok := cli.LatchedTimecode(); ok {
		return &tc, nil
	}
	resp, err := cli.Request("timecode", nil, s.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	var tc Timecode
	if err := json.Unmarshal(resp.Result, &tc); err != nil {
		return nil, err
	}
	return &tc, nil
}

// RequestRaw 透传任意方法调用（内部/调试用）
func (s *Supervisor) RequestRaw(method string, timeout time.Duration) (json.RawMessage, error) {
	cli, err := s.Ready(timeout)
	if err != nil {
		return nil, err
	}
	resp, err := cli.Request(method, nil, timeout)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// TryRequestRaw 透传任意方法调用，引擎忙（长请求在途）时立即失败。
// 供状态轮询等"可跳过"路径使用——此前 /api/audio/status 走 Request，
// bounce 期间每次轮询都在事务锁上无界排队（最长挂起 20 分钟）。
func (s *Supervisor) TryRequestRaw(method string, timeout time.Duration) (json.RawMessage, error) {
	cli, err := s.Ready(timeout)
	if err != nil {
		return nil, err
	}
	resp, err := cli.TryRequest(method, nil, timeout)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	return resp.Result, nil
}
