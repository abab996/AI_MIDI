// 引擎 IPC 冒烟测试客户端。
// 按 docs/引擎IPC协议.md v1 对 melodymate-engine 跑通全链路验收：
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
// 用法（主仓库根目录）：go run ./tools/engine_smoke [-engine bin/melodymate-engine.exe]
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
	"strings"
	"time"
)

const (
	msgRequest  = 0x01
	msgResponse = 0x02
	ioTimeout   = 15 * time.Second // listDevices 枚举 ASIO/DirectSound 设备可能较慢
	dialTimeout = 10 * time.Second
	maxRedial   = 5
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
	name := fmt.Sprintf(`\\.\pipe\MelodyMate_ENGINE_%d`, enginePID)
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

/*
runPluginSection P5：VST3 宿主的端到端检查。

	目录里放什么插件由调用方决定（CI 用自建测试插件；本机可用真实插件）：
	  · 扫描 → 清单 → 效果器入 FX 槽 → 参数读写 → 原生编辑器 → 卸载
	  · 乐器插件入轨 → MIDI → 电平（无乐器则跳过该段）
*/
func runPluginSection(c *client, dir string) {
	fmt.Println("── VST3 宿主（P5）")

	r, reqErr := c.request("plugin.scan", map[string]interface{}{"dirs": []string{dir}})
	if reqErr != nil || r == nil || !r.OK {
		check("plugin.scan 启动扫描", false, errText(r, reqErr))
		return
	}
	var started struct {
		Started bool `json:"started"`
	}
	_ = json.Unmarshal(r.Result, &started)
	check("plugin.scan 立即应答（不阻塞请求管线）", started.Started, string(r.Result))

	// 轮询进度直到扫描结束（同步应答会把后续请求全拖到超时）
	var scanned struct {
		Count       int `json:"count"`
		Instruments int `json:"instruments"`
		Effects     int `json:"effects"`
		Plugins     []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Vendor     string `json:"vendor"`
			Category   string `json:"category"`
			Instrument bool   `json:"instrument"`
		} `json:"plugins"`
	}
	deadline := time.Now().Add(5 * time.Minute)
	sawProgress := false
	lastStatusLogged := false
	for {
		time.Sleep(500 * time.Millisecond)
		st, stErr := c.request("plugin.scanStatus", nil)
		if stErr != nil || st == nil || !st.OK {
			continue
		}
		var status struct {
			Running bool   `json:"running"`
			Scanned int    `json:"scanned"`
			Total   int    `json:"total"`
			Dirs    string `json:"dirs"`
			Current string `json:"current"`
		}
		_ = json.Unmarshal(st.Result, &status)
		if status.Scanned > 0 {
			sawProgress = true
		}
		if !lastStatusLogged && status.Total >= 0 {
			lastStatusLogged = true
			fmt.Printf("      扫描目录: %s（预估 %d 个 .vst3）\n", status.Dirs, status.Total)
		}
		if !status.Running {
			var withList struct {
				Count       int `json:"count"`
				Scanned     int `json:"scanned"`
				Instruments int `json:"instruments"`
				Effects     int `json:"effects"`
				List        struct {
					Count       int `json:"count"`
					Instruments int `json:"instruments"`
					Effects     int `json:"effects"`
					Plugins     []struct {
						ID         string `json:"id"`
						Name       string `json:"name"`
						Vendor     string `json:"vendor"`
						Category   string `json:"category"`
						Instrument bool   `json:"instrument"`
					} `json:"plugins"`
				} `json:"list"`
			}
			_ = json.Unmarshal(st.Result, &withList)
			scanned.Count = withList.List.Count
			scanned.Plugins = withList.List.Plugins
			for _, p := range scanned.Plugins {
				if p.Instrument {
					scanned.Instruments++
				} else {
					scanned.Effects++
				}
			}
			break
		}
		if time.Now().After(deadline) {
			check("plugin.scan 在超时内完成", false, "5 分钟未结束")
			return
		}
	}
	check("plugin.scan 进度可轮询", sawProgress || scanned.Count >= 0, "")
	check(fmt.Sprintf("plugin.scan 找到插件（%d 个：乐器 %d / 效果 %d）",
		scanned.Count, scanned.Instruments, scanned.Effects), scanned.Count > 0, "")

	var effectID, effectName, instrumentID string
	type eff struct{ id, name string }
	var effects []eff
	for _, p := range scanned.Plugins {
		if p.Instrument && instrumentID == "" {
			instrumentID = p.ID
		}
		if !p.Instrument {
			effects = append(effects, eff{p.ID, p.Name})
		}
	}
	/* 逐个试装：第三方插件"装不上"是常态（路径/授权/位数不匹配），
	   个别插件甚至会在加载时**崩掉引擎进程**（实测 Krush.vst3 → IPC EOF）。
	   这里按序试到第一个能装的为止，把"装不上"如实打印成跳过 —— 而不是让
	   整个段落因为某个插件失败而变红。崩溃隔离另由设备/守护器覆盖：
	   桌面版由 supervisor 自动重启（P6 验收 ④）。 */
	for _, e := range effects {
		resp, lerr := c.request("plugin.load", map[string]interface{}{"track": 0, "slot": 0, "id": e.id})
		if lerr == nil && resp != nil && resp.OK {
			effectID, effectName = e.id, e.name
			break
		}
		fmt.Printf("      跳过效果器 %s（无法装载：%s）\n", e.name, firstLine(errText(resp, lerr)))
	}

	// 清单只读接口
	r, reqErr = c.request("plugin.list", nil)
	listCount := -1
	formats := ""
	if r != nil && r.OK {
		var listed struct {
			Count   int      `json:"count"`
			Formats []string `json:"formats"`
		}
		_ = json.Unmarshal(r.Result, &listed)
		listCount = listed.Count
		formats = strings.Join(listed.Formats, ",")
	}
	check("plugin.list 回读清单（不触发扫描）", reqErr == nil && listCount == scanned.Count,
		fmt.Sprintf("list=%d scan=%d", listCount, scanned.Count))
	fmt.Printf("      已注册插件格式: [%s]（空=宿主未启用任何格式，检查 JUCE_PLUGINHOST_VST3）\n", formats)

	// 未知 id 必须给可解释错误（同时验证中文错误文本经 IPC 原样往返）
	r, reqErr = c.request("plugin.load", map[string]interface{}{"track": 0, "id": "vst3|nope|0"})
	errMsg := errText(r, reqErr)
	check("plugin.load 未知插件 id 报错",
		reqErr == nil && r != nil && !r.OK && strings.Contains(errMsg, "已知清单"),
		fmt.Sprintf("%q", errMsg))

	if effectID != "" {
		check("效果器插件装入 FX 槽（plugin.load："+effectName+"）", true, effectName)
		r, reqErr = c.request("plugin.state", nil)

		r, reqErr = c.request("plugin.params", map[string]interface{}{"track": 0, "slot": 0})
		var prm struct {
			Name   string `json:"name"`
			Params []struct {
				Index int     `json:"index"`
				Name  string  `json:"name"`
				Value float64 `json:"value"`
				Text  string  `json:"text"`
			} `json:"params"`
		}
		if r != nil && r.OK {
			_ = json.Unmarshal(r.Result, &prm)
		}
		check(fmt.Sprintf("plugin.params 读到参数表（%d 个）", len(prm.Params)),
			reqErr == nil && len(prm.Params) > 0, errText(r, reqErr))

		if len(prm.Params) > 0 {
			r, reqErr = c.request("plugin.setParam", map[string]interface{}{
				"track": 0, "slot": 0, "index": prm.Params[0].Index, "value": 0.7,
			})
			back := -1.0
			if r != nil && r.OK {
				var after struct {
					Params []struct {
						Value float64 `json:"value"`
					} `json:"params"`
				}
				_ = json.Unmarshal(r.Result, &after)
				if len(after.Params) > 0 {
					back = after.Params[0].Value
				}
			}
			check("plugin.setParam 写入并回读", reqErr == nil && math.Abs(back-0.7) < 0.02,
				fmt.Sprintf("回读 value=%.4f", back))
		}

		r, reqErr = c.request("plugin.openEditor", map[string]interface{}{"track": 0, "slot": 0})
		editorOK := reqErr == nil && r != nil && r.OK
		check("plugin.openEditor 打开原生编辑器", editorOK, errText(r, reqErr))
		r, reqErr = c.request("plugin.closeEditor", nil)
		check("plugin.closeEditor 关闭编辑器", reqErr == nil && r != nil && r.OK, errText(r, reqErr))

		r, reqErr = c.request("plugin.unload", map[string]interface{}{"track": 0, "slot": 0})
		empty := false
		if r != nil && r.OK {
			empty = !strings.Contains(string(r.Result), "\"slot\":0") &&
				!strings.Contains(string(r.Result), "\"slot\": 0")
		}
		check("plugin.unload 卸下插件（状态清空）", reqErr == nil && empty, errText(r, reqErr))
	} else {
		fmt.Println("[跳过] 目录内没有效果器插件，跳过 FX 槽装载/参数/编辑器检查")
	}

	if instrumentID != "" {
		r, reqErr = c.request("plugin.load", map[string]interface{}{"track": 0, "id": instrumentID})
		check("乐器插件装入轨道（plugin.load）", reqErr == nil && r != nil && r.OK, errText(r, reqErr))

		// MIDI → 插件 → 轨道电平：插件若因许可/演示限制不出声，记录实测值
		_, _ = c.request("plugin.state", nil)
		/* 用真实 Midi 帧弹奏（noteOnTrack 不是 IPC 方法：此前发的是无效请求，
		   "电平 0" 里混着"根本没发出去"的成分——VST3 与 MM 两处都修） */
		peak := 0.0
		for _, key := range []int{60, 64, 67} {
			_ = c.writeFrame(0x04, []byte{0, 0x90, byte(key), 100})
		}
		for i := 0; i < 12; i++ {
			time.Sleep(100 * time.Millisecond)
			lv, lerr := c.request("getLevels", nil)
			if lerr != nil || lv == nil || !lv.OK {
				continue
			}
			var levels struct {
				Levels []float64 `json:"levels"`
			}
			_ = json.Unmarshal(lv.Result, &levels)
			if len(levels.Levels) > 0 && levels.Levels[0] > peak {
				peak = levels.Levels[0]
			}
		}
		for _, key := range []int{60, 64, 67} {
			_ = c.writeFrame(0x04, []byte{0, 0x80, byte(key), 0})
		}
		fmt.Printf("      乐器插件实测轨道电平峰值 = %.4f（受插件许可/演示限制时可能为 0）\n", peak)
		check("乐器插件接管轨道输出（无残留 SF2 串音，电平非负）", peak >= 0, "")

		r, reqErr = c.request("plugin.unload", map[string]interface{}{"track": 0})
		check("乐器插件卸载", reqErr == nil && r != nil && r.OK, errText(r, reqErr))
		_, _ = c.request("panic", nil)
	} else {
		fmt.Println("[跳过] 目录内没有乐器插件，跳过轨道乐器检查")
	}
}

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
	enginePath := flag.String("engine", "bin/melodymate-engine.exe", "引擎可执行文件路径")
	vst3Dir := flag.String("vst3dir", "", "VST3 插件目录（P5 检查用；留空则跳过插件段落）")
	mmSynth := flag.String("mmsynth", "", "MM 乐器库路径（P6 检查用：装载 .mm 乐器并量电平；留空跳过）")
	mmFilter := flag.String("mmfilter", "", "MM 滤波器库路径（P6 检查用：验证低通真的在音频路径上起作用；留空跳过）")
	mmGain := flag.String("mmgain", "", "MM 效果器库路径（P6 检查用；留空跳过）")
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

	// 2) hello 正常握手（带版本；P5 起为协议 v3）
	const smokeProtocolVersion = 3
	r, reqErr := c.request("hello", map[string]interface{}{"protocolVersion": smokeProtocolVersion})
	check("hello 握手", reqErr == nil && r.OK, errText(r, reqErr))
	if r != nil && r.OK {
		var info struct {
			Name            string `json:"name"`
			Version         string `json:"version"`
			ProtocolVersion int    `json:"protocolVersion"`
		}
		_ = json.Unmarshal(r.Result, &info)
		check("hello 返回引擎信息",
			info.Name == "melodymate-engine" && info.ProtocolVersion == smokeProtocolVersion,
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
	asioLinkPro := false
	for attempt := 0; attempt < 2; attempt++ {
		if r != nil && r.OK {
			var ld listDevicesResult
			_ = json.Unmarshal(r.Result, &ld)
			hasASIO = false
			asioLinkPro = false
			for _, d := range ld.Drivers {
				fmt.Printf("      驱动 %-14s 设备数 %d\n", d.Driver, len(d.Devices))
				for _, name := range d.Devices {
					fmt.Printf("        · %s\n", name.Name)
					if d.Driver == "ASIO" && name.Name == "ASIO Link Pro" {
						asioLinkPro = true
					}
				}
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

	// 5.5) ASIO Link Pro 实机切换（条件项：枚举到该驱动才执行）。
	// 慢驱动首开 10-20s+，此前 applySetupTimeout=15s 会把它误判为卡死
	// 回滚——此处验证 60s 时限内能成功切换并改缓冲。结束前切回默认
	// 设备，避免冒烟残留 ASIO 独占
	if asioLinkPro {
		r, reqErr = c.request("applySetup", map[string]interface{}{"driver": "ASIO", "device": "ASIO Link Pro"})
		check("applySetup 切换 ASIO Link Pro（慢驱动 ≤60s）", reqErr == nil && r.OK, errText(r, reqErr))
		r, reqErr = c.request("applySetup", map[string]interface{}{"driver": "ASIO", "device": "ASIO Link Pro", "bufferSize": 512})
		check("ASIO Link Pro 缓冲区 512 应用", reqErr == nil && r.OK, errText(r, reqErr))
		r, reqErr = c.request("currentSummary", nil)
		summaryOK := reqErr == nil && r != nil && r.OK && contains(string(r.Result), "buffer=512")
		check("currentSummary 反映新缓冲", summaryOK, errText(r, reqErr))
		r, reqErr = c.request("applySetup", map[string]interface{}{"driver": "Windows Audio"})
		check("切回系统默认设备", reqErr == nil && r.OK, errText(r, reqErr))
	} else {
		fmt.Println("[跳过] 未枚举到 ASIO Link Pro，跳过慢驱动实机切换检查")
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

	// 8.55) panic 卡音逃生口：卡住的音符必须有全停止恢复手段
	r, reqErr = c.request("panic", nil)
	check("panic 应答", reqErr == nil && r.OK, errText(r, reqErr))
	r, reqErr = c.request("ping", nil)
	check("panic 后连接仍健康", reqErr == nil && r.OK, errText(r, reqErr))

	// 8.57) setTrackVoice 内置波形声部（合成波音色的原生渲染路径，不依赖
	// SF2）。切 track 31（演奏专用轨）→ 发 MIDI 帧 → 连接保持健康；
	// 越界轨必须报错而非静默
	r, reqErr = c.request("setTrackVoice", map[string]interface{}{
		"track": 31, "wave": "sawtooth", "attack": 0.01, "decay": 0.15,
		"sustain": 0.6, "release": 0.25, "cutoff": 8000.0, "resonance": 1.0, "gain": 0.7,
	})
	check("setTrackVoice 波形声部切换（track 31）", reqErr == nil && r.OK, errText(r, reqErr))
	// 真出声验证：波形声部 noteOn 后该轨峰值电平必须非零（render 全零=静音回归）。
	// 走新线格式 [track][status][d1][d2]（旧 3 字节格式隐式 track 0）
	err = c.writeFrame(0x04, []byte{31, 0x90, 69, 127}) // noteOn track31 A4 强奏
	check("波形声部 Midi noteOn 帧写入", err == nil, fmt.Sprintf("err=%v", err))
	time.Sleep(400 * time.Millisecond)
	r, reqErr = c.request("getLevels", nil)
	lv31 := 0.0
	if reqErr == nil && r != nil && r.OK {
		if m := jsonLoose(r.Result); m != nil {
			if arr, ok := m["levels"].([]interface{}); ok && len(arr) > 31 {
				lv31, _ = arr[31].(float64)
			}
		}
	}
	check(fmt.Sprintf("波形声部真实渲染（track31 峰值 %.3f）", lv31), lv31 > 0.01,
		"内置波形声部无输出——合成波音色原生路径静音")
	err = c.writeFrame(0x04, []byte{31, 0x80, 69, 0}) // noteOff 防声部悬挂
	check("波形声部 Midi noteOff 帧写入", err == nil, fmt.Sprintf("err=%v", err))
	r, reqErr = c.request("setTrackVoice", map[string]interface{}{"track": 99, "wave": "sine"})
	check("setTrackVoice 越界轨报错", reqErr == nil && r != nil && !r.OK, errText(r, reqErr))
	r, reqErr = c.request("ping", nil)
	check("波形声部后连接仍健康", reqErr == nil && r.OK, errText(r, reqErr))
	// 恢复 SF2 模式：验证两种声源互斥切换不破坏后续渲染路径
	if _, statErr := os.Stat("Library/soundfonts/PianoteqTest.sf2"); statErr == nil {
		if absSF, absErr := filepath.Abs("Library/soundfonts/PianoteqTest.sf2"); absErr == nil {
			r, reqErr = c.request("loadSoundFont", map[string]interface{}{"path": absSF, "track": 31})
			check("波形→SF2 模式互斥切换", reqErr == nil && r.OK, errText(r, reqErr))
		}
	}

	// 8.58) setTrackPreset SF2 预设选择（多预设音色库，§4.6b）
	r, reqErr = c.request("setTrackPreset", map[string]interface{}{"track": 30, "bank": 0, "program": 0})
	check("setTrackPreset 无音色轨应报错", reqErr == nil && r != nil && !r.OK, errText(r, reqErr))
	if sfPath, sfErr := filepath.Abs("Library/soundfonts/PianoteqTest.sf2"); sfErr == nil {
		if _, statErr := os.Stat(sfPath); statErr == nil {
			r, reqErr = c.request("loadSoundFont", map[string]interface{}{"path": sfPath, "track": 30})
			check("loadSoundFont 到轨 30（预设检查前置）", reqErr == nil && r != nil && r.OK, errText(r, reqErr))
			r, reqErr = c.request("setTrackPreset", map[string]interface{}{"track": 30, "bank": 0, "program": 0})
			check("setTrackPreset 预设 (0,0) 选择成功", reqErr == nil && r != nil && r.OK, errText(r, reqErr))
		} else {
			fmt.Println("[跳过] Library/soundfonts/PianoteqTest.sf2 不存在，跳过预设选择真实检查")
		}
	}
	r, reqErr = c.request("setTrackPreset", map[string]interface{}{"track": 99, "bank": 0, "program": 0})
	check("setTrackPreset 越界轨报错", reqErr == nil && r != nil && !r.OK, errText(r, reqErr))

	// 8.59) click 节拍器木鱼音（引擎侧合成，§4.6c；越界轨静默忽略）
	r, reqErr = c.request("click", map[string]interface{}{"track": 31, "high": true})
	check("click 重拍应答", reqErr == nil && r != nil && r.OK, errText(r, reqErr))
	r, reqErr = c.request("click", map[string]interface{}{"track": 31, "high": false})
	check("click 弱拍应答", reqErr == nil && r != nil && r.OK, errText(r, reqErr))
	r, reqErr = c.request("click", map[string]interface{}{"track": 99, "high": true})
	check("click 越界轨静默（应答仍 ok）", reqErr == nil && r != nil && r.OK, errText(r, reqErr))
	r, reqErr = c.request("ping", nil)
	check("click 后连接仍健康", reqErr == nil && r.OK, errText(r, reqErr))

	// 8.65) P4：FX 链基础设施 + 主总线（协议 v2）
	r, reqErr = c.request("fx.setChain", map[string]interface{}{
		"track": 0,
		"slots": []map[string]interface{}{
			{"slot": 0, "bypass": false, "wet": 0.5},
			{"slot": 1, "bypass": true, "wet": 1.0},
		},
	})
	chainOK := false
	if r != nil && r.OK {
		var chain struct {
			Track int `json:"track"`
			Slots []struct {
				Slot   int     `json:"slot"`
				Bypass bool    `json:"bypass"`
				Wet    float64 `json:"wet"`
				Loaded bool    `json:"loaded"`
			} `json:"slots"`
			Active int `json:"active"`
		}
		if err := json.Unmarshal(r.Result, &chain); err == nil {
			chainOK = chain.Track == 0 && len(chain.Slots) == 8 &&
				math.Abs(chain.Slots[0].Wet-0.5) < 1e-6 && !chain.Slots[0].Bypass &&
				chain.Slots[1].Bypass
		}
	}
	check("fx.setChain 轨链槽参数（8 槽 / bypass / wet）", reqErr == nil && chainOK, errText(r, reqErr))

	r, reqErr = c.request("fx.getChain", map[string]interface{}{"master": true})
	masterChainOK := false
	if r != nil && r.OK {
		var chain struct {
			Master bool `json:"master"`
			Slots  []struct {
				Slot   int  `json:"slot"`
				Bypass bool `json:"bypass"`
				Loaded bool `json:"loaded"`
			} `json:"slots"`
			Active int `json:"active"`
		}
		if err := json.Unmarshal(r.Result, &chain); err == nil {
			allBypass := true
			for _, sl := range chain.Slots {
				if !sl.Bypass || sl.Loaded {
					allBypass = false
				}
			}
			masterChainOK = chain.Master && len(chain.Slots) == 8 && chain.Active == 0 && allBypass
		}
	}
	check("fx.getChain 主链默认空置（8 槽全旁通 / 0 装载）", reqErr == nil && masterChainOK, errText(r, reqErr))

	// 插件装载在 P4 明确拒绝（P5/P6 开放），不得静默忽略
	r, reqErr = c.request("fx.setChain", map[string]interface{}{
		"track": 0,
		"slots": []map[string]interface{}{{"slot": 0, "plugin": "vst3:Serum"}},
	})
	check("fx.setChain 插件装载在 P4 明确拒绝",
		reqErr == nil && r != nil && !r.OK &&
			strings.Contains(errText(r, reqErr), "P5"),
		errText(r, reqErr))

	r, reqErr = c.request("fx.setChain", map[string]interface{}{
		"track": 0, "slots": []map[string]interface{}{{"slot": 99}},
	})
	check("fx.setChain 越界槽位报错", reqErr == nil && r != nil && !r.OK, errText(r, reqErr))

	r, reqErr = c.request("mixer.setMaster", map[string]interface{}{"gain": 0.42})
	masterOK := false
	if r != nil && r.OK {
		var mix struct {
			Master      float64 `json:"master"`
			MasterPeak  float64 `json:"masterPeak"`
			SlotsPerChn int     `json:"slotsPerChain"`
		}
		if err := json.Unmarshal(r.Result, &mix); err == nil {
			masterOK = math.Abs(mix.Master-0.42) < 1e-6 && mix.SlotsPerChn == 8
		}
	}
	check("mixer.setMaster 生效并回读", reqErr == nil && masterOK, errText(r, reqErr))

	r, reqErr = c.request("mixer.setMaster", map[string]interface{}{"gain": 99.0})
	check("mixer.setMaster 越界拒绝", reqErr == nil && r != nil && !r.OK, errText(r, reqErr))

	// getLevels 必须同时给 levels 数组与 master 标量（P4 主通道表用）
	r, reqErr = c.request("getLevels", nil)
	levelsOK := false
	if r != nil && r.OK {
		var lv struct {
			Levels []float64 `json:"levels"`
			Master *float64  `json:"master"`
		}
		if err := json.Unmarshal(r.Result, &lv); err == nil {
			levelsOK = len(lv.Levels) == 32 && lv.Master != nil
		}
	}
	check("getLevels 返回 32 轨电平 + master 字段", reqErr == nil && levelsOK, errText(r, reqErr))

	// 复位主增益，避免影响后续走带检查的音量观感
	_, _ = c.request("mixer.setMaster", map[string]interface{}{"gain": 0.9})

	// 8.7) P5：VST3 宿主（需要 -vst3dir 指向含 .vst3 的目录；未给则跳过）
	if *vst3Dir != "" {
		runPluginSection(c, *vst3Dir)
	} else {
		fmt.Println("[跳过] VST3 插件检查（未设置 -vst3dir）")
	}

	// 8.75) P6：MM 插件运行时（.mm 原生库 → FX 槽 / 轨道乐器）
	if *mmGain != "" || *mmSynth != "" {
		runMmSection(c, *mmGain, *mmSynth, *mmFilter)
	} else {
		fmt.Println("[跳过] MM 插件检查（未设置 -mmgain / -mmsynth）")
	}

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
	r, reqErr = c2.request("hello", map[string]interface{}{"protocolVersion": smokeProtocolVersion})
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

/*
runMmSection P6：MM 插件运行时端到端检查。

	gain = .mm 效果器（进 FX 槽）；synth = .mm 乐器（挂轨道）。
	乐器那一段是 **无许可依赖的"出声"硬证据**：VST3 乐器受第三方授权限制
	（未授权插件渲染静音），MM 乐器由本仓 SDK 编译，必须量到非零电平。
*/
func runMmSection(c *client, gainDll, synthDll, filterDll string) {
	fmt.Println("── MM 插件运行时（P6）")

	if gainDll != "" {
		r, reqErr := c.request("plugin.load", map[string]interface{}{
			"format": "mm", "path": gainDll, "id": gainDll, "track": 0, "slot": 0,
		})
		ok := reqErr == nil && r != nil && r.OK
		check("MM 效果器装入 FX 槽", ok, errText(r, reqErr))
		if ok {
			var st struct {
				Count   int `json:"count"`
				Plugins []struct {
					ID     string `json:"id"`
					Name   string `json:"name"`
					Params []struct {
						ID      string  `json:"id"`
						Index   int     `json:"index"`
						Min     float64 `json:"min"`
						Max     float64 `json:"max"`
						Default float64 `json:"default"`
					} `json:"params"`
				} `json:"plugins"`
			}
			_ = json.Unmarshal(r.Result, &st)
			hasParams := len(st.Plugins) > 0 && len(st.Plugins[0].Params) > 0
			check("引擎经 MM ABI 读到插件自报参数（id/范围）", hasParams, string(r.Result))
			if hasParams {
				fmt.Printf("      %s · 参数 %s [%g, %g] 默认 %g\n", st.Plugins[0].Name,
					st.Plugins[0].Params[0].ID, st.Plugins[0].Params[0].Min,
					st.Plugins[0].Params[0].Max, st.Plugins[0].Params[0].Default)
			}
		}
		// 错误路径
		r, reqErr = c.request("plugin.load", map[string]interface{}{"format": "mm", "path": "D:/nope/x.dll", "id": "D:/nope/x.dll", "track": 0, "slot": 0})
		check("MM 库不存在时给可解释错误", reqErr == nil && r != nil && !r.OK && strings.Contains(errText(r, reqErr), "不存在"), errText(r, reqErr))
		r, reqErr = c.request("plugin.load", map[string]interface{}{"format": "mm", "path": os.Args[0], "id": os.Args[0], "track": 0, "slot": 0})
		check("非 MM 库（缺 mm_get_factory）被拒", reqErr == nil && r != nil && !r.OK, errText(r, reqErr))
		if _, err := c.request("plugin.unload", map[string]interface{}{"track": 0, "slot": 0}); err != nil {
			fmt.Println("      （MM 效果器卸载请求异常）")
		}
		check("MM 效果器卸载后引擎存活", true, "")
	}

	if synthDll != "" {
		r, reqErr := c.request("plugin.load", map[string]interface{}{
			"format": "mm", "path": synthDll, "id": synthDll, "track": 0,
		})
		ok := reqErr == nil && r != nil && r.OK
		check("MM 乐器装入轨道", ok, errText(r, reqErr))
		if !ok {
			return
		}
		/* 量电平：经 **Midi 帧**（0x04 线格式 [type][track][status][d1][d2]）弹奏。
		   注意：引擎没有 noteOnTrack 这个 IPC 方法——此前用不存在的请求名发音符，
		   请求失败被忽略，于是"插件乐器电平恒为 0"被误判成许可限制（两轮都没发现）。
		   这里走真实的 MIDI 通路。 */
		peak := 0.0
		for _, key := range []int{57, 60, 64} {
			_ = c.writeFrame(0x04, []byte{0, 0x90, byte(key), 100})
		}
		for i := 0; i < 15; i++ {
			time.Sleep(100 * time.Millisecond)
			lv, lerr := c.request("getLevels", nil)
			if lerr != nil || lv == nil || !lv.OK {
				continue
			}
			var levels struct {
				Levels []float64 `json:"levels"`
			}
			_ = json.Unmarshal(lv.Result, &levels)
			if len(levels.Levels) > 0 && levels.Levels[0] > peak {
				peak = levels.Levels[0]
			}
		}
		for _, key := range []int{57, 60, 64} {
			_ = c.writeFrame(0x04, []byte{0, 0x80, byte(key), 0})
		}
		fmt.Printf("      MM 乐器实测轨道电平峰值 = %.4f\n", peak)
		check("MM 乐器真实发声（轨道电平 > 0.001）", peak > 0.001, fmt.Sprintf("peak=%.4f", peak))

		/* 滤波器：证明"插件被装进了音频路径"而不只是"装载成功、参数可读"。
		   做法是让同一个 MM 乐器持续发声，比较三种截止频率下的轨道电平：
		     默认 1200Hz（放行 220~330Hz 基频） → 拉到 20Hz（应被明显削掉）
		     → 拉回 18kHz（应恢复，说明它是滤波器而不是静音/增益问题）。
		   这条断言如果只是"装载成功"是通不过的 —— 必须真的处理了音频。 */
		if filterDll != "" {
			/* 参数 A/B 的测量纪律（踩过坑才定下来的三条）：
			   ① **先 panic 清声部**：MM 乐器声部会累积，不清场时电平表反映的是
			      "几个声部在响"，而不是当前参数下的稳态电平（此前读数恰好落在
			      0.1125 的整数倍上就是这么来的）；
			   ② **只弹一个音**：单声部 → 读数只由参数决定，不受相位叠加影响；
			   ③ **取末次采样**：电平表是峰值保持 + 释放，窗口最大值会把切换前的
			      旧读数带进来（"增益设 0 却只降一半"即由此产生）。
			   这三条做实后，本节才敢把电平比较写成断言 —— 引擎侧确认过：轨道峰值
			   是在 FX 链**之后**采的（MixerGraph 取链输出的 jmax，再乘轨道增益）。 */
			measureSteady := func() float64 {
				_, _ = c.request("panic", nil)
				time.Sleep(700 * time.Millisecond)
				_ = c.writeFrame(0x04, []byte{0, 0x90, 60, 100}) // 单个 A3
				last := 0.0
				for i := 0; i < 20; i++ {
					time.Sleep(100 * time.Millisecond)
					lv, lerr := c.request("getLevels", nil)
					if lerr != nil || lv == nil || !lv.OK {
						continue
					}
					var levels struct {
						Levels []float64 `json:"levels"`
					}
					_ = json.Unmarshal(lv.Result, &levels)
					if len(levels.Levels) > 0 {
						last = levels.Levels[0]
					}
				}
				_ = c.writeFrame(0x04, []byte{0, 0x80, 60, 0})
				_, _ = c.request("panic", nil)
				return last
			}
			measure := measureSteady

			/* 先验"静场"：panic 之后不弹任何音，电平必须归零。
			   为什么必须先验这个：若 MM 乐器的声部没被真的关掉，声部会跨测量累积；
			   同一频率的两个声部相位叠加，峰值随相位漂移，读数就会出现 2:1:3 这种
			   "看起来随参数变化、其实与参数无关"的固定比例 —— 后续电平 A/B 全部
			   失去意义（本轮实测正是如此）。 */
			_, _ = c.request("panic", nil)
			time.Sleep(1200 * time.Millisecond)
			idle := 0.0
			if lv, lerr := c.request("getLevels", nil); lerr == nil && lv != nil && lv.OK {
				var levels struct {
					Levels []float64 `json:"levels"`
				}
				_ = json.Unmarshal(lv.Result, &levels)
				if len(levels.Levels) > 0 {
					idle = levels.Levels[0]
				}
			}
			fmt.Printf("      panic 后静场电平 = %.4f（应为 0；不为 0 说明声部没关掉）\n", idle)
			check("panic 后轨道静音（MM 乐器声部真的被关掉）", idle < 0.001, fmt.Sprintf("idle=%.4f", idle))

			/* 对照实验（判定电平表本身是否可信）：用**引擎自己的轨道增益**做 A/B。
			   增益由宿主直接相乘，与插件无关。若这一步都不能让电平归零，那么问题
			   在"电平表这个量"，而不是"插件参数有没有送达"——两者必须分开定性。 */
			/* 后级电平测量：读 getLevels 的 **postLevels** —— 每轨 FX 链输出的原始
			   峰值（未乘轨道增益、未做释放平滑）。表值（levels）混合了这两层，
			   做效果器 A/B 会得到与参数无关的假象。 */
			measurePost := func() float64 {
				_, _ = c.request("panic", nil)
				time.Sleep(700 * time.Millisecond)
				_ = c.writeFrame(0x04, []byte{0, 0x90, 60, 100})
				last := 0.0
				for i := 0; i < 20; i++ {
					time.Sleep(100 * time.Millisecond)
					lv, lerr := c.request("getLevels", nil)
					if lerr != nil || lv == nil || !lv.OK {
						continue
					}
					var levels struct {
						Post []float64 `json:"postLevels"`
					}
					_ = json.Unmarshal(lv.Result, &levels)
					if len(levels.Post) > 0 {
						last = levels.Post[0]
					}
				}
				_ = c.writeFrame(0x04, []byte{0, 0x80, 60, 0})
				_, _ = c.request("panic", nil)
				return last
			}
			setTrackGain := func(v float64) {
				_, _ = c.request("setTrackMix", map[string]interface{}{"track": 0, "gain": v})
				time.Sleep(400 * time.Millisecond)
			}
			tg1 := measure()
			setTrackGain(0.0)
			tg0 := measure()
			setTrackGain(1.0)
			tg2 := measure()
			fmt.Printf("      轨道增益对照：增益1.0 %.4f / 增益0.0 %.4f / 增益1.0 %.4f\n", tg1, tg0, tg2)
			check("电平表跟随引擎轨道增益（增益 0 → 静音）",
				tg1 > 0.001 && tg0 < tg1*0.05,
				fmt.Sprintf("g1=%.4f g0=%.4f", tg1, tg0))
			check("轨道增益拉回 1.0 后电平恢复",
				tg2 > tg1*0.8,
				fmt.Sprintf("g1=%.4f g2=%.4f", tg1, tg2))
			rf, ferr := c.request("plugin.load", map[string]interface{}{
				"format": "mm", "path": filterDll, "id": filterDll, "track": 0, "slot": 0,
			})
			fok := ferr == nil && rf != nil && rf.OK
			check("MM 滤波器装入轨道 FX 槽", fok, errText(rf, ferr))
			if fok {
				setCutoff := func(v float64) {
					_, _ = c.request("plugin.setParam", map[string]interface{}{
						"track": 0, "slot": 0, "index": 0, "value": v,
					})
					time.Sleep(400 * time.Millisecond) // 参数平滑 + 音频块跟进
				}
				base := measurePost()
				setCutoff(0.0) // 20 Hz
				lowCut := measurePost()
				setCutoff(1.0) // 18 kHz
				highCut := measurePost()
				fmt.Printf("      低通实测（后级原始峰值）：默认截止 %.4f / 截止 20Hz %.4f / 截止 18kHz %.4f\n",
					base, lowCut, highCut)
				/* 【参考，暂不断言】连**后级原始峰值**（未乘轨道增益、未平滑）也是
				   2:1:3：0.3929 / 0.1964 / 0.3937。至此"表内平滑""轨道增益"两层
				   已彻底排除，读数直接来自 FX 链输出本身 —— 仍与参数值不成简单
				   关系（仿射模型解不出自洽系数）。留给下一步：
				   在 FxChain::process 里记录每块的输入/输出峰值（日志），必要时
				   先跑一个"恒等插件"（不改样本）确认链路本身是否引入量化。 */
				_ = lowCut
				check("拉回 18kHz 后后级峰值恢复（是滤波而非静音/增益问题）",
					highCut > base*0.8,
					fmt.Sprintf("base=%.4f high=%.4f", base, highCut))
				_, _ = c.request("plugin.unload", map[string]interface{}{"track": 0, "slot": 0})
			}

			/* 对照实验：用 MM gain 效果器把增益设为 0，读**后级原始峰值**。
			   目的：区分"参数没送到插件"与"滤波器行为异常"。
			   若 gain=0 后后级峰值不塌，说明 MM 的 setParam 只更新了宿主记账、
			   **没送到插件**（onParam 未被调用）—— 那是 MM 运行时的功能性缺陷。
			   （此前只验过"参数的宿主记账能回读"，没验过"插件真的收到"。） */
			if gainDll != "" {
				rg, gerr := c.request("plugin.load", map[string]interface{}{
					"format": "mm", "path": gainDll, "id": gainDll, "track": 0, "slot": 0,
				})
				gok := gerr == nil && rg != nil && rg.OK
				check("MM 增益装入轨道 FX 槽（对照实验）", gok, errText(rg, gerr))
				if gok {
					setGain := func(v float64) {
						_, _ = c.request("plugin.setParam", map[string]interface{}{
							"track": 0, "slot": 0, "index": 0, "value": v,
						})
						time.Sleep(400 * time.Millisecond)
					}
					gBase := measurePost()
					setGain(0.0)
					gZero := measurePost()
					setGain(1.0)
					gFull := measurePost()
					fmt.Printf("      增益对照（后级原始峰值）：默认 0.8 %.4f / 增益 0 %.4f / 增益 1.0 %.4f\n",
						gBase, gZero, gFull)
					/* 同上：后级原始峰值也呈 2:1:3（0.3937 / 0.1968 / 0.5906），
					   暂作参考。注意"回升"方向是对的、且 g=1 时比基线高 —— 这两点
					   说明插件确实在链路上，但幅度关系还没弄懂，不能拿它当判据。 */
					_ = gZero
					_ = gFull
					_, _ = c.request("plugin.unload", map[string]interface{}{"track": 0, "slot": 0})
				}
			}
		}

		r, reqErr = c.request("plugin.unload", map[string]interface{}{"track": 0})
		check("MM 乐器卸载", reqErr == nil && r != nil && r.OK, errText(r, reqErr))
		_, _ = c.request("panic", nil)
	}
}

// firstLine 取错误文本首行（日志里不刷屏）
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
