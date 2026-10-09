package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	gatewayruntime "chargepilot/internal/gateway/runtime"
	"chargepilot/internal/protocol"
)

// idleSink 提供不持久化的设备 sink，供应用启动失败路径装配。
type idleSink struct{}

func (idleSink) Register(context.Context, protocol.Registration) error { return nil }

func (idleSink) Record(context.Context, protocol.Event) error { return nil }

// idleAdapter 提供立即退出的协议夹具，不解析或持久化设备数据。
type idleAdapter struct{}

// idleStops 提供立即成功的停止恢复夹具，供进程组取消检查。
type idleStops struct{}

func (idleStops) RetryStopping(context.Context) error { return nil }

func (idleStops) RetryPending(context.Context) error { return nil }

func (idleAdapter) Name() string { return "test" }

func (idleAdapter) ServeConn(context.Context, net.Conn, protocol.Sink) error { return nil }

// TestTCPBindFailureStopsHTTPAndRetryLoop 验证设备端口绑定失败时，应用取消 HTTP 和停止重试，并等待两者退出。
func TestTCPBindFailureStopsHTTPAndRetryLoop(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	app := application{
		Server:  &http.Server{Addr: "127.0.0.1:0"},
		Devices: gatewayruntime.Runtime{Endpoints: []protocol.Endpoint{{Address: l.Addr().String(), Adapter: idleAdapter{}}}, Sink: idleSink{}, MaxConnections: 1, Compensation: idleStops{}, UserStops: idleStops{}},
	}
	finished := make(chan error, 1)
	go func() { finished <- app.Run(context.Background()) }()
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "listen ") {
			t.Fatalf("TCP bind error lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP or retry loop kept running after TCP failure")
	}
}
