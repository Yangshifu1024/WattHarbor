// Package monolith 装配单进程中的公开 HTTP、设备监听及后台任务。
package monolith

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"wattharbor/internal/application/admin"
	"wattharbor/internal/application/central"
	"wattharbor/internal/application/gateway"
	"wattharbor/internal/application/user"
	"wattharbor/internal/application/worker"
	"wattharbor/internal/gateway/listener"
	gatewayruntime "wattharbor/internal/gateway/runtime"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/httpapi"
)

var errShutdownTimeout = errors.New("application shutdown timed out")

// Run 完成依赖装配和两个端口绑定，再开始接入及后台消费。
func Run(ctx context.Context) (err error) {
	wake := make(chan struct{}, 1)
	notify := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	transport := make(localTransport)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	userHandler, closeUser, err := user.Prepare(ctx, client, notify)
	if err != nil {
		return fmt.Errorf("user: %w", err)
	}
	defer func() { err = closePrepared(err, closeUser) }()
	a, closeAdmin, err := admin.Prepare(ctx, client)
	if err != nil {
		return fmt.Errorf("admin: %w", err)
	}
	defer func() { err = closePrepared(err, closeAdmin) }()
	centralHandler, closeCentral, err := central.Prepare(ctx, client)
	if err != nil {
		return fmt.Errorf("central: %w", err)
	}
	defer func() { err = closePrepared(err, closeCentral) }()
	g, closeGateway, err := gateway.Prepare(ctx, client, notify)
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	defer func() { err = closePrepared(err, closeGateway) }()
	w, closeWorker, err := worker.Prepare(ctx, client)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	defer func() { err = closePrepared(err, closeWorker) }()

	transport[config.CentralServiceHost] = centralHandler
	transport[config.GatewayServiceHost] = g.Handler
	transport[config.WorkerServiceHost] = w.Handler

	addr := os.Getenv("APP_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	var ready, syncReady atomic.Bool
	w.Jobs.SyncReady = &syncReady
	w.Jobs.Wake = wake
	server := &http.Server{
		Addr: addr, Handler: publicRouter(a.Handler, userHandler,
			map[string]http.Handler{
				"user": userHandler, "admin": a.Handler, config.CentralServiceHost: centralHandler,
				config.GatewayServiceHost: g.Handler, config.WorkerServiceHost: w.Handler,
			}, &ready, &syncReady),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second,
	}
	return serve(ctx, server, g.Devices, &ready, []task{
		{"sync", w.Jobs.RunSync},
		{"scheduler", w.Jobs.RunScheduler},
		{"finance", w.Jobs.RunFinance},
		{"retry-stops", g.Devices.RetryStops},
		{"cleanup-device", g.Devices.CleanupSyncHistory},
		{"cleanup-exports", a.CleanupExports},
	})
}

// serve 先绑定 HTTP 和设备端口，再启动任务；停机顺序由单体统一管理。
func serve(ctx context.Context, server *http.Server, devices gatewayruntime.Runtime, ready *atomic.Bool, jobs []task) (err error) {
	ports, err := listener.Bind(devices.Endpoints)
	if err != nil {
		return err
	}
	defer ports.Close()
	httpPort, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen HTTP %s: %w", server.Addr, err)
	}
	defer httpPort.Close()
	if ctx.Err() != nil {
		return nil
	}

	acceptCtx, stopAccept := context.WithCancel(context.WithoutCancel(ctx))
	defer stopAccept()
	sessionCtx, stopSessions := context.WithCancel(context.WithoutCancel(ctx))
	defer stopSessions()
	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	httpDone := make(chan struct{})
	tasks := append([]task{
		{"http", func(context.Context) error {
			defer close(httpDone)
			return httpapi.ServeOn(acceptCtx, server, httpPort, 5*time.Second)
		}},
		{"device", func(context.Context) error {
			return ports.Serve(acceptCtx, sessionCtx, devices.Sink, devices.MaxConnections)
		}},
	}, jobs...)
	var group sync.WaitGroup
	exits := make(chan error, len(tasks))
	for _, job := range tasks {
		group.Go(func() {
			exits <- classifyExit(job, jobsCtx, acceptCtx)
		})
	}
	done := make(chan struct{})
	go func() { group.Wait(); close(exits); close(done) }()
	ready.Store(true)
	log.Printf("wattharbor listening http=%s", httpPort.Addr())
	select {
	case <-ctx.Done():
	case err = <-exits:
	}
	ready.Store(false)
	stopAccept()
	deadline, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	select {
	case <-httpDone:
	case <-deadline.Done():
		return errors.Join(err, errShutdownTimeout)
	}
	stopSessions()
	stopJobs()
	select {
	case <-done:
	case <-deadline.Done():
		return errors.Join(err, errShutdownTimeout)
	}
	for failure := range exits {
		err = errors.Join(err, failure)
	}
	return err
}

type task struct {
	name string
	run  func(context.Context) error
}

func classifyExit(job task, ctx, stopping context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s panic: %v", job.name, p)
		}
	}()
	err = job.run(ctx)
	if err == nil && stopping.Err() == nil {
		return fmt.Errorf("%s exited unexpectedly", job.name)
	}
	if err == nil || errors.Is(err, context.Canceled) && stopping.Err() != nil {
		return nil
	}
	return fmt.Errorf("%s: %w", job.name, err)
}

// 超时后由进程退出收口，避免正在运行的处理器与主动关闭连接池竞争。
func closePrepared(err error, close func() error) error {
	if errors.Is(err, errShutdownTimeout) {
		return err
	}
	return errors.Join(err, close())
}
