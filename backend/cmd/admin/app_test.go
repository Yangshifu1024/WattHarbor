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
	// started,canceled,release started 通知清理已进入，canceled 通知已观察取消，release 由测试关闭后允许任务退出；三个通道由测试持有。
	started, canceled, release chan struct{}
}

// CleanupExpired 接收 ctx，通知清理已进入并等待取消，再通知已取消并等待测试 release；返回零数量与 ctx.Err()，不删除文件。
func (c blockedCleaner) CleanupExpired(ctx context.Context) (int, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	return 0, ctx.Err()
}

// TestCancellationWaitsForCleanup 使用 t 启动真实应用，确认进程取消传入导出清理且应用等待清理释放闸门后才返回；失败通过 t 报告，测试关闭监听并释放任务。
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

// CleanupExpired 忽略传入的 context，立即返回零数量与 nil；无文件或后台任务副作用。
func (idleCleaner) CleanupExpired(context.Context) (int, error) { return 0, nil }

// TestHTTPBindFailureStopsCleanupLoop 使用 t 占用 HTTP 地址，核对绑定错误取消导出清理循环并及时返回非空错误；测试关闭占用监听，无数据库操作。
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
