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
	"sync"
	"time"
)

// Config 守护器配置
type Config struct {
	EnginePath       string // 引擎 exe 路径（已解析为绝对路径）
	DialTimeout      time.Duration
	HandshakeTimeout time.Duration
	RequestTimeout   time.Duration
	PingInterval     time.Duration // 心跳周期；0 = 默认 2s
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

	status     EngineStatus
	client     *Client
	cmd        *exec.Cmd
	startedEnabled bool // 当前守护会话是否以启用状态启动
	lastApply  *map[string]any // 最近一次 applySetup 参数（重启后重放）
	exePath    string
	stopOnce   sync.Once
	doneCh     chan struct{} // 关闭表示主循环退出
}

// NewSupervisor 创建守护器（不启动；调用 Start）
func NewSupervisor(cfg Config, audio AudioSettings) *Supervisor {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 5 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second // listDevices 首次枚举可能达数秒
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 2 * time.Second
	}
	sup := &Supervisor{cfg: cfg, audio: audio}
	sup.exePath = ResolveEnginePath(audio.EnginePath)
	sup.startedEnabled = audio.EngineEnabled
	return sup
}

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

	go s.loop()
}

// Stop 优雅停止引擎并结束守护循环（幂等）
func (s *Supervisor) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		cancel := s.cancel
		cli := s.client
		s.mu.Unlock()

		// 先尝试协议层优雅退出
		if cli != nil {
			_, _ = cli.Request(1*time.Second, "shutdown", nil)
			_ = cli.Close()
		}
		if cancel != nil {
			cancel()
		}
		// 等主循环收尾（其内部会 Kill 残留进程）
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
	if s.audio.EngineEnabled {
		st.Protocol = ProtocolVersion
	} else {
		st.State = StateDisabled
	}
	return st
}

// Ready 等待引擎就绪或 ctx/超时；返回可用的客户端引用
func (s *Supervisor) Ready(timeout time.Duration) (*Client, error) {
	deadline := time.After(timeout)
	for {
		s.mu.RLock()
		state, cli := s.status.State, s.client
		s.mu.RUnlock()
		if state == StateReady && cli != nil {
			return cli, nil
		}
		select {
		case <-deadline:
			s.mu.RLock()
			defer s.mu.RUnlock()
			return nil, fmt.Errorf("引擎未就绪（state=%s, last_error=%q）",
				s.status.State, s.status.LastError)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ApplySettings 更新音频设置；引擎就绪时立即下发 applySetup
func (s *Supervisor) ApplySettings(audio AudioSettings) error {
	s.mu.Lock()
	s.audio = audio
	s.mu.Unlock()

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
	_, err = cli.Request(s.cfg.RequestTimeout, "applySetup", params)
	return err
}

// TestTone 测试音开关
func (s *Supervisor) TestTone(on bool, freq float64) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	_, err = cli.Request(10*time.Second, "testTone", map[string]any{"on": on, "freq": freq})
	return err
}

// ListDevices 设备枚举透传
func (s *Supervisor) ListDevices() (*DeviceList, error) {
	cli, err := s.Ready(10 * time.Second)
	if err != nil {
		return nil, err
	}
	raw, err := cli.Request(s.cfg.RequestTimeout, "listDevices", nil)
	if err != nil {
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
	if err := json.Unmarshal(raw, &rawList); err != nil {
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

	if !s.audio.EngineEnabled {
		s.setState(StateDisabled, "")
		<-s.ctx.Done()
		return
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

		s.runOnce()
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
			fmt.Sprintf("第 %d 次重启", consecutiveFail))

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

// runOnce 单次完整会话：拉起进程 → 连接握手 → 心跳监控 → 会话失效返回
func (s *Supervisor) runOnce() {
	s.setState(StateStarting, "")

	cmd := exec.Command(s.exePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		s.setState(StateFailed, "启动失败: "+err.Error())
		// 引擎文件缺失属于部署问题，重试无意义：驻留失败态直至主程序退出或文件出现
		if os.IsNotExist(err) || isExecNotFound(err) {
			s.parkUntilDone()
			return
		}
		time.Sleep(time.Second)
		return
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
		s.setState(StateFailed, "连接失败: "+err.Error())
		_ = cmd.Process.Kill()
		<-waitCh
		return
	}

	s.mu.Lock()
	s.client = cli
	s.status.Restarts++
	s.mu.Unlock()
	s.setState(StateReady, "")

	// 重放最近的音频设置（崩溃恢复对上层透明）
	s.mu.RLock()
	lastApply := s.lastApply
	s.mu.RUnlock()
	if lastApply != nil && len(*lastApply) > 0 {
		if _, err := cli.Request(15*time.Second, "applySetup", *lastApply); err != nil {
			slog.Warn("[engine] 重放音频设置失败", "err", err)
		}
	}

	// 心跳 + 进程退出 + IPC 失联三通道监控
	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(s.cfg.PingInterval)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				// 慢请求排队时心跳会顺延；连续失败才判定失联
				if _, err := cli.Request(5*time.Second, "ping", nil); err != nil {
					failures++
					if failures >= 2 {
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

	var failReason string
	select {
	case <-s.ctx.Done():
		failReason = ""
	case werr := <-waitCh:
		failReason = fmt.Sprintf("引擎进程退出: %v", werr)
	case <-cli.Done():
		failReason = "IPC 会话失效"
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
}

// LoadSoundFont 加载音色文件到引擎
func (s *Supervisor) LoadSoundFont(path string) error {
	cli, err := s.Ready(5 * time.Second)
	if err != nil {
		return err
	}
	raw, err := cli.Request(30*time.Second, "loadSoundFont", map[string]any{"path": path})
	if err != nil {
		return err
	}
	var res struct {
		Loaded bool   `json:"loaded"`
		Error  string `json:"error"`
	}
	if jsonErr := json.Unmarshal(raw, &res); jsonErr != nil {
		return jsonErr
	}
	if !res.Loaded {
		return fmt.Errorf("引擎加载音色失败: %s", res.Error)
	}
	return nil
}

// NoteOn/NoteOff 演奏事件：尽力而为——引擎未就绪或队列满时静默丢弃，
// 不阻塞前端键盘路径（M2 骨架；后续可加发送合并）
func (s *Supervisor) NoteOn(channel, key, velocity int) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return
	}
	_, _ = cli.Request(2*time.Second, "noteOn", map[string]any{
		"channel": channel, "key": key, "velocity": velocity,
	})
}

func (s *Supervisor) NoteOff(channel, key int) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		return
	}
	_, _ = cli.Request(2*time.Second, "noteOff", map[string]any{
		"channel": channel, "key": key,
	})
}
