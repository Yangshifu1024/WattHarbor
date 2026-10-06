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
// ctx 为非 nil 的进程生命周期；server 须非 nil，grace 必须为正数且以 duration 表示宽限。
// 返回监听、运行与停机错误；grace 非正先返回配置错误，已取消 ctx 返回 nil 且不绑定端口。
// 绑定成功后本函数负责最终关闭监听；不关闭调用方存储或客户端。
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

// serve 接收进程 ctx、独占的 server、已绑定的 listener 及正数停机宽限 grace；调用方负责监听的最终关闭。
// 返回 HTTP 运行与停机错误的合并值，正常取消返回 nil，异常自行停止与宽限超时保留错误；修改 server 的 BaseContext 和 Handler。
// 请求 context 保留 ctx 的值但脱离进程立即取消，以便先排空；停机失败时取消请求并关闭连接，随后封闭处理器准入并等待全部已准入处理器退出。
// 处理器必须响应请求取消且不得接管连接或启动未受管理任务；此方法不关闭数据库或服务客户端。
func serve(ctx context.Context, server *http.Server, listener net.Listener, grace time.Duration) error {
	// 进程取消先停止监听并尝试排空，不能立即取消已准入的请求。
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
	// 停机后封闭准入与 Add 共用同一把锁，等待期间不会再加入处理器。
	admission.Lock()
	accepting = false
	admission.Unlock()
	active.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr)
}
