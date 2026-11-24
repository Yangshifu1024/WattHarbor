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

	u, closeUser, err := user.Prepare(ctx, client, notify)
	if err != nil {
		return fmt.Errorf("user: %w", err)
	}
	defer func() { err = closePrepared(err, closeUser) }()
	a, closeAdmin, err := admin.Prepare(ctx, client)
	if err != nil {
		return fmt.Errorf("admin: %w", err)
	}
	defer func() { err = closePrepared(err, closeAdmin) }()
	c, closeCentral, err := central.Prepare(ctx, client)
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

	transport["central"], transport["gateway"], transport["worker"] = c.Server.Handler, g.Server.Handler, w.Server.Handler

	devices, err := listener.Bind(g.Devices.Endpoints)
	if err != nil {
		return err
	}
	defer devices.Close()
	addr := os.Getenv("APP_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	httpPort, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen HTTP %s: %w", addr, err)
	}
	defer httpPort.Close()
	if ctx.Err() != nil {
		return nil
	}

	var ready, syncReady atomic.Bool
	w.Jobs.SyncReady = &syncReady
	w.Jobs.Wake = wake
	server := &http.Server{
		Addr: addr, Handler: publicRouter(a.Server.Handler, u.Server.Handler,
			map[string]http.Handler{
				"user": u.Server.Handler, "admin": a.Server.Handler, "central": c.Server.Handler,
				"gateway": g.Server.Handler, "worker": w.Server.Handler,
			}, &ready, &syncReady),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second,
	}
	acceptCtx, stopAccept := context.WithCancel(context.WithoutCancel(ctx))
	defer stopAccept()
	sessionCtx, stopSessions := context.WithCancel(context.WithoutCancel(ctx))
	defer stopSessions()
	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	httpDone := make(chan struct{})
	tasks := []task{
		{"http", func() error {
			defer close(httpDone)
			return httpapi.ServeOn(acceptCtx, server, httpPort, 5*time.Second)
		}},
		{"device", func() error { return devices.Serve(acceptCtx, sessionCtx, g.Devices.Sink, g.Devices.MaxConnections) }},
		{"sync", func() error { return w.Jobs.RunSync(jobsCtx) }},
		{"scheduler", func() error { return w.Jobs.RunScheduler(jobsCtx) }},
		{"finance", func() error { return w.Jobs.RunFinance(jobsCtx) }},
		{"retry-stops", func() error { return g.Devices.RetryStops(jobsCtx) }},
		{"cleanup-device", func() error { return g.Devices.CleanupSyncHistory(jobsCtx) }},
		{"cleanup-exports", func() error { return a.CleanupExports(jobsCtx) }},
	}
	var group sync.WaitGroup
	exits := make(chan error, len(tasks))
	for _, job := range tasks {
		group.Go(func() {
			exits <- classifyExit(job, acceptCtx)
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
	run  func() error
}

func classifyExit(job task, stopping context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s panic: %v", job.name, p)
		}
	}()
	err = job.run()
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
