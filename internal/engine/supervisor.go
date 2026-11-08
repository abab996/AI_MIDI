package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
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
	"sync/atomic"
	"time"

	"melodymate/internal/plugins"
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
	lastChains        map[int][]map[string]any
	lastMasterChain   []map[string]any
	lastMasterGain    float64
	hasMasterGain     bool
	pluginQueue       sync.Mutex
	loadedPlugins     map[string]loadedPlugin
	activeProject     string
	projectGeneration int64
	sessionReady      bool           // 是否已建立过工程会话（区分"空 project 的首次会话"）
	trackIDs          []string       // 引擎轨索引 → 工程 trackId（与前端轨道顺序一致）
	trackIndex        map[string]int // trackId → 引擎轨索引（同一会话内等价于下标）
	/* 每轨"乐器槽"唯一事实来源（只记录 SF2 / 波形 / 插件乐器，不含 FX 槽）。
	   崩溃重放以它为准：后一次设置覆盖前一次，不会出现"SF2 与波形声部同时
	   留在快照里、按顺序重放导致音色回退"的问题（S-H3）。 */
	instrumentSlots map[int]instrumentSlot
	/* 崩溃隔离（P-H3）：重放期间正在装载的插件引用。若引擎在装载它时
	   崩溃，本次崩溃归因到该插件，下次重放跳过它而不是把整个引擎熔断。 */
	replayActive  bool
	replayCurrent string
	pluginSkip    map[string]bool // 已隔离（重放时跳过）的插件引用
	pluginCrashes map[string]int  // 插件导致的引擎崩溃次数（诊断）
	/* 引擎扫描期间消息线程被占满，其它 plugin.* / fx.* 请求会排队；
	   客户端等待超时会 markDead 并重启引擎（P-M6）。这里记录扫描状态，
	   让这些请求先有界等待而不是直接烧满超时。 */
	scanning atomic.Bool
	scanMu   sync.Mutex // 进程外扫描串行化（同时只拉起一个扫描子进程）
	/* P-L1：引擎 Event 帧缓存（参数变化推送）。序号单调递增，跨引擎重启
	   不清零；事件列表每次新会话清空，避免旧轨索引的事件被前端误用。 */
	pluginEvents []PluginParamEvent
	eventSeq     int64
	/* S-M1：SF2 大音色异步加载状态。sfLoading 同时作为"引擎忙"信号，
	   防止加载期间的其它请求超时把会话判死；Cancel 只停止等待/触发恢复，
	   tsf 解析本身无法中断。 */
	sfLoadMu  sync.Mutex
	sfLoads   map[int]*SoundFontLoadState
	sfLoadGen map[int]int64
	sfLoading atomic.Int32
	/* P5：插件二进制状态缓存（工程级）。引擎崩溃重启会把 FX 链重放回来，
	   但插件自己的参数/状态回到默认值——重放完成后用这份缓存把状态推回去，
	   用户才不会感知到"崩溃后音色变了"。默认取进程级单例（与 HTTP 层同一份）。 */
	pluginStates *plugins.Cache
	hasSoundfont bool // 当前会话是否已加载任何音色（/api/audio/status 透出）
	exePath      string
	token        string    // IPC 启动令牌：随进程生成，注入引擎环境并用于握手
	lastReadyAt  time.Time // 最近一次进入 StateReady 的时间（秒崩判定用）
	stopOnce     sync.Once
	doneCh       chan struct{} // 关闭表示主循环退出
	wakeCh       chan struct{} // StateDisabled 驻留时由 ApplySettings 启用引擎唤醒
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
	sup := &Supervisor{cfg: cfg, audio: audio, lastGood: audio, lastSoundFonts: make(map[int]string), wakeCh: make(chan struct{}, 1),
		trackIndex:      map[string]int{},
		instrumentSlots: map[int]instrumentSlot{},
		pluginSkip:      map[string]bool{},
		pluginCrashes:   map[string]int{},
		sfLoads:         map[int]*SoundFontLoadState{},
		sfLoadGen:       map[int]int64{},
		// 插件状态缓存与 HTTP 层共用进程级单例：保存/恢复与崩溃重放必须看到同一份
		pluginStates: plugins.Default()}
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
// 显式覆盖（仅当解析后位于主程序同级 bin/ 内）→ 主程序同级 bin/（生产布局）
// → 当前工作目录 bin/（go run 开发态）。
// 覆盖值做包含性校验（Abs+EvalSymlinks 归一后必须落在 exe 同级 bin/ 下）：
// settings.json 里的 audio.engine_path 不再能指向任意可执行文件（Low 加固
// 2026-11-01）；校验不过就忽略覆盖走默认候选，不报错（配置残留不致命）。
func ResolveEnginePath(override string) string {
	candidates := []string{}
	if override != "" {
		if cleaned, ok := containedEnginePath(override); ok {
			candidates = append(candidates, cleaned)
		} else {
			slog.Warn("[engine] audio.engine_path 不在允许的 bin 目录内，忽略覆盖", "override", override)
		}
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

// containedEnginePath 校验 path 归一化（绝对化 + 解符号链接）后是否位于
// 受信 bin 目录（主程序 exe 同级 bin/，或当前工作目录 bin/——go run 开发态
// 与测试的 exe 在临时目录，此时以 cwd/bin 为准）内；是则返回归一化路径。
func containedEnginePath(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	} else if _, statErr := os.Stat(abs); statErr != nil {
		// 目标不存在（或父级符号链接未就绪）：归一化到目录层级即可判断
		abs = filepath.Clean(abs)
	}
	var roots []string
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Join(filepath.Dir(exe), "bin"))
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, filepath.Join(cwd, "bin"))
	}
	for _, root := range roots {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return abs, true
	}
	return "", false
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
	// 切片深拷贝：状态快照会离开锁在 HTTP 层序列化，后台重放可能同时 append
	st.ReplayErrors = append([]string(nil), s.status.ReplayErrors...)
	st.PluginWarnings = append([]string(nil), s.status.PluginWarnings...)
	st.SoundfontLoaded = s.hasSoundfont
	st.IsolatedPlugins = s.isolatedPluginsLocked()
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

// consumeClientEvents 持续消费引擎 Event 帧（P-L1）。会话关闭即退出。
func (s *Supervisor) consumeClientEvents(cli *Client) {
	for {
		select {
		case <-cli.Done():
			return
		case ev := <-cli.Events():
			s.mu.Lock()
			s.eventSeq++
			s.pluginEvents = append(s.pluginEvents, PluginParamEvent{
				Seq: s.eventSeq, Event: ev.Name, Data: ev.Data,
			})
			// 有界：只保留最近 256 条，前端游标落后太多时自然只剩最新事件
			if len(s.pluginEvents) > 256 {
				trimmed := make([]PluginParamEvent, 256)
				copy(trimmed, s.pluginEvents[len(s.pluginEvents)-256:])
				s.pluginEvents = trimmed
			}
			s.mu.Unlock()
		}
	}
}

// PluginEvents 返回 after 之后的参数事件与当前游标（P-L1）。
func (s *Supervisor) PluginEvents(after int64) (int64, []PluginParamEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PluginParamEvent, 0, 8)
	for _, ev := range s.pluginEvents {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return s.eventSeq, out
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
	s.hasMasterGain = true
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

// ScanPlugins 进程外扫描插件目录（P-M6 彻底方案）。
//
// 插件实例化只在独立的 melodymate-engine.exe --scan-only 子进程中发生：
//
//	· 扫描崩溃/挂死只影响子进程，正在播放的引擎进程不受影响；
//	· 扫描期间引擎消息线程完全空闲，plugin/fx 请求不会排队超时；
//	· 扫描成功后写 XML 缓存，并让运行中的引擎 plugin.loadCache 刷新清单，
//	  新安装的插件可立即装载。
func (s *Supervisor) ScanPlugins(dirs []string, timeout time.Duration) (*PluginList, error) {
	if err := validateScanDirs(dirs); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	exe := s.exePath
	if strings.TrimSpace(exe) == "" {
		return nil, fmt.Errorf("引擎可执行文件路径为空，无法启动扫描子进程")
	}

	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	outFile, err := os.CreateTemp("", "melodymate-scan-*.json")
	if err != nil {
		return nil, fmt.Errorf("创建扫描结果文件失败：%w", err)
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer os.Remove(outPath)

	cachePath := s.pluginCachePath()
	tempCache := false
	if cachePath == "" {
		cachePath = outPath + ".cache.xml"
		tempCache = true
	}
	if tempCache {
		defer os.Remove(cachePath)
	} else {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
	}

	args := buildScanArgs(outPath, cachePath, dirs)
	output, runErr := runScanProcess(exe, args, timeout)
	if runErr != nil {
		tail := strings.TrimSpace(output)
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		if errors.Is(runErr, errScanTimeout) {
			return nil, fmt.Errorf("插件扫描超时（%s），扫描子进程已终止", timeout)
		}
		return nil, fmt.Errorf("扫描子进程异常退出：%v（%s）", runErr, tail)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("读取扫描结果失败：%w", err)
	}
	list, err := parseScanOutput(raw)
	if err != nil {
		return nil, err
	}

	/* 让运行中的引擎刷新已知清单：扫描在子进程完成，live 引擎原本仍持旧
	   清单，不刷新会导致"扫描到了但装不上"。引擎未启动时跳过，下次启动
	   由 runOnce 载入同一份缓存。 */
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli != nil {
		if resp, reqErr := cli.Request("plugin.loadCache", map[string]any{"path": cachePath}, 30*time.Second); reqErr != nil {
			slog.Warn("[engine] 扫描完成，但让运行中引擎刷新插件清单失败", "err", reqErr)
		} else if err := resp.Err(); err != nil {
			slog.Warn("[engine] 扫描完成，但引擎拒绝加载插件清单缓存", "err", err)
		} else {
			slog.Info("[engine] 插件扫描完成，运行中引擎清单已刷新", "count", list.Count)
		}
	}
	return list, nil
}

// buildScanArgs 组装扫描子进程参数（纯函数，便于单测）。
func buildScanArgs(outPath, cachePath string, dirs []string) []string {
	args := []string{"--scan-only", "--out", outPath, "--cache", cachePath}
	if len(dirs) > 0 {
		args = append(args, "--dirs", strings.Join(dirs, ";"))
	}
	return args
}

// parseScanOutput 解析扫描子进程写出的 JSON 清单（纯函数，便于单测）。
func parseScanOutput(raw []byte) (*PluginList, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("扫描结果为空")
	}
	var list PluginList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("扫描结果解析失败：%w", err)
	}
	if list.Count == 0 {
		list.Count = len(list.Plugins)
	}
	return &list, nil
}

// LoadPluginCache 载入上次的扫描缓存（启动时调用，避免每次重扫）
func (s *Supervisor) LoadPluginCache() (int, error) {
	cli, err := s.Ready(3 * time.Second)
	if err != nil {
		return 0, err
	}
	return s.loadPluginCacheWithClient(cli)
}

func (s *Supervisor) loadPluginCacheWithClient(cli *Client) (int, error) {
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
	return s.loadPlugin(track, slot, pluginID, true)
}

// LoadPluginEphemeral 装载"试听"插件：不记入崩溃重放表（P-L6）。
// 试听轨（29）不是工程轨道，引擎重启后不该被重放成工程音源。
func (s *Supervisor) LoadPluginEphemeral(track, slot int, pluginID string) error {
	return s.loadPlugin(track, slot, pluginID, false)
}

func (s *Supervisor) loadPlugin(track, slot int, pluginID string, remember bool) error {
	if strings.TrimSpace(pluginID) == "" {
		return fmt.Errorf("缺少插件 id")
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	// 插件实例化要加载 DLL，给足时间（大插件可能数秒）
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	return s.loadPluginLocked(cli, track, slot, pluginID, remember)
}

// LoadPluginAt 在插件队列内把 trackId 解析为引擎索引后装载（P-M5）。
// 解析与执行之间不会插入 /session 的映射重排，避免状态/实例串号。
// 返回实际使用的引擎轨索引（调用方用它登记引用等元数据）。
func (s *Supervisor) LoadPluginAt(project, trackID string, generation int64, master bool, slot int, pluginID string, ephemeral bool) (int, error) {
	if strings.TrimSpace(pluginID) == "" {
		return 0, fmt.Errorf("缺少插件 id")
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return 0, err
	}
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return 0, err
	}
	return track, s.loadPluginLocked(cli, track, slot, pluginID, !ephemeral)
}

func (s *Supervisor) loadPluginLocked(cli *Client, track, slot int, pluginID string, remember bool) error {
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
	resp, err := cli.Request("plugin.load", params, 60*time.Second)
	if err != nil {
		return err
	}
	if err := resp.Err(); err != nil {
		return err
	}
	s.clearPluginSkip(pluginID)
	if remember {
		s.rememberPlugin(track, slot, params, pluginID, "vst3")
	}
	return nil
}

// resolvePluginTrackLocked 解析 trackId → 引擎轨索引。
// 调用方必须已持有 pluginQueue（与 BeginPluginSession 的重排互斥）。
func (s *Supervisor) resolvePluginTrackLocked(project, trackID string, generation int64, master bool) (int, error) {
	if master {
		trackID = "$master"
	}
	return s.ResolvePluginTrack(project, trackID, generation)
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
	return s.loadMmPlugin(track, slot, dllPath, true)
}

// LoadMmPluginEphemeral 试听用 MM 乐器：不记入崩溃重放表（P-L6）。
func (s *Supervisor) LoadMmPluginEphemeral(track, slot int, dllPath string) (map[string]any, error) {
	return s.loadMmPlugin(track, slot, dllPath, false)
}

func (s *Supervisor) loadMmPlugin(track, slot int, dllPath string, remember bool) (map[string]any, error) {
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
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	return s.loadMmPluginLocked(cli, track, slot, dllPath, remember)
}

// LoadMmPluginAt 在插件队列内解析 trackId 后装载（P-M5）。
func (s *Supervisor) LoadMmPluginAt(project, trackID string, generation int64, master bool, slot int, dllPath string, ephemeral bool) (int, map[string]any, error) {
	dllPath = strings.TrimSpace(dllPath)
	if dllPath == "" {
		return 0, nil, fmt.Errorf("缺少 MM 插件库路径")
	}
	if err := validateMmPluginPath(s.cfg.PluginsDir, dllPath); err != nil {
		return 0, nil, err
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return 0, nil, err
	}
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return 0, nil, err
	}
	out, err := s.loadMmPluginLocked(cli, track, slot, dllPath, !ephemeral)
	return track, out, err
}

func (s *Supervisor) loadMmPluginLocked(cli *Client, track, slot int, dllPath string, remember bool) (map[string]any, error) {
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
	s.clearPluginSkip(dllPath)
	if remember {
		s.rememberPlugin(track, slot, params, dllPath, "mm")
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
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	return s.unloadPluginLocked(cli, track, slot)
}

// UnloadPluginAt 在插件队列内解析 trackId 后卸载（P-M5）。
func (s *Supervisor) UnloadPluginAt(project, trackID string, generation int64, master bool, slot int) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return err
	}
	return s.unloadPluginLocked(cli, track, slot)
}

func (s *Supervisor) unloadPluginLocked(cli *Client, track, slot int) error {
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
	if err := resp.Err(); err != nil {
		return err
	}
	s.forgetPlugin(track, slot)
	return nil
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

// ── P-M5：解析与执行在插件队列内原子完成的变体 ──
func (s *Supervisor) PluginParamsAt(project, trackID string, generation int64, master bool, slot int) (map[string]any, error) {
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return nil, err
	}
	return s.PluginParams(track, slot)
}

func (s *Supervisor) SetPluginParamAt(project, trackID string, generation int64, master bool, slot, index int, value float64) error {
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return err
	}
	return s.SetPluginParam(track, slot, index, value)
}

func (s *Supervisor) OpenPluginEditorAt(project, trackID string, generation int64, master bool, slot int) error {
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return err
	}
	return s.OpenPluginEditor(track, slot)
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

// SetPluginStateCache 注入插件状态缓存（装配层创建；nil 表示该能力降级）。
func (s *Supervisor) SetPluginStateCache(c *plugins.Cache) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pluginStates = c
}

// PluginGetState 导出插件二进制状态（base64 文本）。
// track=-1 表示主总线 FX 槽；slot=-1 表示该轨的乐器。
func (s *Supervisor) PluginGetState(track, slot int) (string, error) {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return "", err
	}
	resp, err := cli.Request("plugin.getState", pluginTarget(track, slot), 60*time.Second)
	if err != nil {
		return "", err
	}
	if err := resp.Err(); err != nil {
		return "", err
	}
	var out struct {
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return "", err
	}
	if out.Data == "" {
		return "", fmt.Errorf("引擎未返回插件状态")
	}
	return out.Data, nil
}

// PluginSetState 把 base64 状态写回插件。
func (s *Supervisor) PluginSetState(track, slot int, data string) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	params := pluginTarget(track, slot)
	params["data"] = data
	resp, err := cli.Request("plugin.setState", params, 60*time.Second)
	if err != nil {
		return err
	}
	return resp.Err()
}

// PluginGetStateAt / PluginSetStateAt 在插件队列内解析 trackId（P-M5）。
// 全量重载期间映射可能重排：先前解析出的索引会指向另一个插件，把状态
// 读/写到错误实例上（状态串号）。
func (s *Supervisor) PluginGetStateAt(project, trackID string, generation int64, master bool, slot int) (string, int, error) {
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return "", 0, err
	}
	blob, err := s.PluginGetState(track, slot)
	return blob, track, err
}

func (s *Supervisor) PluginSetStateAt(project, trackID string, generation int64, master bool, slot int, data string) (int, error) {
	s.pluginQueue.Lock()
	defer s.pluginQueue.Unlock()
	track, err := s.resolvePluginTrackLocked(project, trackID, generation, master)
	if err != nil {
		return 0, err
	}
	return track, s.PluginSetState(track, slot, data)
}

// restorePluginStates 引擎重启重放完成后，把当前工程的插件状态推回去。
// 尽力而为：单个插件恢复失败只记日志（重放时插件可能因崩溃被跳过），
// 绝不让它把会话判成失败——否则一次坏状态会引发无限重启循环。
func (s *Supervisor) restorePluginStates(cli *Client) {
	s.mu.RLock()
	cache := s.pluginStates
	s.mu.RUnlock()
	if cache == nil {
		return
	}
	states := cache.ActiveStates()
	if len(states) == 0 {
		return
	}
	restored, failed := 0, 0
	for _, st := range states {
		s.mu.RLock()
		track := st.Target.Track
		if st.Target.TrackID != "" {
			track = -2
			for index, id := range s.trackIDs {
				if id == st.Target.TrackID {
					track = index
					break
				}
			}
		}
		p, ok := s.loadedPlugins[pluginKey(track, st.Target.Slot)]
		matching := ok && p.Ref == st.Ref && p.Format == st.Format
		s.mu.RUnlock()
		if !matching {
			/* P-L4：引用不匹配此前只加 failed 计数，用户看不到"状态没恢复"。
			   改成单插件告警（不影响引擎就绪）。 */
			s.recordPluginWarning(st.Ref, fmt.Errorf("插件状态引用与当前实例不匹配，已跳过恢复"))
			failed++
			continue
		}
		params := pluginTarget(track, st.Target.Slot)
		params["data"] = base64.StdEncoding.EncodeToString(st.Payload)
		resp, err := cli.Request("plugin.setState", params, 60*time.Second)
		if err == nil {
			err = resp.Err()
		}
		if err != nil {
			failed++
			s.recordReplayError(st.Ref, err)
			slog.Debug("[engine] 恢复插件状态失败（槽位可能为空）",
				"track", st.Target.Track, "slot", st.Target.Slot, "err", err)
			continue
		}
		restored++
	}
	slog.Info("[engine] 引擎重启后恢复插件状态", "restored", restored, "failed", failed)
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
	// 波形声部与 SF2 互斥：写入波形即撤下该轨的 SF2/预设快照，
	// 否则崩溃重放会把两者都装回来（S-H3）
	delete(s.lastSoundFonts, track)
	delete(s.lastPresets, track)
	if track == 0 {
		s.lastSoundFont = ""
	}
	s.setInstrumentSlotLocked(track, instrumentSlot{Kind: "wave", Voice: voice})
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
	path := s.lastSoundFonts[track]
	if path == "" && track == 0 {
		path = s.lastSoundFont
	}
	s.setInstrumentSlotLocked(track, instrumentSlot{Kind: "sf2", Path: path, Preset: [2]int{bank, program}, HasPre: true})
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

		// P-H3：本次崩溃若发生在重放某个插件期间，归因并隔离该插件。
		// 隔离后不再计入"引擎连续失败"（坏插件不该把整个引擎熔断停用）。
		culprit := s.attributeReplayCrash()
		if culprit != "" {
			consecutiveFail = 0
			s.setState(StateRestarting,
				fmt.Sprintf("插件 %s 导致引擎崩溃，已隔离该插件并重启引擎", culprit))
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
	s.mu.Lock()
	s.status.ReplayErrors = nil
	s.mu.Unlock()
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

	/* 扫描期间消息线程被占满：让客户端把"超时"与"会话已死"区分开，
	   避免扫描几十秒就把引擎判死重启（P-M6）。须在发布给其他 goroutine
	   之前设置。 */
	cli.SetBusyCheck(func() bool { return s.scanning.Load() || s.sfLoading.Load() > 0 })

	s.mu.Lock()
	s.client = cli
	s.sessionCnt++
	if s.sessionCnt > 1 {
		s.status.Restarts = s.sessionCnt - 1
	}
	/* 新会话：清空旧事件（序号保持单调，前端 after 游标不会回退） */
	s.pluginEvents = nil
	s.mu.Unlock()

	/* P-L1：消费引擎 Event 帧（插件参数变化），缓存给 HTTP 增量拉取 */
	go s.consumeClientEvents(cli)

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

	/* 会话建立后重放乐器槽（S-H3 / P-H4）：每轨只有一条快照，
	   同一轨的 SF2 / 波形 / 插件乐器不会重复出现。旧实现分三张表、
	   按固定顺序重放，后写覆盖前写，换过音源的轨道恢复出错误音色。
	   没有任何快照时（首次启动）回退加载音色目录首个 SF2 到 track 0。 */
	s.mu.RLock()
	slots := make(map[int]instrumentSlot, len(s.instrumentSlots))
	for tr, sl := range s.instrumentSlots {
		cp := sl
		if sl.Voice != nil {
			cp.Voice = make(map[string]any, len(sl.Voice))
			for k, v := range sl.Voice {
				cp.Voice[k] = v
			}
		}
		slots[tr] = cp
	}
	hasLegacyFont := s.lastSoundFont != "" || len(s.lastSoundFonts) > 0
	s.mu.RUnlock()
	if failReason == "" {
		s.hasSoundfont = false
		if len(slots) == 0 && !hasLegacyFont {
			if def := s.defaultSoundFontPath(); def != "" {
				slots[0] = instrumentSlot{Kind: "sf2", Path: def}
			} else {
				slog.Info("[engine] 未找到默认音色（Library/soundfonts/*.sf2），SF2 轨将走逐轨回退或保持静默")
			}
		}
		for tr, sl := range slots {
			if failReason != "" {
				break
			}
			switch sl.Kind {
			case "sf2":
				if sl.Path == "" {
					continue
				}
				if err := s.loadSoundFontWithTrack(cli, tr, sl.Path); err != nil {
					select {
					case <-cli.Done():
						failReason = "冷启动重放失败: IPC 会话失效"
					default:
						slog.Warn("[engine] 加载音色失败（track)", "track", tr, "path", sl.Path, "err", err)
					}
					continue
				}
				slog.Info("[engine] 已加载音色", "track", tr, "path", sl.Path)
				if sl.HasPre {
					if resp, err := cli.Request("setTrackPreset", map[string]any{
						"track": tr, "bank": sl.Preset[0], "program": sl.Preset[1],
					}, 10*time.Second); err != nil || resp.Err() != nil {
						if err == nil {
							err = resp.Err()
						}
						select {
						case <-cli.Done():
							failReason = "冷启动重放失败: IPC 会话失效"
						default:
							slog.Warn("[engine] 重放预设选择失败（track)", "track", tr, "err", err)
						}
					}
				}
				s.mu.Lock()
				s.hasSoundfont = true
				s.mu.Unlock()
			case "wave":
				if len(sl.Voice) == 0 {
					continue
				}
				params := make(map[string]any, len(sl.Voice)+1)
				for k, v := range sl.Voice {
					params[k] = v
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
			}
		}
	}

	/* P6：启动即载入插件清单缓存（存在时）。
	   价值有两层：① 用户不必每次启动重扫插件目录（扫描要逐个加载插件，几十秒起）；
	   ② 扫描会"加载再卸载"每个插件，某些插件在随后被真正实例化时会崩——
	   走缓存路径完全不碰插件二进制，能规避这一类问题。
	   缓存缺失/损坏时静默跳过（回落到显式扫描）。 */
	if failReason == "" {
		if n, err := s.loadPluginCacheWithClient(cli); err != nil {
			slog.Debug("[engine] 无插件清单缓存（首次启动属正常）", "err", err)
		} else {
			slog.Info("[engine] 插件清单缓存已载入", "count", n)
		}
	}
	s.mu.Lock()
	s.replayActive = true
	s.replayCurrent = ""
	instances := make([]loadedPlugin, 0, len(s.loadedPlugins))
	for _, p := range s.loadedPlugins {
		instances = append(instances, p)
	}
	s.mu.Unlock()
	sort.Slice(instances, func(i, j int) bool { return instances[i].Ref < instances[j].Ref })
	for _, p := range instances {
		if failReason != "" {
			break
		}
		s.mu.RLock()
		skip := s.pluginSkip[p.Ref]
		s.mu.RUnlock()
		if skip {
			/* P-H3：已归因的崩溃插件不再重放。否则它每轮都把引擎带崩，
			   连续 10 次后整个引擎被永久停用（用户失去全部音频）。 */
			s.recordPluginWarning(p.Ref, fmt.Errorf("该插件此前导致引擎崩溃，已隔离；在混音台重新装载可重试"))
			continue
		}
		s.mu.Lock()
		s.replayCurrent = p.Ref
		s.mu.Unlock()
		resp, err := cli.Request("plugin.load", p.Params, 60*time.Second)
		if err == nil {
			err = resp.Err()
		}
		s.mu.Lock()
		if s.replayCurrent == p.Ref {
			s.replayCurrent = ""
		}
		s.mu.Unlock()
		if err != nil {
			/* 单个插件装载失败是"单插件告警"，不是全局失败：
			   其余插件照常装载、播放不受影响（P-H3 的可见错误部分）。 */
			s.recordPluginWarning(p.Ref, err)
			slog.Warn("[engine] 插件实例重放失败", "ref", p.Ref, "err", err)
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
	masterGain, hasMasterGain := s.lastMasterGain, s.hasMasterGain
	s.mu.RUnlock()
	if failReason == "" {
		for tr, slots := range chains {
			if resp, err := cli.Request("fx.setChain", map[string]any{
				"track": tr, "slots": slots,
			}, 10*time.Second); err != nil || resp.Err() != nil {
				if err == nil {
					err = resp.Err()
				}
				s.recordReplayError("fx.setChain", err)
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
		if resp, err := cli.Request("fx.setChain", map[string]any{
			"master": true, "slots": masterChain,
		}, 10*time.Second); err != nil || resp.Err() != nil {
			if err == nil {
				err = resp.Err()
			}
			s.recordReplayError("fx.setChain", err)
			select {
			case <-cli.Done():
				failReason = "冷启动重放失败: IPC 会话失效"
			default:
				slog.Warn("[engine] 重放主总线 FX 链失败", "err", err)
			}
		}
	}
	if failReason == "" && hasMasterGain {
		if resp, err := cli.Request("mixer.setMaster", map[string]any{
			"gain": masterGain,
		}, 10*time.Second); err != nil || resp.Err() != nil {
			if err == nil {
				err = resp.Err()
			}
			s.recordReplayError("mixer.setMaster", err)
			select {
			case <-cli.Done():
				failReason = "冷启动重放失败: IPC 会话失效"
			default:
				slog.Warn("[engine] 重放主总线增益失败", "err", err)
			}
		}
	}

	/* P5：FX 链重放只把插件装回来，插件自身的参数/状态是默认值。
	   这里把当前工程的插件状态推回去（尽力而为，失败不影响会话）。 */
	if failReason == "" {
		s.restorePluginStates(cli)
	}
	s.mu.Lock()
	s.replayActive = false
	s.replayCurrent = ""
	if failReason == "" {
		/* 就绪时刻在重放完成之后才写：重放阶段崩溃属于"装机即崩"，
		   必须计入秒崩判定，否则坏插件会被无限重放（P-H3）。 */
		s.lastReadyAt = time.Now()
	}
	s.mu.Unlock()
	if failReason == "" {
		s.setState(StateReady, "")
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
						/* 扫描/大音色加载都是合法长任务（几十秒到数分钟），
						   期间消息线程无法应答心跳。若照常累计，会被心跳判死
						   并重启（P-M6/S-M1）。进程真崩溃仍由 waitCh 秒级发现。 */
						if s.scanning.Load() || s.sfLoading.Load() > 0 {
							failures = 0
							continue
						}
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
	s.recordLoadedSoundFont(track, path)
	return nil
}

// recordLoadedSoundFont 把一次成功加载记入崩溃重放快照（S-M1 异步路径共用）。
func (s *Supervisor) recordLoadedSoundFont(track int, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSoundFonts == nil {
		s.lastSoundFonts = make(map[int]string)
	}
	s.lastSoundFonts[track] = path
	s.hasSoundfont = true
	if track == 0 {
		s.lastSoundFont = path
	}
	// SF2 与波形声部互斥：装音色即撤下该轨的波形/插件快照
	delete(s.lastVoices, track)
	if cur, ok := s.instrumentSlots[track]; !ok || cur.Kind != "sf2" || cur.Path != path {
		delete(s.lastPresets, track)
	}
	s.setInstrumentSlotLocked(track, instrumentSlot{Kind: "sf2", Path: path})
}

// StartSoundFontLoad 异步加载 SF2 大音色（S-M1）。
// 立即返回，加载在后台 goroutine 进行；进度/取消状态经 SoundFontLoadStatus
// 查询。引擎侧 tsf 解析本身不可中断，取消只停止等待并在完成后恢复原音色。
func (s *Supervisor) StartSoundFontLoad(track int, path string) error {
	if track < 0 || track >= 32 {
		return fmt.Errorf("track 超出范围（0-31）: %d", track)
	}
	if err := s.validateSoundFontPath(path); err != nil {
		return err
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return fmt.Errorf("音色文件不可读：%w", statErr)
	}
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}

	s.sfLoadMu.Lock()
	if s.sfLoads == nil {
		s.sfLoads = map[int]*SoundFontLoadState{}
	}
	if s.sfLoadGen == nil {
		s.sfLoadGen = map[int]int64{}
	}
	if cur := s.sfLoads[track]; cur != nil && cur.Running {
		s.sfLoadMu.Unlock()
		return fmt.Errorf("该轨道正在加载音色，请稍候或先取消")
	}
	s.sfLoadGen[track]++
	token := s.sfLoadGen[track]
	s.sfLoads[track] = &SoundFontLoadState{
		Track: track, Path: path, Size: info.Size(),
		Running: true, Phase: "loading", StartedAt: time.Now(),
	}
	s.sfLoadMu.Unlock()
	s.sfLoading.Add(1)

	s.mu.RLock()
	previous := s.lastSoundFonts[track]
	if previous == "" && track == 0 {
		previous = s.lastSoundFont
	}
	s.mu.RUnlock()

	go func() {
		loadErr := s.loadSoundFontWithTrackTimeout(cli, track, path, 10*time.Minute)
		s.finishSoundFontLoad(track, token, path, previous, loadErr)
	}()
	return nil
}

// finishSoundFontLoad 结算一次异步加载：成功/失败/取消（含恢复原音色）。
func (s *Supervisor) finishSoundFontLoad(track int, token int64, path, previous string, loadErr error) {
	s.sfLoadMu.Lock()
	if s.sfLoadGen[track] != token {
		s.sfLoadMu.Unlock()
		s.sfLoading.Add(-1)
		return // 已被更新的请求取代
	}
	state := s.sfLoads[track]
	if state == nil {
		s.sfLoadMu.Unlock()
		s.sfLoading.Add(-1)
		return
	}
	cancelled := state.Cancelling
	if cancelled && loadErr == nil {
		state.Phase = "restoring"
	}
	s.sfLoadMu.Unlock()

	phase := "done"
	errText := ""
	if loadErr != nil {
		phase = "failed"
		errText = loadErr.Error()
	} else if cancelled {
		if previous != "" && previous != path {
			cli, rerr := s.Ready(5 * time.Second)
			if rerr == nil {
				rerr = s.loadSoundFontWithTrackTimeout(cli, track, previous, 10*time.Minute)
			}
			if rerr != nil {
				/* 恢复失败：引擎里已经是新音色。快照跟随引擎实际状态，
				   避免下次重放与当前声音不一致。 */
				s.recordLoadedSoundFont(track, path)
				phase = "failed"
				errText = "已取消，但恢复原音色失败：" + rerr.Error()
			} else {
				phase = "cancelled"
				errText = "已取消加载，已恢复原音色"
			}
		} else {
			/* 没有可恢复的旧音色：引擎已完成加载，快照跟随引擎实际。 */
			s.recordLoadedSoundFont(track, path)
			phase = "cancelled"
			errText = "已取消等待（引擎已完成加载）"
		}
	} else {
		s.recordLoadedSoundFont(track, path)
	}

	s.sfLoadMu.Lock()
	if s.sfLoadGen[track] == token {
		if st := s.sfLoads[track]; st != nil {
			st.Running = false
			st.Cancelling = false
			st.Phase = phase
			st.Error = errText
			st.FinishedAt = time.Now()
		}
	}
	s.sfLoadMu.Unlock()
	s.sfLoading.Add(-1)
}

// CancelSoundFontLoad 取消等待中的加载（引擎解析无法中断）。
func (s *Supervisor) CancelSoundFontLoad(track int) error {
	s.sfLoadMu.Lock()
	defer s.sfLoadMu.Unlock()
	st := s.sfLoads[track]
	if st == nil || !st.Running {
		return fmt.Errorf("该轨道没有正在加载的音色")
	}
	st.Cancelling = true
	st.Phase = "cancelling"
	return nil
}

// SoundFontLoadStatus 查询某轨的音色加载状态（S-M1）。
func (s *Supervisor) SoundFontLoadStatus(track int) SoundFontLoadState {
	s.sfLoadMu.Lock()
	defer s.sfLoadMu.Unlock()
	st := s.sfLoads[track]
	if st == nil {
		return SoundFontLoadState{Track: track, Phase: "idle"}
	}
	cp := *st
	if cp.Running && !cp.StartedAt.IsZero() {
		cp.ElapsedMS = time.Since(cp.StartedAt).Milliseconds()
	} else if !cp.StartedAt.IsZero() && !cp.FinishedAt.IsZero() {
		cp.ElapsedMS = cp.FinishedAt.Sub(cp.StartedAt).Milliseconds()
	}
	return cp
}

// loadSoundFontWith 在指定会话上加载音色并解析结果（track 0）
func (s *Supervisor) loadSoundFontWith(cli *Client, path string) error {
	return s.loadSoundFontWithTrack(cli, 0, path)
}

// loadSoundFontWithTrack 指定轨道
func (s *Supervisor) loadSoundFontWithTrack(cli *Client, track int, path string) error {
	return s.loadSoundFontWithTrackTimeout(cli, track, path, 30*time.Second)
}

// loadSoundFontWithTrackTimeout 指定轨道与超时。大音色解析可达数分钟，
// 异步加载路径用长超时，避免客户端把健康会话判死（S-M1）。
func (s *Supervisor) loadSoundFontWithTrackTimeout(cli *Client, track int, path string, timeout time.Duration) error {
	resp, err := cli.Request("loadSoundFont", map[string]any{"path": path, "track": track}, timeout)
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

// defaultSoundFontPath 返回"内置音色"路径：优先 GeneralUser GS（安装包随附的
// 标准 GM 音色库），否则取目录中按文件名排序的首个 SF2；无则空串。
//
// 修复 S-M4：旧实现无条件取排序第一个，用户导入 "Arachno.sf2"（排在
// GeneralUser 之前）会静默顶替所有工程的"内置钢琴/弦乐"。
func (s *Supervisor) defaultSoundFontPath() string {
	if s.cfg.SoundFontDir == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(s.cfg.SoundFontDir, "*.sf2"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	for _, candidate := range matches {
		if strings.Contains(strings.ToLower(filepath.Base(candidate)), "generaluser") {
			return candidate
		}
	}
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

func (s *Supervisor) recordReplayError(target string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	message := target + ": " + err.Error()
	s.status.ReplayErrors = append(s.status.ReplayErrors, message)
	s.status.LastError = strings.Join(s.status.ReplayErrors, "; ")
}

// recordPluginWarning 记录单插件告警：不影响引擎就绪与其余轨道播放。
func (s *Supervisor) recordPluginWarning(target string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordPluginWarningLocked(target, err)
}

func (s *Supervisor) recordPluginWarningLocked(target string, err error) {
	message := target + ": " + err.Error()
	for _, existing := range s.status.PluginWarnings {
		if existing == message {
			return
		}
	}
	// 有界：坏插件每次重放都会产生一条，避免状态快照无限增长
	if len(s.status.PluginWarnings) >= 20 {
		s.status.PluginWarnings = s.status.PluginWarnings[len(s.status.PluginWarnings)-19:]
	}
	s.status.PluginWarnings = append(s.status.PluginWarnings, message)
}

// attributeReplayCrash 读取并清空"重放期间正在装载的插件"。
// 返回非空表示本次崩溃归因到该插件（调用方据此跳过它并重置熔断计数）。
func (s *Supervisor) attributeReplayCrash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := ""
	if s.replayActive && s.replayCurrent != "" {
		ref = s.replayCurrent
		if s.pluginCrashes == nil {
			s.pluginCrashes = map[string]int{}
		}
		if s.pluginSkip == nil {
			s.pluginSkip = map[string]bool{}
		}
		s.pluginCrashes[ref]++
		s.pluginSkip[ref] = true
		s.recordPluginWarningLocked(ref, fmt.Errorf("装载该插件时引擎崩溃（第 %d 次），已隔离", s.pluginCrashes[ref]))
		slog.Error("[engine] 插件导致引擎崩溃，已隔离", "ref", ref, "crashes", s.pluginCrashes[ref])
	}
	s.replayActive = false
	s.replayCurrent = ""
	return ref
}

// clearPluginSkip 用户主动（重新）装载插件时解除隔离：允许再试一次。
func (s *Supervisor) clearPluginSkip(ref string) {
	s.mu.Lock()
	if s.pluginSkip != nil {
		delete(s.pluginSkip, ref)
	}
	s.mu.Unlock()
}

// isolatedPlugins 当前被隔离的插件列表（状态接口透出）。
func (s *Supervisor) isolatedPlugins() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isolatedPluginsLocked()
}

// isolatedPluginsLocked 调用方须持有 s.mu（读或写）。
func (s *Supervisor) isolatedPluginsLocked() []string {
	if len(s.pluginSkip) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.pluginSkip))
	for ref := range s.pluginSkip {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}
