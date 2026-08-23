package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Client 引擎管道客户端。
//
// I/O 模型约束：os.OpenFile 打开的命名管道是同步（非重叠）句柄，
// 不允许读写并发执行（会互相卡死）。因此本客户端把「写请求 + 读到匹配
// 响应」作为一次完整事务，用互斥锁串行化；期间收到的事件帧转发到
// Events 通道（带缓冲，满则丢弃——事件均为可刷新的状态类信息）。
//
// 已知取舍：请求无法流水线化（慢方法会排队），心跳等高频通道
// 留待 M2 切换重叠 I/O 后开启。对 M1 的启动/守护/设备管理足够。
type Client struct {
	conn     *os.File
	mu       sync.Mutex
	nextID   float64
	dead     chan struct{}
	deadOnce sync.Once

	eventsMu sync.Mutex
	events   chan Event
}

// Dial 连接引擎管道并完成协议握手（hello + 版本校验）。
// 管道在引擎进程创建后数百毫秒内才出现，retry 负责等待。
func Dial(pid int, dialTimeout, handshakeTimeout time.Duration) (*Client, error) {
	name := fmt.Sprintf(`\\.\pipe\AI_MIDI_ENGINE_%d`, pid)
	deadline := time.Now().Add(dialTimeout)

	var conn *os.File
	var lastErr error
	for {
		f, err := os.OpenFile(name, os.O_RDWR, 0)
		if err == nil {
			conn = f
			break
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("连接引擎管道超时: %w", lastErr)
		}
		time.Sleep(100 * time.Millisecond)
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

// Events 事件流。通道满时新事件被丢弃（当前仅 deviceChanged 等状态类事件）。
func (c *Client) Events() <-chan Event { return c.events }

func (c *Client) handshake(timeout time.Duration) error {
	raw, err := c.call(timeout, map[string]any{
		"id": 0, "method": "hello",
		"params": map[string]any{"protocolVersion": ProtocolVersion},
	})
	if err != nil {
		return fmt.Errorf("握手失败: %w", err)
	}
	var hello struct {
		Name            string `json:"name"`
		ProtocolVersion int    `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw, &hello); err != nil {
		return fmt.Errorf("握手响应解析失败: %w", err)
	}
	if hello.Name != "aimidi-engine" || hello.ProtocolVersion != int(ProtocolVersion) {
		return fmt.Errorf("握手校验失败: name=%q proto=%d", hello.Name, hello.ProtocolVersion)
	}
	return nil
}

// Request 发送一个方法调用并等待其响应（串行化）。
// 返回 result 字段的原始 JSON。
func (c *Client) Request(timeout time.Duration, method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(timeout, map[string]any{
		"id": c.nextRequestID(), "method": method, "params": params,
	})
}

func (c *Client) nextRequestID() float64 {
	c.nextID++
	return c.nextID
}

// call 无锁事务：写入请求帧 → 逐帧读取直到匹配 id 的响应。
// 必须持有 c.mu（handshake 阶段除外，彼时无并发）。
func (c *Client) call(timeout time.Duration, msg map[string]any) (json.RawMessage, error) {
	id := 0.0
	if v, ok := msg["id"].(float64); ok {
		id = v
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- writeAll(c.conn, EncodeFrame(MsgRequest, payload)) }()

	select {
	case err := <-done:
		if err != nil {
			c.markDead()
			return nil, fmt.Errorf("发送失败: %w", err)
		}
	case <-time.After(5 * time.Second):
		c.markDead()
		return nil, fmt.Errorf("发送超时")
	}

	deadline := time.After(timeout)
	var lenBuf [4]byte
	for {
		select {
		case <-deadline:
			return nil, fmt.Errorf("等待 %s 响应超时", msg["method"])
		default:
		}

		// 读循环放在辅助 goroutine 中以便 deadline 生效；
		// 超时后连接已不可信，由调用方（supervisor）负责重建
		type readRes struct {
			frame *Frame
			n     int
			err   error
		}
		res := make(chan readRes, 1)
		go func() {
			frame, n, err := readFrameWith(c.conn, lenBuf[:])
			res <- readRes{frame, n, err}
		}()

		select {
		case rr := <-res:
			if rr.err != nil {
				c.markDead()
				return nil, fmt.Errorf("读取失败: %w", rr.err)
			}
			switch rr.frame.Type {
			case MsgResponse:
				var resp struct {
					ID     *float64        `json:"id"`
					OK     bool            `json:"ok"`
					Result json.RawMessage `json:"result"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rr.frame.Payload, &resp); err != nil {
					c.markDead()
					return nil, fmt.Errorf("%w: 响应解析失败: %v", ErrProtocol, err)
				}
				if resp.ID == nil || *resp.ID != id {
					continue // 不匹配的响应（上一轮遗留），跳过
				}
				if !resp.OK {
					msgText := "unknown error"
					if resp.Error != nil {
						msgText = resp.Error.Message
					}
					return nil, &EngineError{Method: msg["method"].(string), Message: msgText}
				}
				return resp.Result, nil
			case MsgEvent:
				c.emitEvent(rr.frame.Payload)
			default:
				// Midi/Timecode（M2 起）：暂不处理
			}
		case <-deadline:
			c.markDead()
			return nil, fmt.Errorf("等待 %s 响应超时", msg["method"])
		}
	}
}

func (c *Client) emitEvent(payload []byte) {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil || ev.Name == "" {
		return
	}
	c.eventsMu.Lock()
	select {
	case c.events <- ev:
	default:
	}
	c.eventsMu.Unlock()
}

// Close 关闭底层连接并标记会话终结
func (c *Client) Close() error {
	c.markDead()
	return c.conn.Close()
}

// Done 会话因传输层故障或 Close 而终结时关闭。
// 守护器以此感知"IPC 失联"（区别于进程退出）。
func (c *Client) Done() <-chan struct{} { return c.dead }

func (c *Client) markDead() {
	c.deadOnce.Do(func() { close(c.dead) })
}

// EngineError 引擎返回的业务错误（ok=false）
type EngineError struct {
	Method  string
	Message string
}

func (e *EngineError) Error() string { return e.Method + ": " + e.Message }

// writeAll 全量写入
func writeAll(f *os.File, data []byte) error {
	total := 0
	for total < len(data) {
		n, err := f.Write(data[total:])
		total += n
		if err != nil {
			return err
		}
	}
	return nil
}

// readFrameWith 从文件读取一帧（lenBuf 复用调用方缓冲）
func readFrameWith(f *os.File, lenBuf []byte) (*Frame, int, error) {
	if _, err := ioReadFull(f, lenBuf[:4]); err != nil {
		return nil, 0, err
	}
	total := int(binary.LittleEndian.Uint32(lenBuf[:4]))
	if total <= 0 || total > MaxFrameSize {
		return nil, 0, fmt.Errorf("%w: 非法帧长度 %d", ErrProtocol, total)
	}
	payload := make([]byte, total)
	if _, err := ioReadFull(f, payload); err != nil {
		return nil, 0, err
	}
	return &Frame{Type: payload[0], Payload: payload[1:]}, 4 + total, nil
}

func ioReadFull(f *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
