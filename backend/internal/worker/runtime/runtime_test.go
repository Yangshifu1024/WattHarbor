package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// recordedTask 通过内存记录、固定失败及可选取消验证任务次序，不访问外部依赖。
type recordedTask struct {
	// name 当前夹具写入调用记录的名称。
	name string
	// calls 测试持有的调用记录切片指针，供同一测试线程追加与断言。
	calls *[]string
	// fail 为 true 时在记录与可选取消之后返回固定临时失败。
	fail bool
	// cancel 可选的测试取消函数；nil 不取消，非 nil 在任务记录后调用。
	cancel context.CancelFunc
}

// Run 记录调用名称并按配置取消 context；fail=true 时返回预设错误，否则返回完成数 1。
func (t recordedTask) Run(context.Context) (int, error) {
	*t.calls = append(*t.calls, t.name)
	if t.cancel != nil {
		t.cancel()
	}
	if t.fail {
		return 0, errors.New("temporary failure")
	}
	return 1, nil
}

// DispatchBatch 接收 ctx 并原样交给同一 t.Run，返回其数量与错误；保留记录、可选取消及失败副作用，不另执行任务。
func (t recordedTask) DispatchBatch(ctx context.Context) (int, error) { return t.Run(ctx) }

// SyncBatch 接收 ctx 并原样交给同一 t.Run，返回其数量与错误；保留记录、可选取消及失败副作用，不另执行任务。
func (t recordedTask) SyncBatch(ctx context.Context) (int, error) { return t.Run(ctx) }

// PublishBatch 接收 ctx 并原样交给同一 t.Run，返回其数量与错误；保留记录、可选取消及失败副作用，不另执行任务。
func (t recordedTask) PublishBatch(ctx context.Context) (int, error) { return t.Run(ctx) }

// recordedDispatch 将计数任务夹具适配为只返回错误的资金派发夹具。
type recordedDispatch struct {
	// recordedTask 复用记录、失败与可选取消行为的计数任务夹具。
	recordedTask
}

// Run 接收 ctx 并传入嵌入的 recordedTask，忽略其数量，返回错误；保留内存记录与可选取消副作用。
func (t recordedDispatch) Run(ctx context.Context) error {
	_, err := t.recordedTask.Run(ctx)
	return err
}

// TestSyncOrderAndContinueAfterTaskFailure 核对一轮同步的固定任务与发布顺序，首任务失败不阻止后续任务；只记录内存调用，断言失败由 t 报告，无网络或数据库操作。
func TestSyncOrderAndContinueAfterTaskFailure(t *testing.T) {
	var calls []string
	makeTask := func(name string) recordedTask { return recordedTask{name: name, calls: &calls} }
	alerts := makeTask("alerts")
	alerts.fail = true
	r := Runtime{DeviceAlerts: alerts, CardEvents: makeTask("cards"), PaidStarts: makeTask("paid"), StartResults: makeTask("start"), EndResults: makeTask("end"), Publishers: []Publication{{"gateway", makeTask("outbox gateway")}, {"user", makeTask("outbox user")}, {"admin", makeTask("outbox admin")}}}
	r.syncOnce(context.Background())
	want := []string{"alerts", "cards", "paid", "start", "end", "outbox gateway", "outbox user", "outbox admin"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v", calls)
	}
}

// TestFinanceOrderAndCancellationStopsLaterTasks 核对自动停机先于计费派发，计费任务取消后不再进入退款任务；只记录内存调用，失败由 t 报告并释放 context。
func TestFinanceOrderAndCancellationStopsLaterTasks(t *testing.T) {
	var calls []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := Runtime{AutoStops: recordedTask{name: "stop", calls: &calls}, Billing: recordedDispatch{recordedTask{name: "billing", calls: &calls, cancel: cancel}}, Refunds: recordedDispatch{recordedTask{name: "refund", calls: &calls}}}
	r.financeOnce(ctx)
	if !reflect.DeepEqual(calls, []string{"stop", "billing"}) {
		t.Fatalf("ran work after cancellation: %v", calls)
	}
}

// TestPollWaitsForInFlightTaskToExit 使用闸门核对取消后 poll 必须等待在途同步回调退出；失败由 t 报告，测试取消 context 并释放回调，不访问存储。
func TestPollWaitsForInFlightTaskToExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- poll(ctx, time.Millisecond, func(ctx context.Context) { close(started); <-ctx.Done(); <-release })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task not started")
	}
	cancel()
	select {
	case <-finished:
		t.Fatal("poll returned while task still active")
	default:
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("poll did not exit")
	}
}
