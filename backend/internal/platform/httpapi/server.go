package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Serve 运行 HTTP 服务，取消时先等待请求完成；超过宽限期后取消请求并关闭连接，仍等待处理器退出。
// 处理器必须响应请求取消，不得接管连接或启动未管理的任务。
// 本函数独占 server；ctx 和 server 必须非 nil，grace 必须为正。已取消时不监听，返回 nil。
// 返回监听、运行或停机错误；调用方在返回后释放数据库和客户端。
func Serve(ctx context.Context, server *http.Server, grace time.Duration) error {
	if grace <= 0 {
		return errors.New("HTTP shutdown grace must be positive")
	}
	if ctx.Err() != nil {
		return nil
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	return serve(ctx, server, listener, grace)
}

// serve 运行已绑定的 HTTP 服务；停机先等待请求，失败时取消请求并关闭连接。
// 停止接收新处理器后，仍等待已有处理器退出；返回合并后的运行和停机错误。
// 本函数管理 server 的请求 context 和 Handler，调用方负责关闭监听及其他资源。
func serve(ctx context.Context, server *http.Server, listener net.Listener, grace time.Duration) error {
	// 进程取消先停止监听并尝试排空，不能立即取消已接收的请求。
	requests, cancelRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRequests()
	server.BaseContext = func(net.Listener) context.Context { return requests }
	handler := server.Handler
	if handler == nil {
		handler = http.DefaultServeMux
	}
	var active sync.WaitGroup
	var admission sync.Mutex
	accepting := true
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admission.Lock()
		if !accepting {
			admission.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		active.Add(1)
		admission.Unlock()
		defer active.Done()
		handler.ServeHTTP(w, r)
	})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
		served = nil
		if errors.Is(serveErr, http.ErrServerClosed) && ctx.Err() == nil {
			serveErr = errors.New("HTTP server stopped unexpectedly")
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), grace)
	shutdownErr := server.Shutdown(shutdown)
	cancel()
	if shutdownErr != nil {
		cancelRequests()
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	if served != nil {
		serveErr = <-served
	}
	// 停机后停止接收处理器与 Add 共用同一把锁，等待期间不会再加入处理器。
	admission.Lock()
	accepting = false
	admission.Unlock()
	active.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr)
}
