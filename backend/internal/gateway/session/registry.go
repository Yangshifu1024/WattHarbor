// Package session 登记设备当前连接，支持替换旧连接和按设备发送命令。
package session

import (
	"context"
	"errors"
	"sync"

	"chargepilot/internal/protocol"
)

// ErrOffline 设备当前没有已登记连接。
var ErrOffline = errors.New("device is not connected")

// Registry 保存设备当前已认证的连接，将命令发送到该连接。
// 新连接替换旧连接；旧连接的断开回调不会移除新连接。
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

// Send 通过设备当前连接发送命令；无连接时返回 ErrOffline。
func (r *Registry) Send(ctx context.Context, deviceID string, command protocol.Command) error {
	r.mu.RLock()
	entry := r.sessions[deviceID]
	r.mu.RUnlock()
	if entry == nil {
		return ErrOffline
	}
	return entry.session.Send(ctx, command)
}

// Connected 检查设备是否已有登记连接，不探测网络连接是否仍可用。
func (r *Registry) Connected(deviceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[deviceID] != nil
}

// Registry 同时是适配器依赖的会话登记入口和运行时的命令下发器。
// 两条断言检查 Registry 是否同时实现设备发送和连接关闭接口。
var (
	_ protocol.SessionRouter = (*Registry)(nil)
	_ protocol.CommandSender = (*Registry)(nil)
)
