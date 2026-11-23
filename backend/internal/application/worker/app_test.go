package worker

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	workerruntime "chargepilot/internal/worker/runtime"
)

// idleBatch 提供立即成功的空批任务，供 worker 生命周期检查。
type idleBatch struct{}

func (idleBatch) Run(context.Context) (int, error) { return 0, nil }

func (idleBatch) DispatchBatch(context.Context) (int, error) { return 0, nil }

func (idleBatch) SyncBatch(context.Context) (int, error) { return 0, nil }

// idleDispatch 提供立即成功的空资金派发任务，不执行资金写入。
type idleDispatch struct{}

func (idleDispatch) Run(context.Context) error { return nil }

// idleScheduler 提供立即成功的空调度任务，不访问调度表。
type idleScheduler struct{}

func (idleScheduler) CleanupHistory(context.Context) error { return nil }

func (idleScheduler) RunDue(context.Context) error { return nil }

// TestHTTPBindFailureStopsAllWorkerLoops 验证 HTTP 绑定失败时返回错误，并等待三个 worker 循环退出；使用空任务，不连接存储。
func TestHTTPBindFailureStopsAllWorkerLoops(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	app := Application{Server: &http.Server{Addr: l.Addr().String()}, Jobs: workerruntime.Runtime{
		Scheduler: idleScheduler{}, AutoStops: idleBatch{}, Billing: idleDispatch{}, Refunds: idleDispatch{},
		DeviceAlerts: idleBatch{}, CardEvents: idleBatch{}, PaidStarts: idleBatch{}, StartResults: idleBatch{}, EndResults: idleBatch{},
	}}
	finished := make(chan error, 1)
	go func() { finished <- app.Run(context.Background()) }()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("bind error lost")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker loops did not exit after HTTP failure")
	}
}
