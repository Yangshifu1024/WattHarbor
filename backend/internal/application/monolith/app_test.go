package monolith

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gatewayruntime "wattharbor/internal/gateway/runtime"
	"wattharbor/internal/protocol"
	"wattharbor/internal/protocol/dc589"
)

type lifecycleSink struct{}

func (lifecycleSink) Register(context.Context, protocol.Registration) error { return nil }
func (lifecycleSink) Record(context.Context, protocol.Event) error          { return nil }

func lifecycleDevices(address string) gatewayruntime.Runtime {
	return gatewayruntime.Runtime{
		Endpoints:      []protocol.Endpoint{{Address: address, Adapter: dc589.TCPAdapter{}}},
		Sink:           lifecycleSink{},
		MaxConnections: 1,
	}
}

func TestBindFailureDoesNotStartJobs(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	for _, failed := range []string{"device", "HTTP"} {
		t.Run(failed, func(t *testing.T) {
			httpAddr, deviceAddr := "127.0.0.1:0", "127.0.0.1:0"
			if failed == "device" {
				deviceAddr = occupied.Addr().String()
			} else {
				httpAddr = occupied.Addr().String()
			}
			var ready, jobStarted atomic.Bool
			err := serve(t.Context(), &http.Server{Addr: httpAddr}, lifecycleDevices(deviceAddr), &ready, []task{
				{"job", func(context.Context) error {
					jobStarted.Store(true)
					return nil
				}},
			})
			if err == nil || !strings.Contains(err.Error(), "listen "+failed) {
				t.Fatalf("listen %s error = %v", failed, err)
			}
			if ready.Load() || jobStarted.Load() {
				t.Fatal("listen failure started jobs or marked the application ready")
			}
		})
	}
}

func TestShutdownWaitsForCanceledJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var ready atomic.Bool
	finished := make(chan error, 1)
	go func() {
		finished <- serve(ctx, &http.Server{Addr: "127.0.0.1:0"}, lifecycleDevices("127.0.0.1:0"), &ready, []task{
			{"cleanup", func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return ctx.Err()
			}},
		})
	}()

	select {
	case <-started:
	case err := <-finished:
		t.Fatalf("serve returned before the job started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("job did not start")
	}
	cancel()
	select {
	case <-canceled:
	case err := <-finished:
		t.Fatalf("serve returned before canceling the job: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("job did not receive cancellation")
	}
	if ready.Load() {
		t.Fatal("application is still ready during shutdown")
	}
	select {
	case err := <-finished:
		t.Fatalf("serve returned before the canceled job stopped: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop")
	}
}
