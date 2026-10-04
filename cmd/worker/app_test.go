package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	workerruntime "github.com/ChargePilot2026/charge-pilot/internal/worker/runtime"
)

type idleBatch struct{}

func (idleBatch) Run(context.Context) (int, error)           { return 0, nil }
func (idleBatch) DispatchBatch(context.Context) (int, error) { return 0, nil }
func (idleBatch) SyncBatch(context.Context) (int, error)     { return 0, nil }

type idleDispatch struct{}

func (idleDispatch) Run(context.Context) error { return nil }

type idleScheduler struct{}

func (idleScheduler) CleanupHistory(context.Context) error { return nil }
func (idleScheduler) RunDue(context.Context) error         { return nil }

func TestHTTPBindFailureStopsAllWorkerLoops(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	app := application{Server: &http.Server{Addr: l.Addr().String()}, Jobs: workerruntime.Runtime{
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
