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

// Register 忽略传入的 context 与设备注册资料，立即返回 nil；仅满足测试装配，不执行持久化。
func (idleSink) Register(context.Context, protocol.Registration) error { return nil }

// Record 忽略传入的 context 与协议事件，立即返回 nil；仅满足测试装配，不执行持久化。
func (idleSink) Record(context.Context, protocol.Event) error { return nil }

// idleAdapter 提供立即退出的协议夹具，不解析或持久化设备数据。
type idleAdapter struct{}

// idleStops 提供立即成功的停止恢复夹具，供进程组取消检查。
type idleStops struct{}

// RetryStopping 忽略传入的 context，立即返回 nil，不扫描命令或发送设备指令。
func (idleStops) RetryStopping(context.Context) error { return nil }

// RetryPending 忽略传入的 context，立即返回 nil，不扫描命令或发送设备指令。
func (idleStops) RetryPending(context.Context) error { return nil }

// Name 无入参，返回固定协议名 test；无副作用。
func (idleAdapter) Name() string { return "test" }

// ServeConn 忽略传入的 context、连接与 sink，立即返回 nil，不解析、关闭或持久化连接；连接关闭由监听服务负责。
func (idleAdapter) ServeConn(context.Context, net.Conn, protocol.Sink) error { return nil }

// TestTCPBindFailureStopsHTTPAndRetryLoop 使用 t 占用设备 TCP 地址，确认应用保留绑定错误并取消、等待 HTTP 与停止重试任务退出；测试关闭监听，不写设备或订单。
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
