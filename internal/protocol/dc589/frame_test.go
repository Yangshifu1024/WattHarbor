package dc589

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestDocumentedStopFrame(t *testing.T) {
	raw, err := hex.DecodeString("EE09B900000000000005B5")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := Decode(raw)
	if err != nil || frame.Command != StopCharge || !bytes.Equal(frame.Data, []byte{5}) {
		t.Fatal(frame, err)
	}
	encoded, err := Encode(frame)
	if err != nil || !bytes.Equal(encoded, raw) {
		t.Fatal(hex.EncodeToString(encoded), err)
	}
	read, err := ReadFrame(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil || read.Command != StopCharge {
		t.Fatal(read, err)
	}
}

func TestDocumentedRegistrationFrame(t *testing.T) {
	raw, err := hex.DecodeString("EE35A00000000000005348240514082652534831304850303153483130535030326B000863121077410587898604041018C0013999111E")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := ParseRegistration(frame)
	if err != nil {
		t.Fatal(err)
	}
	if registration.BoardID != "5348240514082652" || registration.SoftwareVersion != 107 || registration.HardwareVersion != "SH10HP01" {
		t.Fatal(registration)
	}
}

func TestFrameRejectsCorruptionAndBadLength(t *testing.T) {
	raw, _ := hex.DecodeString("EE09B900000000000005B5")
	raw[len(raw)-1] ^= 1
	if _, err := Decode(raw); !errors.Is(err, ErrSum) {
		t.Fatal(err)
	}
	raw[1] = 8
	if _, err := Decode(raw); !errors.Is(err, ErrLength) {
		t.Fatal(err)
	}
}

func TestStartCommandUsesDocumentedPayload(t *testing.T) {
	frame, err := BuildStart(StartCommand{Port: 5, Mode: ByTime, Quantity: 100})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != 0xEE || raw[1] != 0x1B || raw[2] != 0xB7 || raw[9] != 5 || raw[19] != 2 || raw[20] != 100 {
		t.Fatal(hex.EncodeToString(raw))
	}
}

// FuzzDecode 用随机字节流验证 Decode 的接受侧不变量与往返性质：
// 任何被接受的输入都必须满足起始符、长度域与 XOR 校验和约束，会话六字节
// 与负载逐字节取自原文，且重新编码必须还原出完全相同的字节序列。
// 拒绝侧的错误分类已由 TestFrameRejectsCorruptionAndBadLength 锁定，
// 模糊输入只需不 panic 且不产生畸形 Frame 即可。
func FuzzDecode(f *testing.F) {
	valid, err := hex.DecodeString("EE09B900000000000005B5")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(nil))
	f.Add([]byte{0xEE})
	f.Add([]byte{0xEE, 0x08, 0xB9, 1, 2, 3, 4, 5, 6, 0x00})
	f.Add([]byte{0xEE, 0x07, 0xB9, 1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte("EE-not-a-frame-but-long-enough-to-pass-trivial-rejections"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		frame, err := Decode(raw)
		if err != nil {
			return
		}
		if raw[0] != StartByte || len(raw) != 2+int(raw[1]) || checksum(raw[1:len(raw)-1]) != raw[len(raw)-1] {
			t.Fatalf("Decode 接受了不满足帧约束的输入 %x", raw)
		}
		if !bytes.Equal(frame.Session[:], raw[3:9]) {
			t.Fatalf("会话字节未被原样保留：%x", frame.Session)
		}
		if len(frame.Data) != len(raw)-2-HeaderSize {
			t.Fatalf("负载长度 %d 与帧长约束不符（帧长 %d）", len(frame.Data), len(raw))
		}
		encoded, err := Encode(frame)
		if err != nil || !bytes.Equal(encoded, raw) {
			t.Fatalf("编解码往返改变了字节：入 %x 出 %x 错误 %v", raw, encoded, err)
		}
	})
}
