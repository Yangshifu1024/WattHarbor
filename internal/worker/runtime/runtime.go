// Package runtime 管理 worker 的定时任务顺序与取消，不创建进程资源。
package runtime

import (
	"context"
	"errors"
	"log"
	"time"
)

// BatchRunner 提供取消可传播的同步批任务；运行时只记录失败，不使用完成数量决定任务顺序。
type BatchRunner interface {
	// Run 按传入的任务 context 同步执行一批工作，返回实现定义的完成数量与错误；持久化及取消处理由实现负责。
	Run(context.Context) (int, error)
}

// Dispatcher 提供取消可传播的同步派发任务；业务提交与恢复由具体实现负责。
type Dispatcher interface {
	// Run 按传入的任务 context 同步派发工作并返回执行错误；业务提交与取消处理由实现负责。
	Run(context.Context) error
}

// StartDispatcher 提供持有分页状态的同步启动派发能力，运行时复用同一实例。
type StartDispatcher interface {
	// DispatchBatch 按传入的任务 context 同步派发一批启动，返回实现定义的完成数量与执行错误；持久化及取消处理由实现负责。
	DispatchBatch(context.Context) (int, error)
}

// ResultSynchronizer 提供同步回执推进能力，持久化与幂等责任属于具体同步器。
type ResultSynchronizer interface {
	// SyncBatch 按传入的任务 context 同步推进一批回执，返回完成数量与执行错误；确认、重放及持久化由实现负责。
	SyncBatch(context.Context) (int, error)
}

// Publisher 提供同步 Outbox 发布能力，运行时不复制发布或补偿逻辑。
type Publisher interface {
	// PublishBatch 按传入的任务 context 同步发布一批事件，返回完成数量与执行错误；投递及幂等记录由实现负责。
	PublishBatch(context.Context) (int, error)
}

// Publication 组合日志来源与同一发布器实例，按切片顺序进入同步轮次。
type Publication struct {
	// Source 发布来源名称，仅拼接日志任务名称，不更换发布器或持久身份。
	Source string
	// Publisher 同一来源的同步发布器实例，具体实现负责重放与持久状态。
	Publisher Publisher
}

// Runtime 组织已注入任务的时钟、串行轮次及取消检查；不打开或关闭进程资源。
type Runtime struct {
	// Scheduler 调度清理与到期执行能力，运行时串行调用，不接管其数据库资源。
	Scheduler interface {
		// CleanupHistory 按传入的任务 context 清理历史；同步返回执行错误，取消影响与事务责任由实现负责，不启动脱离管理的任务。
		CleanupHistory(context.Context) error
		// RunDue 按传入的任务 context 执行到期调度；同步返回执行错误，取消影响与事务责任由实现负责。
		RunDue(context.Context) error
	}
	// AutoStops 自动停机批任务，资金轮次首先执行。
	AutoStops BatchRunner
	// Billing 计费派发任务，在自动停机之后同步执行。
	Billing Dispatcher
	// Refunds 退款派发任务，在计费派发之后同步执行。
	Refunds Dispatcher
	// DeviceAlerts 设备告警同步批任务，每轮同步首先执行。
	DeviceAlerts BatchRunner
	// CardEvents 刷卡事件批任务，在设备告警之后执行。
	CardEvents BatchRunner
	// PaidStarts 待启动派发器，复用实例所持分页游标，不能每轮重建。
	PaidStarts StartDispatcher
	// StartResults 启动回执同步器，在启动派发之后执行。
	StartResults ResultSynchronizer
	// EndResults 结束回执同步器，在启动回执之后执行。
	EndResults ResultSynchronizer
	// Publishers 调用方持有的发布来源切片，运行时不复制，按现有顺序在回执同步后执行；运行期间不得并发修改。
	Publishers []Publication
}

// RunScheduler 接收生命周期 ctx，每秒同步检查调度；首次 tick 清理历史，之后从每次清理返回时重新计算一小时期限，清理失败也更新该期限。
// 清理后仅在 ctx 未取消时执行到期任务；单次错误记日志，循环正常取消返回 nil，不打开资源，并等待当前同步任务返回。
func (r *Runtime) RunScheduler(ctx context.Context) error {
	nextCleanup := time.Now()
	return poll(ctx, time.Second, func(ctx context.Context) {
		if time.Now().After(nextCleanup) {
			report("scheduled history cleanup", r.Scheduler.CleanupHistory(ctx))
			nextCleanup = time.Now().Add(time.Hour)
		}
		if ctx.Err() == nil {
			report("scheduled tasks", r.Scheduler.RunDue(ctx))
		}
	})
}

// RunFinance 接收生命周期 ctx，每十秒同步执行一轮资金任务，首次执行等待首个 tick；轮次错误记录后继续后续未取消任务。
// 取消时返回 nil，须等待在途任务返回，不替具体实现提交或回滚，也不关闭资源。
func (r *Runtime) RunFinance(ctx context.Context) error {
	return poll(ctx, 10*time.Second, r.financeOnce)
}

// financeOnce 接收单轮 ctx，按自动停机、计费、退款顺序同步执行；每个任务前检查取消，单次错误只记日志，未取消时继续后续任务。
// 无返回值，批数量不参与调度；业务副作用及恢复属于各任务实现。
func (r *Runtime) financeOnce(ctx context.Context) {
	runSequence(ctx, []task{
		{"charge auto stop", counted(r.AutoStops.Run)},
		{"billing dispatch", r.Billing.Run},
		{"refund dispatch", r.Refunds.Run},
	})
}

// RunSync 串行推进同一轮任务；PaidStarts 持有分页游标，循环间不重新装配。
// ctx 为同步循环生命周期，首次执行等待一秒 tick；每轮复用 r 的任务与发布器实例。
// 取消后等待当前同步任务返回并返回 nil，单任务错误只记录，不关闭资源或返回业务错误。
func (r *Runtime) RunSync(ctx context.Context) error {
	return poll(ctx, time.Second, r.syncOnce)
}

// syncOnce 接收单轮 ctx，按告警、刷卡、待启动、启动回执、结束回执、发布切片原顺序同步执行。
// 无返回值，每个任务前检查取消；单次错误只记日志，保留已注入派发器游标与发布器身份，不复制业务逻辑或建立事务。
func (r *Runtime) syncOnce(ctx context.Context) {
	tasks := []task{
		{"device alerts", counted(r.DeviceAlerts.Run)},
		{"card events", counted(r.CardEvents.Run)},
		{"paid charge starts", counted(r.PaidStarts.DispatchBatch)},
		{"charge start results", counted(r.StartResults.SyncBatch)},
		{"charge end results", counted(r.EndResults.SyncBatch)},
	}
	for _, publisher := range r.Publishers {
		tasks = append(tasks, task{"outbox " + publisher.Source, counted(publisher.Publisher.PublishBatch)})
	}
	runSequence(ctx, tasks)
}

// task 组合日志名称与同步任务回调，供单轮顺序执行。
type task struct {
	// name 单轮日志中的任务名称，不影响执行顺序或错误分类。
	name string
	// run 同步任务回调，接收该轮 context 并返回执行错误；调用方须等待其返回。
	run func(context.Context) error
}

// counted 接收非 nil 的计数任务 run，返回只保留错误的同步回调；回调把传入 ctx 原样交给 run，忽略其完成数量，不重试或启动 goroutine。
func counted(run func(context.Context) (int, error)) func(context.Context) error {
	return func(ctx context.Context) error { _, err := run(ctx); return err }
}

// runSequence 接收单轮 ctx 与调用方持有的 tasks 切片，按顺序同步执行并记录每次错误；每次调用前取消即停止，单任务失败不阻止后续任务。
// 无返回值，不复制切片、不等待额外后台任务；在途回调必须自行响应 ctx，返回后才继续检查取消。
func runSequence(ctx context.Context, tasks []task) {
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		report(task.name, task.run(ctx))
	}
}

// report 接收日志任务名 name 与执行错误 err；nil 或可由 errors.Is 匹配 context.Canceled 时静默，其余错误按名称记录。
// 无返回值，不包装错误、不取消 context、不影响后续任务顺序。
func report(name string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("%s: %v", name, err)
	}
}

// poll 接收生命周期 ctx、正数时钟间隔 interval 与非 nil 同步回调 run；首轮等待 tick，每次 tick 在未取消时直接调用 run(ctx)。
// 取消返回 nil 并停止 ticker，已进入回调必须先返回，故回调须响应 ctx；不启动额外 goroutine、不捕获 panic，interval 非正由 time.NewTicker 触发 panic。
func poll(ctx context.Context, interval time.Duration, run func(context.Context)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if ctx.Err() == nil {
				run(ctx)
			}
		}
	}
}
