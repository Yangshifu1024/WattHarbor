package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type recordedTask struct {
	name   string
	calls  *[]string
	fail   bool
	cancel context.CancelFunc
}

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
func (t recordedTask) DispatchBatch(ctx context.Context) (int, error) { return t.Run(ctx) }
func (t recordedTask) SyncBatch(ctx context.Context) (int, error)     { return t.Run(ctx) }
func (t recordedTask) PublishBatch(ctx context.Context) (int, error)  { return t.Run(ctx) }

type recordedDispatch struct{ recordedTask }

func (t recordedDispatch) Run(ctx context.Context) error {
	_, err := t.recordedTask.Run(ctx)
	return err
}

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
