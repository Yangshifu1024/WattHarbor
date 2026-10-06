package listener

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// testAdapter 通知真实 TCP 连接进入适配器并等待调用方取消，不解析设备帧。
type testAdapter struct {
	// name 适配器名称，用于区分连接进入哪个协议端点。
	name string
	// connected 由测试持有的连接通知通道；适配器只发送名称，不关闭通道。
	connected chan<- string
}

// Name 无入参，返回 a.name 协议标识，无副作用。
func (a testAdapter) Name() string { return a.name }

// ServeConn 接收 ctx、conn 与忽略的 sink，向 a.connected 通知名称后等待 ctx 取消并返回 ctx.Err()；不读写或关闭 conn，连接由 Serve 释放。
func (a testAdapter) ServeConn(ctx context.Context, conn net.Conn, _ protocol.Sink) error {
	a.connected <- a.name
	<-ctx.Done()
	return ctx.Err()
}

// testSink 提供无持久化的协议 sink，供多端口生命周期检查。
type testSink struct{}

// Register 忽略传入的 context 与设备注册资料，立即返回 nil；仅满足测试装配，不执行持久化。
func (testSink) Register(context.Context, protocol.Registration) error { return nil }

// Record 忽略传入的 context 与协议事件，立即返回 nil；仅满足测试装配，不执行持久化。
func (testSink) Record(context.Context, protocol.Event) error { return nil }

// TestServeMultipleProtocolPorts 使用 t 启动两个真实本地 TCP 端口，核对分别进入对应适配器且取消后 Serve 返回 context.Canceled；测试关闭客户端与监听，不写设备数据。
func TestServeMultipleProtocolPorts(t *testing.T) {
	ports := make([]string, 2)
	for i := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = listener.Addr().String()
		_ = listener.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan string, 2)
	result := make(chan error, 1)
	go func() {
		result <- Serve(ctx, []protocol.Endpoint{{Address: ports[0], Adapter: testAdapter{"dc589", connected}}, {Address: ports[1], Adapter: testAdapter{"future", connected}}}, testSink{}, 2)
	}()
	var wg sync.WaitGroup
	for _, address := range ports {
		wg.Add(1)
		go func(address string) {
			defer wg.Done()
			var conn net.Conn
			var err error
			for range 20 {
				conn, err = net.DialTimeout("tcp", address, 100*time.Millisecond)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Errorf("dial %s: %v", address, err)
				return
			}
			defer conn.Close()
		}(address)
	}
	wg.Wait()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-connected:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("only adapters %v accepted connections", seen)
		}
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not stop")
	}
}
