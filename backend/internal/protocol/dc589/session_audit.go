package dc589

import (
	"sync"
	"sync/atomic"
	"time"

	"wattharbor/internal/protocol"
)

// Transport 是该适配器使用的传输方式，取值见 protocol.Transport。
type Transport = protocol.Transport

const (
	// TransportTCP 表示设备通过 TCP 连接。
	TransportTCP = protocol.TransportTCP
	// TransportMQTT 表示设备通过 MQTT 连接。
	TransportMQTT = protocol.TransportMQTT
)

// SessionAudit 在内存中累计连接流量，连接结束时由适配器保存。
// 进程异常退出会丢失尚未保存的记录。
type SessionAudit struct {
	transport  Transport
	remoteAddr string
	startedAt  time.Time
	sessionID  string

	bytesIn   atomic.Int64
	bytesOut  atomic.Int64
	framesIn  atomic.Int64
	framesOut atomic.Int64
	lastSeen  atomic.Int64 // 最近一帧的 UnixNano
	// unknown 使用定长命令集合，限制未知命令审计数据的大小。
	unknownMu sync.Mutex
	unknown   [256]bool
}

// NewSessionAudit 开始记录连接；立即保存对端地址，供连接关闭后的审计写入使用。
func NewSessionAudit(transport Transport, sessionID, remoteAddr string, startedAt time.Time) *SessionAudit {
	audit := &SessionAudit{
		transport:  transport,
		remoteAddr: remoteAddr,
		startedAt:  startedAt,
		sessionID:  sessionID,
	}
	audit.lastSeen.Store(startedAt.UnixNano())
	return audit
}

// Inbound 记录接收帧及载荷字节数；dataLen 不含帧头。
func (a *SessionAudit) Inbound(dataLen int, at time.Time) {
	if a == nil {
		return
	}
	a.bytesIn.Add(int64(dataLen))
	a.framesIn.Add(1)
	a.lastSeen.Store(at.UnixNano())
}

// Outbound 记录发送帧数、载荷字节数和最近活动时间。
func (a *SessionAudit) Outbound(dataLen int, at time.Time) {
	if a == nil {
		return
	}
	a.bytesOut.Add(int64(dataLen))
	a.framesOut.Add(1)
	a.lastSeen.Store(at.UnixNano())
}

// Unknown 记录收到但未处理的命令集合，用于识别超出实现范围的设备上报。
func (a *SessionAudit) Unknown(command byte) {
	if a == nil {
		return
	}
	a.unknownMu.Lock()
	a.unknown[command] = true
	a.unknownMu.Unlock()
}

// UnknownCommands 按升序返回去重后的未知命令字节。
func (a *SessionAudit) UnknownCommands() []int {
	if a == nil {
		return nil
	}
	a.unknownMu.Lock()
	defer a.unknownMu.Unlock()
	var out []int
	for i, seen := range a.unknown {
		if seen {
			out = append(out, i)
		}
	}
	return out
}

// Snapshot 汇总流量及连接起止时间，结束原因由调用方提供。
// closeReason 是 device_session.close_reason 的同一词表（VARCHAR 开放集合），
// 未知取值原值透传，不在此处校验或替换。
func (a *SessionAudit) Snapshot(deviceID string, closeReason protocol.CloseReason, endedAt time.Time) protocol.SessionRecord {
	if a == nil {
		return protocol.SessionRecord{}
	}
	last := time.Unix(0, a.lastSeen.Load()).UTC()
	return protocol.SessionRecord{
		SessionID:   a.sessionID,
		DeviceID:    deviceID,
		Transport:   a.transport,
		RemoteAddr:  a.remoteAddr,
		StartedAt:   a.startedAt.UTC(),
		LastActive:  last,
		EndedAt:     endedAt.UTC(),
		CloseReason: closeReason,
		BytesIn:     a.bytesIn.Load(),
		BytesOut:    a.bytesOut.Load(),
		FramesIn:    a.framesIn.Load(),
		FramesOut:   a.framesOut.Load(),
	}
}
