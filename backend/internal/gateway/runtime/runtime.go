// Package runtime 管理设备接入与停止重试，不创建进程资源。
package runtime

import (
	"context"
	"errors"
	"log"
	"time"

	"chargepilot/internal/gateway/listener"
	"chargepilot/internal/protocol"
)

// Runtime 持有已装配的设备监听配置及两类停机恢复能力，执行同步的监听或定时恢复循环。
// 本类型不创建或关闭数据库、连接注册表与客户端资源；调用方负责提供有效依赖并管理两方法的取消与等待。
type Runtime struct {
	// Endpoints 原协议适配器及 TCP 监听地址集合；Serve 交监听器校验并先完成全部端口绑定。
	Endpoints []protocol.Endpoint
	// Sink 原设备协议事件接收能力，交各适配器处理连接；实际持久化等副作用由实现负责，本类型不关闭它。
	Sink protocol.Sink
	// MaxConnections 原全部监听端点共用的并发连接上限，须大于零；超限连接由监听器拒绝并关闭。
	MaxConnections int
	// Compensation 原启动补偿停机恢复能力；运行 RetryStops 时须非 nil，先于 UserStops 顺序调用。
	Compensation interface {
		// RetryStopping 接收本轮 ctx，执行原补偿 STOP 的到期恢复；返回恢复错误，持久化与设备下发由实现负责。
		RetryStopping(context.Context) error
	}
	// UserStops 原用户停机恢复能力；运行 RetryStops 时须非 nil，每轮在补偿调用返回且 ctx 未取消时调用。
	UserStops interface {
		// RetryPending 接收本轮 ctx，执行原用户 STOP 的到期恢复；返回恢复错误，持久化与设备下发由实现负责。
		RetryPending(context.Context) error
	}
}

// Serve 接收 ctx，按原 Endpoints、Sink 和 MaxConnections 运行设备 TCP 监听，等待监听循环与已接入处理器退出。
// context.Canceled 归为正常退出并返回 nil；配置、绑定、接收及其他退出错误原样返回，包括截止时间到期错误。
// 监听器负责关闭监听和设备连接，不释放调用方持有的数据库或客户端资源。
func (r Runtime) Serve(ctx context.Context) error {
	err := listener.Serve(ctx, r.Endpoints, r.Sink, r.MaxConnections)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// RetryStops 接收 ctx，等待原五秒 ticker 后顺序调用 Compensation.RetryStopping 与 UserStops.RetryPending，复用同一 ctx。
// 每个调用前检查取消；恢复错误仅按原日志记录并继续，context.Canceled 不记错误，ctx 结束时停止 ticker 并返回 nil。
// 本方法同步等待正在执行的能力返回，不创建额外任务、重试身份或进程资源；退出及时性依赖注入能力响应 ctx。
func (r Runtime) RetryStops(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if ctx.Err() != nil {
				continue
			}
			if err := r.Compensation.RetryStopping(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("STOP compensation: %v", err)
			}
			if ctx.Err() != nil {
				continue
			}
			if err := r.UserStops.RetryPending(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("user STOP retry: %v", err)
			}
		}
	}
}
