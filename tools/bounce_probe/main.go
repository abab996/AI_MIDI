// 一次性验证脚本：bounce tracks 路径 33 轨越界防护（修复前引擎进程崩溃）
// 用法：go run ./tools/bounce_probe [-engine bin/aimidi-engine.exe]
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
	msgRequest = 0x01
	ioTimeout  = 60 * time.Second
)

type response struct {
	ID     *float64        `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type client struct {
	conn   *os.File
	nextID float64
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

func (c *client) readFrame() ([]byte, error) {
	res := make(chan struct {
		b   []byte
		err error
	}, 1)
	go func() {
		var lb [4]byte
		if _, err := ioReadFull(c.conn, lb[:]); err != nil {
			res <- struct {
				b   []byte
				err error
			}{nil, err}
			return
		}
		n := binary.LittleEndian.Uint32(lb[:])
		pl := make([]byte, n)
		if _, err := ioReadFull(c.conn, pl); err != nil {
			res <- struct {
				b   []byte
				err error
			}{nil, err}
			return
		}
		res <- struct {
			b   []byte
			err error
		}{pl, nil}
	}()
	select {
	case r := <-res:
		return r.b, r.err
	case <-time.After(ioTimeout):
		return nil, fmt.Errorf("读取超时")
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

func (c *client) request(method string, params map[string]any) (*response, error) {
	c.nextID++
	id := c.nextID
	body, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.writeFrame(msgRequest, body); err != nil {
		return nil, err
	}
	for {
		pl, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		if len(pl) == 0 || pl[0] != 0x02 {
			continue
		}
		var r response
		if err := json.Unmarshal(pl[1:], &r); err != nil {
			return nil, err
		}
		if r.ID != nil && *r.ID == id {
			return &r, nil
		}
	}
}

func main() {
	enginePath := flag.String("engine", "bin/aimidi-engine.exe", "引擎可执行文件路径")
	flag.Parse()

	cmd := exec.Command(*enginePath)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("[失败] 启动引擎:", err)
		os.Exit(1)
	}
	defer func() { _ = cmd.Process.Kill() }()
	pipeName := fmt.Sprintf(`\\.\pipe\AI_MIDI_ENGINE_%d`, cmd.Process.Pid)

	var conn *os.File
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for conn == nil {
		conn, err = os.OpenFile(pipeName, os.O_RDWR, 0)
		if err != nil {
			if time.Now().After(deadline) {
				fmt.Println("[失败] 连接管道超时:", err)
				os.Exit(1)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	c := &client{conn: conn}

	if r, err := c.request("hello", map[string]any{"protocolVersion": 1}); err != nil || !r.OK {
		fmt.Println("[失败] 握手:", err)
		os.Exit(1)
	}

	// 33 轨（超过引擎 32 轨上限）+ 末轨带 midi clip：修复前 tmpBufs[32]
	// 越界 → 引擎进程崩溃（管道 EOF）。修复后应正常返回（33 轨被跳过）
	tracks := make([]any, 0, 33)
	for i := 0; i < 33; i++ {
		clipType := "midi"
		tracks = append(tracks, map[string]any{
			"clips": []any{map[string]any{
				"type":   clipType,
				"start":  0.0,
				"length": 4.0,
				"notes": []any{map[string]any{
					"note": "C4", "start": 0.0, "end": 2.0, "velocity": 100,
				}},
			}},
		})
	}
	r, err := c.request("bounce", map[string]any{
		"bpm": 120.0, "tracks": tracks, "tailSec": 0.1,
		"path": "output/bounce_probe_test.wav",
	})
	if err != nil {
		fmt.Println("[失败] 33 轨 bounce 请求失败（引擎可能已崩溃）:", err)
		os.Exit(1)
	}
	if !r.OK {
		fmt.Println("[失败] 33 轨 bounce 被拒:", r.Error)
		os.Exit(1)
	}
	fmt.Println("[通过] 33 轨 bounce 未崩溃，path =", string(r.Result)[:min(80, len(r.Result))])

	// 降号解析：Eb4 不再静默变 C4（无音色时听不出差别，此处仅验证不报错）
	r2, err := c.request("bounce", map[string]any{
		"bpm": 120.0, "beats": 2.0, "tailSec": 0.1,
		"notes": []any{map[string]any{"note": "Eb4", "start": 0.0, "end": 1.0, "track": 0, "vel": 100}},
		"path":  "output/bounce_probe_flat.wav",
	})
	if err != nil || !r2.OK {
		fmt.Println("[失败] 降号音符 bounce:", err)
		os.Exit(1)
	}
	fmt.Println("[通过] 降号音符（Eb4）bounce 正常")

	_, _ = c.request("shutdown", nil)
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-waitCh:
		fmt.Println("[通过] 引擎优雅退出")
	case <-time.After(5 * time.Second):
		fmt.Println("[失败] 引擎未退出")
		os.Exit(1)
	}
	fmt.Println("bounce 越界探测全部通过 ✅")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
