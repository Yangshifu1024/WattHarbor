// Package session 登记设备当前连接，支持替换旧连接和按设备发送命令。
package session

import (
	"context"
	"errors"
	"slices"
	"sync"

	"chargepilot/internal/protocol"
)

// ErrOffline 设备当前没有已登记连接。
var ErrOffline = errors.New("device is not connected")

// Registry 保存设备当前已认证的连接，将命令发送到该连接。
// 新连接替换旧连接；旧连接的断开回调不会移除新连接。
type Registry struct {
	mu         sync.RWMutex
	sessions   map[string]*registryEntry
	operations map[string]*deviceOperation
}

type registryEntry struct{ session protocol.Session }

// deviceOperation 用许可串行化同设备的命令及占用变更，refs 包括执行者和等待者。
type deviceOperation struct {
	permit chan struct{}
	refs   int
}

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

// ConnectedDeviceIDs 返回当前登记设备编号的独立有序快照，供 SQL 在 LIMIT 前过滤离线任务。
// 快照不证明连接仍可写；执行时仍需检查 Connected/Send，不包含任何数据库或业务状态。
func (r *Registry) ConnectedDeviceIDs() []string {
	r.mu.RLock()
	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	slices.Sort(ids)
	return ids
}

// WithDeviceOperation 串行执行同设备的 run；不同设备不互相等待，等待许可时响应 ctx 取消。
// 命令保存、最后归属核对、发送、ACK 持久化和端口释放须共用本实例，防止旧 STOP 跨越端口复用。
// 回调同步执行且必须响应 ctx，不可嵌套获取同设备许可；Send 本身不取许可。
// 无数据库锁跨网络写入。回调返回或 panic 均释放许可，最后一个使用者退出后删除临时条目。
func (r *Registry) WithDeviceOperation(ctx context.Context, deviceID string, run func() error) error {
	if r == nil || deviceID == "" || run == nil {
		return errors.New("device operation is not configured")
	}
	r.mu.Lock()
	if r.operations == nil {
		r.operations = make(map[string]*deviceOperation)
	}
	op := r.operations[deviceID]
	if op == nil {
		op = &deviceOperation{permit: make(chan struct{}, 1)}
		op.permit <- struct{}{}
		r.operations[deviceID] = op
	}
	op.refs++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		op.refs--
		if op.refs == 0 {
			delete(r.operations, deviceID)
		}
		r.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-op.permit:
	}
	defer func() { op.permit <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return run()
}

// SerializedSink 为会改变端口占用的设备回执使用同一设备许可，保留原 Sink 的其他能力。
// Registry 必须与所有控制服务及释放端点共用；Sink 的连接资源仍由进程管理。
type SerializedSink struct {
	Registry *Registry     // 命令与占用操作共用的许可属主
	Sink     protocol.Sink // 实际鉴权与设备事件持久化能力
}

// Register 原样委托设备鉴权，不改变端口占用或建立后台任务。
func (s SerializedSink) Register(ctx context.Context, in protocol.Registration) error {
	return s.Sink.Register(ctx, in)
}

// Record 对启动/停止 ACK 的整次持久化取设备许可，其余事件原样保存；返回持久化或取消错误。
func (s SerializedSink) Record(ctx context.Context, event protocol.Event) error {
	if event.Type == protocol.StartResult || event.Type == protocol.StopResult {
		return s.Registry.WithDeviceOperation(ctx, event.DeviceID, func() error { return s.Sink.Record(ctx, event) })
	}
	return s.Sink.Record(ctx, event)
}

// HasChargingPorts 保留底层的重连心跳选择能力；未实现时沿用无充电端口的默认值。
func (s SerializedSink) HasChargingPorts(ctx context.Context, id string) (bool, error) {
	if reader, ok := s.Sink.(interface {
		HasChargingPorts(context.Context, string) (bool, error)
	}); ok {
		return reader.HasChargingPorts(ctx, id)
	}
	return false, nil
}

// RecordSession 保留底层连接统计持久化；底层没有会话记录能力时不生成伪记录。
func (s SerializedSink) RecordSession(ctx context.Context, record protocol.SessionRecord) error {
	if recorder, ok := s.Sink.(protocol.SessionRecorder); ok {
		return recorder.RecordSession(ctx, record)
	}
	return nil
}

// Registry 同时是适配器依赖的会话登记入口和运行时的命令下发器。
// 两条断言检查 Registry 是否同时实现设备发送和连接关闭接口。
var (
	_ protocol.SessionRouter = (*Registry)(nil)
	_ protocol.CommandSender = (*Registry)(nil)
)
