package main

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// blockedCleaner 提供可阻塞的清理夹具，通过取消与释放两个阶段验证应用等待责任。
type blockedCleaner struct {
	// started 通知任务已开始，canceled 通知已收到取消，release 由测试关闭以允许任务退出。
	started, canceled, release chan struct{}
}

// CleanupExpired 通知任务开始，等待取消后通知测试，再等待 release 放行；不删除文件。
func (c blockedCleaner) CleanupExpired(ctx context.Context) (int, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	return 0, ctx.Err()
}

// TestCancellationWaitsForCleanup 验证进程取消后，应用仍等待导出清理任务退出。
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

// idleCleaner 提供立即成功的空清理夹具，避免绑定失败测试产生文件副作用。
type idleCleaner struct{}

// CleanupExpired 立即返回零清理数量，不删除文件。
func (idleCleaner) CleanupExpired(context.Context) (int, error) { return 0, nil }

// TestHTTPBindFailureStopsCleanupLoop 验证 HTTP 绑定失败时取消导出清理，并返回绑定错误。
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
