package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Serve 运行 HTTP 服务，取消时等待请求排空。超过宽限期后取消请求并关闭连接，
// 仍等待处理器退出才返回，使调用方可以在返回后安全释放存储。
// 处理器须响应请求 context；不支持接管连接或在处理器外启动未受管理的任务。
// server 由本函数独占使用，BaseContext 和 Handler 的生命周期由本函数管理。
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

func serve(ctx context.Context, server *http.Server, listener net.Listener, grace time.Duration) error {
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
	admission.Lock()
	accepting = false
	admission.Unlock()
	active.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr)
}
