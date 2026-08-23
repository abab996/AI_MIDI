// 引擎 IPC 冒烟测试客户端。
// 按 docs/引擎IPC协议.md v1 对 aimidi-engine 跑通全链路验收：
//
//	未握手拒绝 → hello(带版本) → 错误版本拒绝 → ping → listDevices(检查 ASIO 类型)
//	→ applySetup 回环 → testTone 开/关 → 断线重连(握手复位) → shutdown 优雅退出
//
// I/O 模型注意：os.OpenFile 打开的命名管道是同步（非重叠）句柄，不支持 deadline，
// 且不允许两个 goroutine 并发读写（会互相卡死）。因此本客户端在单一 goroutine 上
// 串行执行全部读写，每次读写由看门狗计时；事件帧（deviceChanged 等）会停留在
// 管道缓冲区中，随下一次请求一并读出并被跳过。连接建立阶段若探测失败则丢弃
// 句柄重建连接（服务端重新监听后的新实例必然可用）。
//
// 用法（主仓库根目录）：go run ./tools/engine_smoke [-engine bin/aimidi-engine.exe]
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"
)

const (
	msgRequest   = 0x01
	msgResponse  = 0x02
	ioTimeout    = 15 * time.Second // listDevices 枚举 ASIO/DirectSound 设备可能较慢
	dialTimeout  = 10 * time.Second
	maxRedial    = 5
)

type frame struct {
	typ  byte
	body []byte
}

type ioResult struct {
	f   frame
	n   int
	err error
}

type response struct {
	ID     *float64        `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type listDevicesResult struct {
	Drivers []struct {
		Driver  string `json:"driver"`
		Devices []struct {
			Name        string    `json:"name"`
			Channels    int       `json:"channels"`
			SampleRates []float64 `json:"sampleRates"`
		} `json:"devices"`
	} `json:"drivers"`
}

// client 串行 I/O 客户端：所有读写只发生在调用 request 的 goroutine 上
// （看门狗辅助 goroutine 仅阻塞在单次系统调用上，超时后由 close 解除）
type client struct {
	conn   *os.File
	nextID float64
}

func dialOnce(name string, timeout time.Duration) (*os.File, error) {
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

// readFrame 读一整帧（带看门狗）。超时时底层读可能仍挂在辅助 goroutine 上，
// 调用方应当关闭连接使其随进程退出。
func (c *client) readFrame() (frame, error) {
	res := make(chan ioResult, 1)
	go func() {
		var lenBuf [4]byte
		if _, err := ioReadFull(c.conn, lenBuf[:]); err != nil {
			res <- ioResult{err: err}
			return
		}
		n := binary.LittleEndian.Uint32(lenBuf[:])
		payload := make([]byte, n)
		if _, err := ioReadFull(c.conn, payload); err != nil {
			res <- ioResult{err: err}
			return
		}
		res <- ioResult{f: frame{typ: payload[0], body: payload[1:]}}
	}()
	select {
	case r := <-res:
		return r.f, r.err
	case <-time.After(ioTimeout):
		return frame{}, fmt.Errorf("读取超时")
	}
}

func (c *client) writeFrame(typ byte, body []byte) error {
	frameBytes := make([]byte, 0, 5+len(body))
	var head [4]byte
	binary.LittleEndian.PutUint32(head[:], uint32(len(body)+1))
	frameBytes = append(frameBytes, head[:]...)
	frameBytes = append(frameBytes, typ)
	frameBytes = append(frameBytes, body...)

	res := make(chan error, 1)
	go func() {
		_, err := c.conn.Write(frameBytes)
		res <- err
	}()
	select {
	case err := <-res:
		return err
	case <-time.After(ioTimeout):
		return fmt.Errorf("写入超时")
	}
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
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

// awaitResponse 串行读帧直到出现与 id 匹配的响应；期间收到的事件帧直接跳过
func (c *client) awaitResponse(id float64) (*response, error) {
	for {
		fr, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch fr.typ {
		case msgResponse:
			var r response
			if err := json.Unmarshal(fr.body, &r); err != nil {
				return nil, fmt.Errorf("响应解析失败: %w", err)
			}
			if r.ID != nil && *r.ID == id {
				return &r, nil
			}
		case 0x03:
			fmt.Printf("      [事件] %s\n", string(fr.body))
		default:
			fmt.Printf("      [其他帧] type=0x%02x\n", fr.typ)
		}
	}
}

func (c *client) request(method string, params map[string]interface{}) (*response, error) {
	c.nextID++
	id := c.nextID
	if params == nil {
		params = map[string]interface{}{}
	}
	body := mustJSON(map[string]interface{}{"id": id, "method": method, "params": params})
	if err := c.writeFrame(msgRequest, body); err != nil {
		return nil, err
	}
	return c.awaitResponse(id)
}

func (c *client) close() { _ = c.conn.Close() }

// openSession 建立经过验证的会话：连接后先发一次网关探测（ping 应被协议拒绝），
// 全部 I/O 带看门狗；任何传输层异常都丢弃句柄重连。返回的会话已通过网关验证，
// 尚未 hello。
func openSession(enginePID int) (*client, error) {
	name := fmt.Sprintf(`\\.\pipe\AI_MIDI_ENGINE_%d`, enginePID)
	var lastErr error
	for attempt := 1; attempt <= maxRedial; attempt++ {
		conn, err := dialOnce(name, dialTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		c := &client{conn: conn}

		gateBody := mustJSON(map[string]interface{}{
			"id": 0.5, "method": "ping", "params": map[string]interface{}{},
		})
		if err := c.writeFrame(msgRequest, gateBody); err != nil {
			lastErr = fmt.Errorf("第 %d 次连接写入失败: %w", attempt, err)
			c.close()
			continue
		}
		r, err := c.awaitResponse(0.5)
		if err != nil {
			lastErr = fmt.Errorf("第 %d 次连接探测失败: %w", attempt, err)
			c.close()
			continue
		}
		if r.OK || r.Error == nil || !contains(r.Error.Message, "engine not ready") {
			lastErr = fmt.Errorf("第 %d 次连接：网关未按协议拒绝（ok=%v）", attempt, r.OK)
			c.close()
			continue
		}
		return c, nil
	}
	return nil, lastErr
}

var failed int

func check(name string, cond bool, detail string) {
	if cond {
		fmt.Printf("[通过] %s\n", name)
	} else {
		failed++
		fmt.Printf("[失败] %s：%s\n", name, detail)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func errText(r *response, err error) string {
	if err != nil {
		return "transport: " + err.Error()
	}
	if !r.OK && r.Error != nil {
		return r.Error.Message
	}
	return ""
}

func expectError(name, gotErr, wantSub string) {
	check(name, gotErr != "" && contains(gotErr, wantSub), fmt.Sprintf("期望错误含 %q，实际 %q", wantSub, gotErr))
}

func main() {
	enginePath := flag.String("engine", "bin/aimidi-engine.exe", "引擎可执行文件路径")
	flag.Parse()

	if _, err := os.Stat(*enginePath); err != nil {
		fmt.Printf("[失败] 找不到引擎 %s（请先运行 tools/build_engine.bat）\n", *enginePath)
		os.Exit(1)
	}

	cmd := exec.Command(*enginePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("[失败] 启动引擎进程失败:", err)
		os.Exit(1)
	}
	fmt.Printf("引擎已启动 pid=%d\n", cmd.Process.Pid)

	killIfHung := true
	defer func() {
		if killIfHung && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// 1) 建立会话（内含网关拒绝验证与自愈重连）
	c, err := openSession(cmd.Process.Pid)
	if err != nil {
		fmt.Println("[失败]", err)
		_ = cmd.Process.Kill()
		os.Exit(1)
	}
	check("握手前 ping 被拒绝（openSession 内验证）", true, "")

	// 2) hello 正常握手（带版本）
	r, reqErr := c.request("hello", map[string]interface{}{"protocolVersion": 1})
	check("hello 握手", reqErr == nil && r.OK, errText(r, reqErr))
	if r != nil && r.OK {
		var info struct {
			Name            string `json:"name"`
			Version         string `json:"version"`
			ProtocolVersion int    `json:"protocolVersion"`
		}
		_ = json.Unmarshal(r.Result, &info)
		check("hello 返回引擎信息",
			info.Name == "aimidi-engine" && info.ProtocolVersion == 1,
			fmt.Sprintf("name=%q proto=%d", info.Name, info.ProtocolVersion))
	}

	// 3) 版本不匹配被拒（双向版本校验）
	r, reqErr = c.request("hello", map[string]interface{}{"protocolVersion": 99})
	expectError("错误版本号被拒绝", errText(r, reqErr), "protocol version mismatch")

	// 4) ping
	r, reqErr = c.request("ping", nil)
	check("ping→pong", reqErr == nil && r.OK && string(r.Result) == `"pong"`, errText(r, reqErr))

	// 5) 设备枚举（ASIO 编译开关验证点）
	r, reqErr = c.request("listDevices", nil)
	check("listDevices", reqErr == nil && r.OK, errText(r, reqErr))
	if r != nil && r.OK {
		var ld listDevicesResult
		_ = json.Unmarshal(r.Result, &ld)
		hasASIO := false
		for _, d := range ld.Drivers {
			fmt.Printf("      驱动 %-14s 设备数 %d\n", d.Driver, len(d.Devices))
			if d.Driver == "ASIO" {
				hasASIO = true
			}
		}
		check("枚举结果包含 ASIO 驱动类型（JUCE_ASIO 已生效）", hasASIO,
			"未发现 ASIO 类型——确认 SDK 就位且 CMake 检测通过后重新构建")
	}

	// 6) applySetup 空参数回环（不做机器相关的硬件假设）
	r, reqErr = c.request("applySetup", map[string]interface{}{})
	check("applySetup 回环", reqErr == nil && r.OK, errText(r, reqErr))

	// 7) 测试音开/关（会有一声短促 440Hz）
	r, reqErr = c.request("testTone", map[string]interface{}{"on": true, "freq": 440.0})
	check("testTone 开", reqErr == nil && r.OK, errText(r, reqErr))
	time.Sleep(600 * time.Millisecond)
	r, reqErr = c.request("testTone", map[string]interface{}{"on": false})
	check("testTone 关", reqErr == nil && r.OK, errText(r, reqErr))

	// 8) 断线重连：握手状态应复位（openSession 同样验证网关拒绝）
	c.close()
	c2, err := openSession(cmd.Process.Pid)
	if err != nil {
		fmt.Println("[失败] 重连失败:", err)
		os.Exit(1)
	}
	check("重连后网关复位（openSession 内验证）", true, "")
	r, reqErr = c2.request("hello", nil)
	check("重连后重新 hello", reqErr == nil && r.OK, errText(r, reqErr))

	// 9) 优雅退出
	r, reqErr = c2.request("shutdown", nil)
	check("shutdown 应答", reqErr == nil && r.OK && string(r.Result) == `"bye"`, errText(r, reqErr))
	c2.close()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err := <-waitCh:
		killIfHung = false
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			code = -1
		}
		check("引擎优雅退出（exit code 0）", code == 0, fmt.Sprintf("exit code=%d err=%v", code, err))
	case <-time.After(5 * time.Second):
		fmt.Println("[失败] 引擎 shutdown 后 5 秒内未退出")
		failed++
	}

	if failed > 0 {
		fmt.Printf("\n冒烟测试：共 %d 项失败\n", failed)
		os.Exit(1)
	}
	fmt.Println("\n冒烟测试全部通过 ✅")
}
