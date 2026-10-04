package main

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type blockedCleaner struct{ started, canceled, release chan struct{} }

func (c blockedCleaner) CleanupExpired(ctx context.Context) (int, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	return 0, ctx.Err()
}

func TestCancellationWaitsForCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := blockedCleaner{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(c.release) })
	app := application{Server: &http.Server{Addr: "127.0.0.1:0"}, Exports: c}
	finished := make(chan error, 1)
	go func() { finished <- app.Run(ctx) }()
	select {
	case <-c.started:
		cancel()
		select {
		case <-c.canceled:
		case <-time.After(3 * time.Second):
			t.Fatal("cleanup not canceled")
		}
		select {
		case <-finished:
			t.Fatal("returned while cleanup still active")
		default:
		}
		once.Do(func() { close(c.release) })
	case err := <-finished:
		t.Fatalf("application returned before cancellation: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("application did not respond")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("application did not stop")
	}
}

type idleCleaner struct{}

func (idleCleaner) CleanupExpired(context.Context) (int, error) { return 0, nil }
func TestHTTPBindFailureStopsCleanupLoop(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	app := application{Server: &http.Server{Addr: l.Addr().String()}, Exports: idleCleaner{}}
	finished := make(chan error, 1)
	go func() { finished <- app.Run(context.Background()) }()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("bind error lost")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup loop kept running after HTTP failure")
	}
}
