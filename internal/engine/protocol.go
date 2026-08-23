// Package engine 提供原生音频引擎子进程（aimidi-engine.exe）的接入层：
// 进程守护、命名管道客户端、协议编解码。
//
// 协议权威文档：docs/引擎IPC协议.md（v1）。
// 铁律：音频样本永远不进 Go 进程——本包只发命令、收事件与元数据。
package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 消息类型（payload 首字节），与引擎侧 Protocol.h 镜像
const (
	MsgRequest  byte = 0x01 // Go -> Engine { id, method, params }
	MsgResponse byte = 0x02 // Engine -> Go { id, ok, result | error }
	MsgEvent    byte = 0x03 // Engine -> Go { event, data }
	MsgMidi     byte = 0x04 // 预留（M2）
	MsgTimecode byte = 0x05 // 预留（M2/M3）
)

// ProtocolVersion 协议版本（握手双向校验）
const ProtocolVersion uint32 = 1

// MaxFrameSize 单帧上限（防异常长度）
const MaxFrameSize = 16 * 1024 * 1024

// ErrProtocol 协议层错误（帧损坏、类型未知等，通常需要重连）
var ErrProtocol = errors.New("engine protocol error")

// EncodeFrame 将一帧编码为线格式：[uint32 LE 总长][类型字节][payload]
func EncodeFrame(msgType byte, payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(payload)+1))
	out[4] = msgType
	copy(out[5:], payload)
	return out
}

// Frame 解码结果
type Frame struct {
	Type    byte
	Payload []byte
}

// DecodeFrame 从缓冲区起始解析一帧。返回帧与消费的字节数；
// 数据不足时返回 (nil, 0, nil)，由调用方继续读；非法长度返回错误。
func DecodeFrame(buf []byte) (*Frame, int, error) {
	if len(buf) < 4 {
		return nil, 0, nil
	}
	total := int(binary.LittleEndian.Uint32(buf[:4]))
	if total <= 0 || total > MaxFrameSize {
		return nil, 0, fmt.Errorf("%w: 非法帧长度 %d", ErrProtocol, total)
	}
	if len(buf) < 4+total {
		return nil, 0, nil
	}
	payload := make([]byte, total)
	copy(payload, buf[4:4+total])
	return &Frame{Type: payload[0], Payload: payload[1:]}, 4 + total, nil
}
