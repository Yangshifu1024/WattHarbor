package session

import (
	"context"
	"errors"
	"sync"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

var ErrOffline = errors.New("device is not connected")

// Registry 将命令路由至设备当前已认证的连接。
// 新连接替换旧会话；旧会话的延迟断开回调不会移除新连接。
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*registryEntry
}

type registryEntry struct{ session protocol.Session }

// Attach 登记设备当前会话并返回 detach 函数。
// 新会话替换并关闭同设备的旧连接，通过可选 onReplaced 回调记录替换原因。
func (r *Registry) Attach(deviceID string, session protocol.Session, onReplaced func(protocol.Session)) func() {
	r.mu.Lock()
	if r.sessions == nil {
		r.sessions = make(map[string]*registryEntry)
	}
	previous := r.sessions[deviceID]
	current := &registryEntry{session: session}
	r.sessions[deviceID] = current
	r.mu.Unlock()
	if previous != nil {
		if onReplaced != nil {
			onReplaced(previous.session)
		}
		_ = previous.session.Close()
	}
	return func() {
		r.mu.Lock()
		if r.sessions[deviceID] == current {
			delete(r.sessions, deviceID)
		}
		r.mu.Unlock()
	}
}

func (r *Registry) Send(ctx context.Context, deviceID string, command protocol.Command) error {
	r.mu.RLock()
	entry := r.sessions[deviceID]
	r.mu.RUnlock()
	if entry == nil {
		return ErrOffline
	}
	return entry.session.Send(ctx, command)
}

func (r *Registry) Connected(deviceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[deviceID] != nil
}

// Registry 同时是适配器依赖的会话登记入口和运行时的命令下发器。
// 两条断言在编译期锁定这两个形状，避免实现漂移后才发现不满足。
var (
	_ protocol.SessionRouter = (*Registry)(nil)
	_ protocol.CommandSender = (*Registry)(nil)
)
