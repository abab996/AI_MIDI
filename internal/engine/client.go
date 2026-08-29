package engine

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
)

// ErrSessionDead 会话已判死（进程退出/协议失败/已关闭），后续请求立即失败
var ErrSessionDead = errors.New("引擎会话已失效")

// Client 原生音频引擎管道客户端（串行事务模型）。
//
// I/O 模型约束：os.OpenFile 打开的命名管道是同步（非重叠）句柄，
// 不支持 deadline，且不允许读写并发执行。因此本客户端把「写请求 +
// 读到匹配响应」作为一次完整事务，用互斥锁串行化；单次读由看门狗
// goroutine 计时，超时上限取该请求的剩余总超时——**不得再设 5s 之类
// 的固定封顶**：ASIO Link Pro 等驱动打开耗时 5-20s，封顶会把正常的
// 慢设备操作误判为会话失效，引发引擎重启风暴。会话终结由 markDead +
// supervisor 重建负责。事务期间收到的事件帧转发到 Events 通道
// （带缓冲，满则丢弃——事件均为可刷新的状态类信息）。
type Client struct {
	conn     *os.File
	mu       sync.Mutex // 保护 Request 事务（写+读）
	writeMu  sync.Mutex // 保护实时 MIDI 写入，避免被长 Request 阻塞
	nextID   float64
	dead     chan struct{}
	deadOnce sync.Once

	eventsMu sync.Mutex
	events   chan Event

	tcMu   sync.Mutex
	lastTc Timecode // 最近一次收到的走带时间码（0x05 帧锁存）
	hasTc  bool     // 是否收到过至少一帧
}

// Dial 连接引擎管道并完成协议握手（hello + 版本校验）。
// 管道在引擎进程创建后数百毫秒内才出现，dialPipe 负责等待。
func Dial(pid int, dialTimeout, handshakeTimeout time.Duration) (*Client, error) {
	name := fmt.Sprintf(`\\.\pipe\AI_MIDI_ENGINE_%d`, pid)
	conn, err := dialPipe(name, dialTimeout)
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn:   conn,
		dead:   make(chan struct{}),
		events: make(chan Event, 16),
	}

	if err := c.handshake(handshakeTimeout); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return c, nil
}

// Close 关闭底层连接并标记会话终结
func (c *Client) Close() error {
	c.markDead()
	return c.conn.Close()
}

// Done 会话终结信号
func (c *Client) Done() <-chan struct{} { return c.dead }

func (c *Client) markDead() {
	c.deadOnce.Do(func() { close(c.dead) })
}

// Events 事件流通道
func (c *Client) Events() <-chan Event { return c.events }

// Request 发送 JSON 控制请求并等待匹配的响应。
// 返回的 error 仅为传输层错误；业务失败（ok=false）需调用方检查 resp.Err()。
func (c *Client) Request(method string, params map[string]any, timeout time.Duration) (*Response, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	// 已判死的会话立即失败：否则每个后续请求都要先排队拿锁、再烧满
	// 自身超时（冷启动重放场景会放大成 15+30×N 秒的卡死）
	select {
	case <-c.dead:
		return nil, ErrSessionDead
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	req := Request{
		ID:     c.nextRequestID(),
		Method: method,
		Params: params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	if err := c.writeFrameWithTimeout(MsgRequest, body, timeout); err != nil {
		c.markDead()
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}

	return c.awaitResponse(req.ID, timeout)
}

// TryRequest 尝试发送请求：会话已死或事务锁被在途长请求占用时立即失败，
// 不排队等待。供 Stop 等"不能被 bounce 之类长请求阻塞"的路径使用。
func (c *Client) TryRequest(method string, params map[string]any, timeout time.Duration) (*Response, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	select {
	case <-c.dead:
		return nil, ErrSessionDead
	default:
	}
	if !c.mu.TryLock() {
		return nil, fmt.Errorf("引擎忙（有在途请求），已跳过 %s", method)
	}
	defer c.mu.Unlock()

	req := Request{
		ID:     c.nextRequestID(),
		Method: method,
		Params: params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	if err := c.writeFrameWithTimeout(MsgRequest, body, timeout); err != nil {
		c.markDead()
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	return c.awaitResponse(req.ID, timeout)
}

// SendMidi 发送 3 字节 MIDI 二进制帧（尽力而为，不等待响应）。
// 线格式：payload = [类型0x04][status][data1][data2]（status 为完整 MIDI 状态字节）。
// 兼容旧单轨；新多轨请用 SendMidiTrack。
func (c *Client) SendMidi(status, data1, data2 byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrameWithTimeout(MsgMidi, []byte{status, data1, data2}, 2*time.Second)
}

// SendMidiTrack 发送带 track 的 MIDI 帧（每轨独立 tsf）。
// 线格式：payload = [类型0x04][track][status][data1][data2]
func (c *Client) SendMidiTrack(track int, status, data1, data2 byte) error {
	if track < 0 {
		track = 0
	}
	if track > 31 {
		track = 31
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrameWithTimeout(MsgMidi, []byte{byte(track), status, data1, data2}, 2*time.Second)
}

// Ping 心跳探测
func (c *Client) Ping(timeout time.Duration) error {
	resp, err := c.Request("ping", nil, timeout)
	if err != nil {
		return err
	}
	if !resp.OK {
		return resp.Err()
	}
	return nil
}

// TryPing 非阻塞心跳：若有长请求占用 mu 则跳过本次，避免被 Bounce 等长任务饿死
func (c *Client) TryPing(timeout time.Duration) (bool, error) {
	if !c.mu.TryLock() {
		return false, nil // 跳过
	}
	defer c.mu.Unlock()
	// 手动构造 ping 请求（复用 writeFrameWithTimeout / awaitResponse 逻辑，但已持锁）
	req := Request{ID: c.nextRequestID(), Method: "ping", Params: nil}
	body, err := json.Marshal(req)
	if err != nil {
		return true, err
	}
	if err := c.writeFrameWithTimeout(MsgRequest, body, timeout); err != nil {
		c.markDead()
		return true, err
	}
	resp, err := c.awaitResponse(req.ID, timeout)
	if err != nil {
		return true, err
	}
	if !resp.OK {
		return true, resp.Err()
	}
	return true, nil
}

// NoteOn 实时音符按下（二进制帧，track 0 兼容）
func (c *Client) NoteOn(channel, key, velocity int) error {
	return c.SendMidi(byte(0x90|(channel&0x0F)), byte(key&0x7F), byte(velocity&0x7F))
}

// NoteOff 实时音符释放（二进制帧，track 0 兼容）
func (c *Client) NoteOff(channel, key int) error {
	return c.SendMidi(byte(0x80|(channel&0x0F)), byte(key&0x7F), 0x00)
}

// NoteOnTrack 指定轨道的音符按下（每轨独立 SF2）
func (c *Client) NoteOnTrack(track, key, velocity int) error {
	return c.SendMidiTrack(track, byte(0x90), byte(key&0x7F), byte(velocity&0x7F))
}

// NoteOffTrack 指定轨道的音符释放
func (c *Client) NoteOffTrack(track, key int) error {
	return c.SendMidiTrack(track, byte(0x80), byte(key&0x7F), 0x00)
}

// ScheduleSamples 批量调度音频素材（每轨独立缓冲，尾音自然不截断）
// clips: [{track, path, start, length, offset, fadeIn, fadeOut, gain}]
func (c *Client) ScheduleSamples(clips []map[string]any, bpm float64, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	resp, err := c.Request("scheduleSamples", map[string]any{"clips": clips, "bpm": bpm}, timeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// ClearSamples 清空已调度素材
func (c *Client) ClearSamples(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	resp, err := c.Request("clearSamples", nil, timeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// ScheduleNotes 批量调度 MIDI（为离线 bounce 准备）
func (c *Client) ScheduleNotes(notes []map[string]any, bpm float64, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	resp, err := c.Request("scheduleNotes", map[string]any{"notes": notes, "bpm": bpm}, timeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// ClearNotes 清空 MIDI 调度
func (c *Client) ClearNotes(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	resp, err := c.Request("clearNotes", nil, timeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// Bounce 离线渲染至 WAV（按全局采样率，尾音到静默不截断）
func (c *Client) Bounce(params map[string]any, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	resp, err := c.Request("bounce", params, timeout)
	if err != nil {
		return "", err
	}
	if err := resp.Err(); err != nil {
		return "", err
	}
	var res struct {
		Path string `json:"path"`
		Ok   bool   `json:"ok"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return "", err
	}
	return res.Path, nil
}

// GetLevels 获取各轨峰值电平
func (c *Client) GetLevels(timeout time.Duration) ([]float32, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	resp, err := c.Request("getLevels", nil, timeout)
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

// SetLoop 设置循环区间
func (c *Client) SetLoop(on bool, start, end float64, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	resp, err := c.Request("setLoop", map[string]any{"on": on, "start": start, "end": end}, timeout)
	if err != nil {
		return err
	}
	return resp.Err()
}

// Shutdown 通知引擎退出
func (c *Client) Shutdown(timeout time.Duration) error {
	resp, err := c.Request("shutdown", nil, timeout)
	if err != nil {
		return err
	}
	if !resp.OK {
		return resp.Err()
	}
	return nil
}

func (c *Client) handshake(timeout time.Duration) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"client":          "ai_midi_supervisor",
	}

	resp, err := c.Request("hello", params, timeout)
	if err != nil {
		return fmt.Errorf("握手失败: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("握手被拒: %w", resp.Err())
	}

	var info EngineInfo
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		return fmt.Errorf("解析引擎信息失败: %w", err)
	}
	if info.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("协议版本不匹配: 期望 %d, 引擎 %d", ProtocolVersion, info.ProtocolVersion)
	}
	return nil
}

func (c *Client) nextRequestID() float64 {
	c.nextID++
	return c.nextID
}

// writeFrame 按协议编码并写入一帧：[uint32 LE 长度(含类型字节)][类型][载荷]。
// 写入由看门狗计时，超时判定连接不可信。
func (c *Client) writeFrame(typ byte, body []byte) error {
	return c.writeFrameWithTimeout(typ, body, 5*time.Second)
}

func (c *Client) writeFrameWithTimeout(typ byte, body []byte, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	frame := EncodeFrame(typ, body)

	done := make(chan error, 1)
	go func() {
		_, err := c.conn.Write(frame)
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("写入超时")
	}
}

// awaitResponse 串行读帧直到出现与 id 匹配的响应。
// 读循环放在辅助 goroutine 中以便 deadline 生效；超时后连接已不可信，
// markDead 并由调用方（supervisor）负责重建。
func (c *Client) awaitResponse(id float64, timeout time.Duration) (*Response, error) {
	deadline := time.Now().Add(timeout)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.markDead()
			return nil, fmt.Errorf("等待响应超时 (id=%v)", id)
		}
		// 单次读超时 = 剩余总超时：慢设备操作（如 ASIO 驱动打开 5-20s）
		// 是合法长请求，任何固定封顶都会把它误判为会话失效
		frame, err := c.readFrameWithTimeout(remaining)
		if err != nil {
			c.markDead()
			return nil, fmt.Errorf("读取帧失败: %w", err)
		}

		switch frame.Type {
		case MsgResponse:
			var r Response
			if err := json.Unmarshal(frame.Payload, &r); err != nil {
				c.markDead()
				return nil, fmt.Errorf("%w: 响应解析失败: %v", ErrProtocol, err)
			}
			if r.ID == nil || *r.ID != id {
				continue // 不匹配的响应（上一轮遗留），跳过
			}
			return &r, nil
		case MsgEvent:
			c.emitEvent(frame.Payload)
		case MsgTimecode:
			c.latchTimecode(frame.Payload)
		default:
			// Midi 等其他类型：当前客户端不主动消费
		}
	}
}

// frameResult 单帧读取结果
type frameResult struct {
	frame *Frame
	err   error
}

// readFrameWithTimeout 读取一帧（带看门狗）。帧格式与 DecodeFrame 一致：
// [uint32 LE 总长 n(含类型字节)][n 字节 payload]，payload[0] 为类型。
// 看门狗超时后调用方判死会话；遗留的读 goroutine 阻塞在已关闭的
// conn 上自然退出（同步管道句柄不支持读写并发，事务模型下每连接
// 同一时刻至多一个此类读者，不会与新读者抢帧）。
func (c *Client) readFrameWithTimeout(timeout time.Duration) (*Frame, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	res := make(chan frameResult, 1)
	go func() {
		var lenBuf [4]byte
		if _, err := ioReadFull(c.conn, lenBuf[:]); err != nil {
			res <- frameResult{err: err}
			return
		}
		total := int(binary.LittleEndian.Uint32(lenBuf[:]))
		if total <= 0 || total > MaxFrameSize {
			res <- frameResult{err: fmt.Errorf("%w: 非法帧长度 %d", ErrProtocol, total)}
			return
		}
		payload := make([]byte, total)
		if _, err := ioReadFull(c.conn, payload); err != nil {
			res <- frameResult{err: err}
			return
		}
		res <- frameResult{frame: &Frame{Type: payload[0], Payload: payload[1:]}}
	}()

	select {
	case r := <-res:
		return r.frame, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("读取超时")
	}
}

// latchTimecode 解析 25 字节走带帧载荷并锁存最新值。
// 布局与协议文档/引擎 Transport 一致：
// [samplePos int64][beatPos float64][bpm float64][playing uint8]，均 LE。
func (c *Client) latchTimecode(payload []byte) {
	if len(payload) < 25 {
		return
	}
	tc := Timecode{
		SamplePos: int64(binary.LittleEndian.Uint64(payload[0:8])),
		Beat:      float64FromBits(binary.LittleEndian.Uint64(payload[8:16])),
		BPM:       float64FromBits(binary.LittleEndian.Uint64(payload[16:24])),
		Playing:   payload[24] == 1,
	}
	c.tcMu.Lock()
	c.lastTc = tc
	c.hasTc = true
	c.tcMu.Unlock()
}

// LatchedTimecode 返回最近锁存的时间码与是否收到过
func (c *Client) LatchedTimecode() (Timecode, bool) {
	c.tcMu.Lock()
	defer c.tcMu.Unlock()
	return c.lastTc, c.hasTc
}

func float64FromBits(b uint64) float64 { return math.Float64frombits(b) }

func (c *Client) emitEvent(payload []byte) {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil || ev.Name == "" {
		return
	}
	c.eventsMu.Lock()
	defer c.eventsMu.Unlock()

	select {
	case c.events <- ev:
	default:
		// 通道满时丢弃（事件均为可刷新的状态类信息）
	}
}

func ioReadFull(f *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, fmt.Errorf("EOF")
		}
		total += n
	}
	return total, nil
}

func dialPipe(name string, timeout time.Duration) (*os.File, error) {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(name, os.O_RDWR, 0)
		if err == nil {
			return f, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("连接管道 %s 超时: %w", name, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
