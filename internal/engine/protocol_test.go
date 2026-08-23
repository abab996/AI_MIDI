package engine

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	payload := []byte(`{"id":1,"method":"ping","params":{}}`)
	frame := EncodeFrame(MsgRequest, payload)

	got, consumed, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if consumed != len(frame) {
		t.Fatalf("消费字节数不符: got %d want %d", consumed, len(frame))
	}
	if got.Type != MsgRequest || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("往返不一致: type=%d payload=%q", got.Type, got.Payload)
	}
}

func TestDecodePartialBuffer(t *testing.T) {
	payload := []byte(`{"a":1}`)
	frame := EncodeFrame(MsgEvent, payload)

	// 逐字节喂入：数据不足时必须返回 (nil,0,nil) 而非错误
	var acc []byte
	var last *Frame
	for i, b := range frame {
		acc = append(acc, b)
		f, n, err := DecodeFrame(acc)
		if err != nil {
			t.Fatalf("第 %d 字节时意外报错: %v", i+1, err)
		}
		if f == nil && n != 0 {
			t.Fatalf("不完整帧却消耗了 %d 字节", n)
		}
		if f != nil {
			last = f
			if n != len(frame) {
				t.Fatalf("完整帧消费字节数不符: %d", n)
			}
		}
	}
	if last == nil || last.Type != MsgEvent {
		t.Fatal("流式解码未得到最终帧")
	}
}

func TestDecodeRejectsBadLength(t *testing.T) {
	// 声明 256MiB（> MaxFrameSize 16MiB）的长度 → 必须报错而非挂起
	bad := []byte{0x00, 0x00, 0x00, 0x10, 0x01}
	if _, _, err := DecodeFrame(bad); err == nil {
		t.Fatal("非法帧长度未被拒绝")
	}

	// 零长度帧同样拒绝
	zero := []byte{0x00, 0x00, 0x00, 0x00}
	if _, _, err := DecodeFrame(zero); err == nil {
		t.Fatal("零长度帧未被拒绝")
	}
}

// 协议契约：载荷为不含结尾 NUL 的纯 UTF-8 JSON（引擎侧 writeJson 已按
// sizeInBytes()-1 发送）。此处锁定线格式无隐藏终止符。
func TestFramePayloadHasNoTrailingNul(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"ok": true})
	frame := EncodeFrame(MsgResponse, payload)
	n := int(binaryLittleEndian(frame[:4]))
	if n != len(payload)+1 {
		t.Fatalf("长度前缀不符: %d", n)
	}
	if bytes.HasSuffix(frame[5:], []byte{0}) {
		t.Fatal("载荷含结尾 NUL，违反协议约定")
	}
}

func binaryLittleEndian(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// 守护器默认请求超时需覆盖 listDevices 冷启动（ASIO 枚举实测可达数秒）
func TestSupervisorDefaultRequestTimeout(t *testing.T) {
	sup := NewSupervisor(Config{}, AudioSettings{})
	if sup.cfg.RequestTimeout < 15*time.Second {
		t.Fatalf("默认请求超时过短: %v", sup.cfg.RequestTimeout)
	}
}
