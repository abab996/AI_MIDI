// 最小往返探针：连接引擎管道 → 发送 hello → 带超时读取响应
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"
)

func main() {
	name := fmt.Sprintf(`\\.\pipe\AI_MIDI_ENGINE_%s`, os.Args[1])
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		fmt.Println("CONNECT-FAIL:", err)
		os.Exit(1)
	}
	defer f.Close()
	fmt.Println("CONNECTED")

	payload := []byte(`{"id":1,"method":"hello","params":{"protocolVersion":1}}`)
	frame := make([]byte, 0, 5+len(payload))
	var head [4]byte
	binary.LittleEndian.PutUint32(head[:], uint32(len(payload)+1))
	frame = append(frame, head[:]...)
	frame = append(frame, 0x01)
	frame = append(frame, payload...)

	f.SetWriteDeadline(time.Now().Add(3 * time.Second))
	n, err := f.Write(frame)
	fmt.Println("WRITE:", n, "bytes, err:", err)
	if err != nil {
		os.Exit(1)
	}

	buf := make([]byte, 1024)
	f.SetReadDeadline(time.Now().Add(3 * time.Second))
	m, err := f.Read(buf)
	fmt.Println("READ:", m, "bytes, err:", err)
	if m >= 5 {
		fmt.Println("RESPONSE:", string(buf[5:m]))
	}
}
