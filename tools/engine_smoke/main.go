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
	"math"
	"os"
	"os/exec"
	"path/filepath"
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
	conn        *os.File
	nextID      float64
	everPlaying bool // 任一 timecode 帧 playing=true
	everStopped bool // 任一 timecode 帧 playing=false
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

// awaitResponse 串行读帧直到出现与 id 匹配的响应；事件帧打印，
// timecode 帧锁存播放状态（body = payload 去掉类型字节，playing 在 body[24]）
func (c *client) awaitResponse(id float64) (*response, error) {
	for {
		fr, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		if fr.typ == 0x05 && len(fr.body) >= 25 {
			if fr.body[24] == 1 {
				c.everPlaying = true
			} else {
				c.everStopped = true
			}
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

// drainTimecode 在时限内统计收到的 Timecode 帧（0x05）。
// 线格式：payload（含类型字节）= [0x05][samplePos int64][beat f64][bpm f64][playing u8]
// 故原始帧 p 中：p[0]=类型、p[9:17]=beat、p[25]=playing。
func (c *client) drainTimecode(d time.Duration) (count int, playingLast bool, beatLast float64) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		type res struct {
			p   []byte
			err error
		}
		ch := make(chan res, 1)
		go func() {
			var lb [4]byte
			if _, err := ioReadFull(c.conn, lb[:]); err != nil {
				ch <- res{nil, err}
				return
			}
			n := binary.LittleEndian.Uint32(lb[:])
			pl := make([]byte, n)
			if _, err := ioReadFull(c.conn, pl); err != nil {
				ch <- res{nil, err}
				return
			}
			ch <- res{pl, nil}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				return
			}
			if r.p[0] == 0x05 && len(r.p) >= 26 {
				count++
				playingLast = r.p[25] == 1
				beatLast = math.Float64frombits(binary.LittleEndian.Uint64(r.p[9:17]))
			}
		case <-time.After(300 * time.Millisecond):
		}
	}
	return
}

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

func jsonLoose(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
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
	// ASIO 枚举与引擎启动期的默认设备初始化存在竞争（虚拟 ASIO 驱动
	// 初始化期间扫描可能为空），缺失时延迟重试一次
	r, reqErr = c.request("listDevices", nil)
	check("listDevices", reqErr == nil && r.OK, errText(r, reqErr))
	hasASIO := false
	for attempt := 0; attempt < 2; attempt++ {
		if r != nil && r.OK {
			var ld listDevicesResult
			_ = json.Unmarshal(r.Result, &ld)
			hasASIO = false
			for _, d := range ld.Drivers {
				fmt.Printf("      驱动 %-14s 设备数 %d\n", d.Driver, len(d.Devices))
				if d.Driver == "ASIO" {
					hasASIO = true
				}
			}
			if hasASIO || attempt == 1 {
				break
			}
			fmt.Println("      [重试] 未发现 ASIO，2 秒后重试一次…")
		}
		time.Sleep(2 * time.Second)
		r, reqErr = c.request("listDevices", nil)
		if reqErr != nil || r == nil || !r.OK {
			break
		}
	}
	check("枚举结果包含 ASIO 驱动类型（JUCE_ASIO 已生效）", hasASIO,
		"未发现 ASIO 类型——确认 SDK 就位且 CMake 检测通过后重新构建")

	// 6) applySetup 空参数回环（不做机器相关的硬件假设）
	r, reqErr = c.request("applySetup", map[string]interface{}{})
	check("applySetup 回环", reqErr == nil && r.OK, errText(r, reqErr))

	// 7) 测试音开/关（会有一声短促 440Hz）
	r, reqErr = c.request("testTone", map[string]interface{}{"on": true, "freq": 440.0})
	check("testTone 开", reqErr == nil && r.OK, errText(r, reqErr))
	time.Sleep(600 * time.Millisecond)
	r, reqErr = c.request("testTone", map[string]interface{}{"on": false})
	check("testTone 关", reqErr == nil && r.OK, errText(r, reqErr))

	// 8.5) M2 合成器：加载失败须优雅表达；实时音符走二进制 Midi 帧（协议禁止 JSON 化）
	r, reqErr = c.request("loadSoundFont", map[string]interface{}{"path": "Z:/__no_such__.sf2"})
	loaded := true
	if r != nil && r.OK {
		loaded, _ = jsonLoose(r.Result)["loaded"].(bool)
	}
	check("loadSoundFont 缺文件优雅返回 loaded=false",
		reqErr == nil && r != nil && r.OK && !loaded, errText(r, reqErr))

	// Midi 帧线格式：payload = [类型0x04][status][data1][data2]
	err = c.writeFrame(0x04, []byte{0x90, 60, 100}) // noteOn ch0 key60 vel100
	check("Midi noteOn 帧写入", err == nil, fmt.Sprintf("err=%v", err))
	err = c.writeFrame(0x04, []byte{0x90, 64, 100}) // noteOn ch0 key64 vel100
	check("Midi noteOn 帧写入（第二音）", err == nil, fmt.Sprintf("err=%v", err))
	err = c.writeFrame(0x04, []byte{0x80, 60, 0}) // noteOff ch0 key60
	check("Midi noteOff 帧写入", err == nil, fmt.Sprintf("err=%v", err))
	err = c.writeFrame(0x04, []byte{0x80, 64, 0}) // noteOff ch0 key64（防声部悬挂）
	check("Midi noteOff 帧写入（第二音）", err == nil, fmt.Sprintf("err=%v", err))
	r, reqErr = c.request("ping", nil)
	check("MIDI 帧后连接仍健康", reqErr == nil && r.OK, errText(r, reqErr))

	// 8.6) 真实 SF2 加载（文件存在时）：守护器启动即自动加载默认音色，
	// 此处验证引擎对真实文件解析成功（tsf 渲染路径打通的前置条件）
	if sfPath, sfErr := filepath.Abs("Library/soundfonts/PianoteqTest.sf2"); sfErr == nil {
		if _, statErr := os.Stat(sfPath); statErr == nil {
			r, reqErr = c.request("loadSoundFont", map[string]interface{}{"path": sfPath})
			loadedReal := false
			if r != nil && r.OK {
				loadedReal, _ = jsonLoose(r.Result)["loaded"].(bool)
			}
			check("loadSoundFont 真实文件 loaded=true",
				reqErr == nil && r != nil && r.OK && loadedReal, errText(r, reqErr))
		} else {
			fmt.Println("[跳过] Library/soundfonts/PianoteqTest.sf2 不存在，跳过真实音色加载检查")
		}
	}

	// 7.6) M3 走带：play → timecode 推送 → locate/setTempo → stop。
	// 注意：drainTimecode 结束时残留的阻塞读 goroutine 会占住同步管道句柄，
	// 播放期间引擎 50Hz 推送可在 20ms 内喂饱它，但停止后无新帧 → 后续写入死锁。
	// 故停止后不再 drain，终态经 awaitResponse 内锁存 + timecode 拉取验证。
	r, reqErr = c.request("play", nil)
	check("play 应答", reqErr == nil && r.OK, errText(r, reqErr))
	tcCount, _, _ := c.drainTimecode(1200 * time.Millisecond)
	check(fmt.Sprintf("timecode 推送 ≥20 帧（实测 %d）", tcCount), tcCount >= 20,
		"引擎未按 50Hz 推送走带帧")

	r, reqErr = c.request("locate", map[string]interface{}{"beat": 8})
	check("locate 应答", reqErr == nil && r.OK, errText(r, reqErr))
	_, _, beatNow := c.drainTimecode(400 * time.Millisecond)
	check(fmt.Sprintf("locate 后 beat≈8（实测 %.2f）", beatNow),
		beatNow > 7.0 && beatNow < 9.5, "定位后拍位置不符")

	r, reqErr = c.request("setTempo", map[string]interface{}{"bpm": 140})
	check("setTempo 应答", reqErr == nil && r.OK, errText(r, reqErr))

	r, reqErr = c.request("stop", nil)
	check("stop 应答", reqErr == nil && r.OK, errText(r, reqErr))
	check("播放/停止状态切换均经推送帧观察到", c.everPlaying && c.everStopped,
		fmt.Sprintf("playing=%v stopped=%v", c.everPlaying, c.everStopped))

	r, reqErr = c.request("timecode", nil)
	tcJSON := jsonLoose(nil)
	if r != nil && r.OK {
		tcJSON = jsonLoose(r.Result)
	}
	playingJSON, _ := tcJSON["playing"].(bool)
	check("timecode 拉取 playing=false", reqErr == nil && r != nil && r.OK && !playingJSON,
		errText(r, reqErr))

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
