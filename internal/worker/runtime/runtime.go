// Package runtime 管理 worker 的定时任务顺序与取消，不创建进程资源。
package runtime

import (
	"context"
	"errors"
	"log"
	"time"
)

type BatchRunner interface {
	Run(context.Context) (int, error)
}
type Dispatcher interface{ Run(context.Context) error }
type StartDispatcher interface {
	DispatchBatch(context.Context) (int, error)
}
type ResultSynchronizer interface {
	SyncBatch(context.Context) (int, error)
}
type Publisher interface {
	PublishBatch(context.Context) (int, error)
}
type Publication struct {
	Source    string
	Publisher Publisher
}

type Runtime struct {
	Scheduler interface {
		CleanupHistory(context.Context) error
		RunDue(context.Context) error
	}
	AutoStops    BatchRunner
	Billing      Dispatcher
	Refunds      Dispatcher
	DeviceAlerts BatchRunner
	CardEvents   BatchRunner
	PaidStarts   StartDispatcher
	StartResults ResultSynchronizer
	EndResults   ResultSynchronizer
	Publishers   []Publication
}

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

func (r *Runtime) RunFinance(ctx context.Context) error {
	return poll(ctx, 10*time.Second, r.financeOnce)
}

func (r *Runtime) financeOnce(ctx context.Context) {
	runSequence(ctx, []task{
		{"charge auto stop", counted(r.AutoStops.Run)},
		{"billing dispatch", r.Billing.Run},
		{"refund dispatch", r.Refunds.Run},
	})
}

// RunSync 串行推进同一轮任务；PaidStarts 持有分页游标，循环间不重新装配。
func (r *Runtime) RunSync(ctx context.Context) error {
	return poll(ctx, time.Second, r.syncOnce)
}

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

type task struct {
	name string
	run  func(context.Context) error
}

func counted(run func(context.Context) (int, error)) func(context.Context) error {
	return func(ctx context.Context) error { _, err := run(ctx); return err }
}

func runSequence(ctx context.Context, tasks []task) {
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		report(task.name, task.run(ctx))
	}
}

func report(name string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("%s: %v", name, err)
	}
}

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
