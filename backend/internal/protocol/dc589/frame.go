// Package dc589 实现厂商 5.8.9 的二进制 TCP 分帧协议。
// 线上格式依次为 EE、LEN、CMD、六个 session 字节、payload、XOR 校验和。
package dc589

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// StartByte 是 DC589 帧的起始字节 0xEE。
const StartByte byte = 0xEE

// HeaderSize 是 LEN 计入的固定 8 字节：命令字、六字节会话和校验和。
const HeaderSize = 8 // CMD + 六字节 session + 校验和，都计入 LEN

var (
	// ErrFrame 表示起始字节错误或帧过短。
	ErrFrame = errors.New("invalid 5.8.9 frame")
	// ErrLength 表示 LEN 与实际长度不符，或载荷超过长度限制。
	ErrLength = errors.New("invalid 5.8.9 length")
	// ErrSum 表示 XOR 校验失败。
	ErrSum = errors.New("invalid 5.8.9 XOR checksum")
)

// Frame 保存命令字、六字节会话和载荷，供编解码使用。
type Frame struct {
	Command byte
	Session [6]byte
	Data    []byte
}

// Encode 添加起始字节、长度和 XOR 校验；载荷最多 247 字节。
func Encode(frame Frame) ([]byte, error) {
	length := HeaderSize + len(frame.Data)
	if length > 255 {
		return nil, ErrLength
	}
	out := make([]byte, 2+length)
	out[0], out[1], out[2] = StartByte, byte(length), frame.Command
	copy(out[3:9], frame.Session[:])
	copy(out[9:len(out)-1], frame.Data)
	out[len(out)-1] = checksum(out[1 : len(out)-1])
	return out, nil
}

// Decode 检查起始字节、长度和 XOR 校验，返回独立的载荷副本。
func Decode(raw []byte) (Frame, error) {
	if len(raw) < 2+HeaderSize || raw[0] != StartByte {
		return Frame{}, ErrFrame
	}
	if raw[1] < HeaderSize || len(raw) != 2+int(raw[1]) {
		return Frame{}, ErrLength
	}
	if checksum(raw[1:len(raw)-1]) != raw[len(raw)-1] {
		return Frame{}, ErrSum
	}
	var session [6]byte
	copy(session[:], raw[3:9])
	return Frame{Command: raw[2], Session: session, Data: bytes.Clone(raw[9 : len(raw)-1])}, nil
}

// ReadFrame 从 TCP 字节流读取完整帧，不依赖网络包边界。
// 畸形帧直接返回错误，不尝试为未认证设备扫描并恢复分帧。
func ReadFrame(reader *bufio.Reader) (Frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(reader, head[:]); err != nil {
		return Frame{}, err
	}
	if head[0] != StartByte {
		return Frame{}, ErrFrame
	}
	if head[1] < HeaderSize {
		return Frame{}, ErrLength
	}
	raw := make([]byte, 2+int(head[1]))
	copy(raw[:2], head[:])
	if _, err := io.ReadFull(reader, raw[2:]); err != nil {
		return Frame{}, fmt.Errorf("read 5.8.9 frame: %w", err)
	}
	return Decode(raw)
}

func checksum(payload []byte) byte {
	var sum byte
	for _, b := range payload {
		sum ^= b
	}
	return sum
}
