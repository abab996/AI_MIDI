package engine

import (
	"context"
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

// Config 守护器配置
type Config struct {
	EnginePath       string // 引擎 exe 路径（已解析为绝对路径）
	SoundFontDir     string // 默认音色目录（Library/soundfonts）；由 main 注入，避免 config↔engine 循环导入
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
	startedEnabled bool            // 当前守护会话是否以启用状态启动
	parked         bool            // 引擎文件缺失等不可重试错误：驻留失败态直至退出
	sessionCnt     int             // 已建立的会话数（Restarts = sessionCnt - 1）
	lastApply      *map[string]any // 最近一次 applySetup 参数（重启后重放）
	lastGood       AudioSettings   // 最近一次成功应用的音频设置（applySetup 超时后回退目标）
	lastSoundFont  string          // 兼容旧单轨（track 0）
	lastSoundFonts map[int]string  // 每轨独立 SF2（重启后重放）
	lastVoices     map[int]map[string]any // 每轨内置波形声部参数（重启后重放）
	hasSoundfont   bool            // 当前会话是否已加载任何音色（/api/audio/status 透出）
	exePath        string
	stopOnce       sync.Once
	doneCh         chan struct{} // 关闭表示主循环退出
	wakeCh         chan struct{} // StateDisabled 驻留时由 ApplySettings 启用引擎唤醒
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

// ResolveEnginePath 解析引擎可执行文件路径：
// 显式覆盖 → 主程序同级 bin/（生产布局）→ 当前工作目录 bin/（go run 开发态）。
func ResolveEnginePath(override string) string {
	candidates := []string{}
	if override != "" {
		candidates = append(candidates, override)
	}
	if exeDir, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exeDir), "bin", "aimidi-engine.exe"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "bin", "aimidi-engine.exe"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, filepath.Join("bin", "aimidi-engine.exe"))
	}
	slog.Warn("[engine] 未找到 aimidi-engine.exe，使用首选候选路径", "candidates", candidates)
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
		// 为 nil，直接跳过等待
		if s.doneCh == nil {
			return
		}
		select {
		case <-s.doneCh:
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

		select {
		case <-s.ctx.Done():
			s.setState(StateStopped, "")
			s.killProcess()
			return
		default:
		}

		consecutiveFail++
		s.setState(StateRestarting,
			fmt.Sprintf("第 %d 次重启: %s", consecutiveFail, reason))

		// 连续快速失败的退避：500ms 起，封顶 3s
		backoff := time.Duration(consecutiveFail) * 500 * time.Millisecond
		if backoff > 3*time.Second {
			backoff = 3 * time.Second
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

	cli, err := Dial(cmd.Process.Pid, s.cfg.DialTimeout, s.cfg.HandshakeTimeout)
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
				Backend:       s.audio.Backend,
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
	if timeout > 20*time.Minute {
		timeout = 20 * time.Minute
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
