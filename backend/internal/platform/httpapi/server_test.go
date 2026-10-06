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

// observedListener 包装真实 TCP 监听并仅通知一次关闭事件，供 HTTP 停机顺序验证。
type observedListener struct {
	// net.Listener 被包装的真实 TCP 监听，关闭结果保持原样。
	net.Listener
	// closed 首次关闭时被关闭的通知通道，由观察监听负责关闭。
	closed chan struct{}
	// once 保证关闭通知仅发送一次，底层 Close 仍按每次调用执行。
	once sync.Once
}

// Close 无入参，关闭被包装监听并返回原关闭错误；无论该错误是否为空，仅首次调用关闭 l.closed 通知通道。
func (l *observedListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

// waitSignal 接收测试句柄 t 与事件通道 ch，最多等待三秒收到事件；无返回值，超时由 t.Fatal 终止当前测试，不关闭 ch。
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
	}
}

// testListener 接收测试句柄 t，在回环随机端口打开 TCP 监听并返回关闭可观测的包装；绑定错误由 t.Fatal 终止，监听关闭登记到 t.Cleanup。
func testListener(t *testing.T) *observedListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return &observedListener{Listener: l, closed: make(chan struct{})}
}

// TestServeDrainsRequestsBeforeReturning 使用 t 启动真实本地 HTTP 请求，核对取消进程时请求 context 保持有效且 serve 等待处理器完成；失败通过 t 报告，闸门与监听在退出时释放。
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

// TestServeTimeoutCancelsAndWaitsForHandler 使用 t 核对宽限超时会取消真实请求，但 serve 仍等待已取消处理器退出并返回截止错误；测试释放闸门、监听及客户端，无数据库操作。
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

// TestServeBindFailureReturnsWithoutWaitingForCancellation 使用 t 占用监听地址，核对 Serve 立即返回绑定错误，不等待未取消的进程 context；测试清理占用监听。
func TestServeBindFailureReturnsWithoutWaitingForCancellation(t *testing.T) {
	l := testListener(t)
	if err := Serve(context.Background(), &http.Server{Addr: l.Addr().String()}, time.Second); err == nil {
		t.Fatal("occupied address accepted")
	}
}
