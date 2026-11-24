// Package runtime 管理设备接入、停止重试与投递历史清理，不创建进程资源。
package runtime

import (
	"context"
	"errors"
	"log"
	"time"
	"wattharbor/internal/platform/diagnostics"

	"wattharbor/internal/gateway/listener"
	"wattharbor/internal/protocol"
)

// Runtime 持有设备监听、两类停机恢复及投递历史清理能力，执行受管理的监听与定时循环。
// 本类型不创建或关闭数据库、连接注册表与客户端资源；调用方负责有效依赖及所有方法的取消与等待。
type Runtime struct {
	Diagnostics *diagnostics.Registry
	// Endpoints 协议适配器及 TCP 监听地址集合；Serve 交监听器校验并先完成全部端口绑定。
	Endpoints []protocol.Endpoint
	// Sink 设备协议事件接收能力，交各适配器处理连接；实际持久化等副作用由实现负责，本类型不关闭它。
	Sink protocol.Sink
	// MaxConnections 全部监听端点共用的并发连接上限，须大于零；超限连接由监听器拒绝并关闭。
	MaxConnections int
	// Compensation 启动补偿停机恢复能力；运行 RetryStops 时须非 nil，先于 UserStops 顺序调用。
	Compensation interface {
		// RetryStopping 接收本轮 ctx，执行补偿 STOP 的到期恢复；返回恢复错误，持久化与设备下发由实现负责。
		RetryStopping(context.Context) error
	}
	// UserStops 用户停机恢复能力；运行 RetryStops 时须非 nil，每轮在补偿调用返回且 ctx 未取消时调用。
	UserStops interface {
		// RetryPending 接收本轮 ctx，执行用户 STOP 的到期恢复；返回恢复错误，持久化与设备下发由实现负责。
		RetryPending(context.Context) error
	}
	// SyncHistory 清理已完成或缺源投递历史；nil表示未装配。
	SyncHistory interface {
		// CleanupSyncRetries 接收任务ctx执行有上限清理，保留仍待投递的核实记录。
		CleanupSyncRetries(context.Context) error
	}
}

// CleanupSyncHistory 启动时及每小时顺序清理一次；错误记录后下轮继续，取消后等待当前调用退出。
// 清理范围及上限由注入能力决定；本方法不创建脱离管理的任务，也不释放数据库。
func (r Runtime) CleanupSyncHistory(ctx context.Context) error {
	if r.SyncHistory == nil {
		return nil
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := r.Diagnostics.Run(ctx, "sync history cleanup", r.SyncHistory.CleanupSyncRetries); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("sync delivery history cleanup: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Serve 按 Endpoints、Sink 和 MaxConnections 运行设备 TCP 监听，等待监听循环与已接入处理器退出。
// context.Canceled 归为正常退出并返回 nil；配置、绑定、接收及其他退出错误原样返回，包括截止时间到期错误。
// 监听器负责关闭监听和设备连接，不释放调用方持有的数据库或客户端资源。
func (r Runtime) Serve(ctx context.Context) error {
	err := listener.Serve(ctx, r.Endpoints, r.Sink, r.MaxConnections)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// RetryStops 等待五秒 ticker 后顺序调用 Compensation.RetryStopping 与 UserStops.RetryPending，复用同一 ctx。
// 每个调用前检查取消；恢复错误仅按日志记录并继续，context.Canceled 不记错误，ctx 结束时停止 ticker 并返回 nil。
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
			if err := r.Diagnostics.Run(ctx, "compensation stop retry", r.Compensation.RetryStopping); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("STOP compensation: %v", err)
			}
			if ctx.Err() != nil {
				continue
			}
			if err := r.Diagnostics.Run(ctx, "user stop retry", r.UserStops.RetryPending); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("user STOP retry: %v", err)
			}
		}
	}
}
