package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type observedListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *observedListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
	}
}

func testListener(t *testing.T) *observedListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return &observedListener{Listener: l, closed: make(chan struct{})}
}

func TestServeDrainsRequestsBeforeReturning(t *testing.T) {
	l := testListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.Context()
		<-release
		_, _ = io.WriteString(w, "completed")
	})}
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, server, l, time.Second) }()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		defer client.CloseIdleConnections()
		resp, err := client.Get("http://" + l.Addr().String())
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			err = readErr
			if string(body) != "completed" {
				err = errors.New("response was not drained")
			}
		}
		response <- err
	}()
	var requestCtx context.Context
	select {
	case requestCtx = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	waitSignal(t, l.closed)
	if requestCtx.Err() != nil {
		t.Fatal("request canceled during graceful drain")
	}
	select {
	case err := <-finished:
		t.Fatalf("returned before handler completed: %v", err)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	if err := <-response; err != nil {
		t.Fatal(err)
	}
}

func TestServeTimeoutCancelsAndWaitsForHandler(t *testing.T) {
	l := testListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, canceled := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
	})}
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, server, l, 30*time.Millisecond) }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: 3 * time.Second}
		defer client.CloseIdleConnections()
		if resp, err := client.Get("http://" + l.Addr().String()); err == nil {
			_ = resp.Body.Close()
		}
	}()
	waitSignal(t, started)
	cancel()
	waitSignal(t, canceled)
	select {
	case err := <-finished:
		t.Fatalf("returned before canceled handler exited: %v", err)
	default:
	}
	once.Do(func() { close(release) })
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want shutdown deadline, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	waitSignal(t, requestDone)
}

func TestServeBindFailureReturnsWithoutWaitingForCancellation(t *testing.T) {
	l := testListener(t)
	if err := Serve(context.Background(), &http.Server{Addr: l.Addr().String()}, time.Second); err == nil {
		t.Fatal("occupied address accepted")
	}
}
