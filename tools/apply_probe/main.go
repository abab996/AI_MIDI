// applySetup 崩溃复现探针：启动引擎（继承 stderr）→ hello →
// applySetup 指定驱动+不存在的设备 → 打印响应与进程存活状态。
// 用法：go run ./tools/apply_probe [driver] [device]
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func ioReadFull(f *os.File, buf []byte) error {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		if err != nil || n == 0 {
			return fmt.Errorf("read: %v (got %d/%d)", err, total, len(buf))
		}
		total += n
	}
	return nil
}

type response struct {
	ID     *float64        `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func writeFrame(f *os.File, typ byte, body []byte) error {
	frame := make([]byte, 4+1+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)+1))
	frame[4] = typ
	copy(frame[5:], body)
	_, err := f.Write(frame)
	return err
}

func main() {
	driver := "ASIO"
	device := "ASIO Link Pro"
	if len(os.Args) > 2 {
		driver, device = os.Args[1], os.Args[2]
	}

	cmd := exec.Command("bin/aimidi-engine.exe")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("SPAWN-FAIL:", err)
		os.Exit(1)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	pid := cmd.Process.Pid
	fmt.Printf("engine pid=%d\n", pid)

	name := fmt.Sprintf(`\\.\pipe\AI_MIDI_ENGINE_%d`, pid)
	var conn *os.File
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(name, os.O_RDWR, 0)
		if err == nil {
			conn = f
			break
		}
		if time.Now().After(deadline) {
			fmt.Println("DIAL-FAIL:", err)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer conn.Close()
	fmt.Println("CONNECTED")

	var nextID float64
	request := func(method string, params map[string]any) (*response, error) {
		nextID++
		id := nextID
		body, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		if err := writeFrame(conn, 0x01, body); err != nil {
			return nil, fmt.Errorf("write: %w", err)
		}
		// 串行读帧直到 id 匹配
		for {
			var lb [4]byte
			if err := ioReadFull(conn, lb[:]); err != nil {
				return nil, err
			}
			n := binary.LittleEndian.Uint32(lb[:])
			pl := make([]byte, n)
			if err := ioReadFull(conn, pl); err != nil {
				return nil, err
			}
			if pl[0] != 0x02 {
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

	r, err := request("hello", map[string]any{"protocolVersion": 1})
	fmt.Printf("hello: ok=%v err=%v\n", r != nil && r.OK, err)

	// 先看真实设备名（复现前的环境快照）
	if r, err = request("listDevices", nil); err == nil && r.OK {
		var ld struct {
			Drivers []struct {
				Driver  string `json:"driver"`
				Devices []struct {
					Name string `json:"name"`
				} `json:"devices"`
			} `json:"drivers"`
		}
		_ = json.Unmarshal(r.Result, &ld)
		for _, d := range ld.Drivers {
			fmt.Printf("  [驱动] %s:", d.Driver)
			for _, dev := range d.Devices {
				fmt.Printf(" %q", dev.Name)
			}
			fmt.Println()
		}
	}

	params := map[string]any{"driver": driver, "device": device}
	r, err = request("applySetup", params)
	if err != nil {
		fmt.Println("applySetup TRANSPORT-FAIL:", err)
	} else {
		msg := "ok"
		if !r.OK && r.Error != nil {
			msg = r.Error.Message
		}
		fmt.Printf("applySetup: ok=%v result=%s error=%s\n", r.OK, string(r.Result), msg)
	}

	// 进程是否还活着 + 3 秒后再探一次
	time.Sleep(3 * time.Second)
	r2, err2 := request("currentSummary", nil)
	if err2 != nil {
		fmt.Println("AFTER-3S: engine dead or pipe broken:", err2)
	} else {
		fmt.Printf("AFTER-3S summary: %s\n", string(r2.Result))
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case werr := <-waitCh:
		fmt.Println("ENGINE-EXITED:", werr)
	case <-time.After(time.Second):
		fmt.Println("ENGINE-ALIVE")
	}
}
