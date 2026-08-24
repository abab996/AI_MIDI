package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
)

// Client 原生音频引擎管道客户端（串行事务模型）。
//
// I/O 模型约束：os.OpenFile 打开的命名管道是同步（非重叠）句柄，
// 不支持 deadline，且不允许读写并发执行。因此本客户端把「写请求 +
// 读到匹配响应」作为一次完整事务，用互斥锁串行化；单次读写均由
// 看门狗 goroutine 计时，超时即判定连接不可信（markDead），由
// supervisor 负责重建会话。期间收到的事件帧转发到 Events 通道
// （带缓冲，满则丢弃——事件均为可刷新的状态类信息）。
type Client struct {
	conn     *os.File
	mu       sync.Mutex
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

	if err := c.writeFrame(MsgRequest, body); err != nil {
		c.markDead()
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}

	return c.awaitResponse(req.ID, timeout)
}

// SendMidi 发送 3 字节 MIDI 二进制帧（尽力而为，不等待响应）。
// 线格式：payload = [类型0x04][status][data1][data2]（status 为完整 MIDI 状态字节）。
func (c *Client) SendMidi(status, data1, data2 byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.writeFrame(MsgMidi, []byte{status, data1, data2})
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

// NoteOn 实时音符按下（二进制帧）
func (c *Client) NoteOn(channel, key, velocity int) error {
	return c.SendMidi(byte(0x90|(channel&0x0F)), byte(key&0x7F), byte(velocity&0x7F))
}

// NoteOff 实时音符释放（二进制帧）
func (c *Client) NoteOff(channel, key int) error {
	return c.SendMidi(byte(0x80|(channel&0x0F)), byte(key&0x7F), 0x00)
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
	frame := EncodeFrame(typ, body)

	done := make(chan error, 1)
	go func() {
		_, err := c.conn.Write(frame)
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("写入超时")
	}
}

// awaitResponse 串行读帧直到出现与 id 匹配的响应。
// 读循环放在辅助 goroutine 中以便 deadline 生效；超时后连接已不可信，
// markDead 并由调用方（supervisor）负责重建。
func (c *Client) awaitResponse(id float64, timeout time.Duration) (*Response, error) {
	deadline := time.After(timeout)

	for {
		select {
		case <-deadline:
			c.markDead()
			return nil, fmt.Errorf("等待响应超时 (id=%v)", id)
		default:
		}

		frame, err := c.readFrame()
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

// readFrame 读取一帧（带看门狗）。帧格式与 DecodeFrame 一致：
// [uint32 LE 总长 n(含类型字节)][n 字节 payload]，payload[0] 为类型。
func (c *Client) readFrame() (*Frame, error) {
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
	case <-time.After(5 * time.Second):
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
