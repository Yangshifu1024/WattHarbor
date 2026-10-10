package listener

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/protocol"
)

// testAdapter 通知测试连接已进入适配器，然后等待取消。
type testAdapter struct {
	name protocol.AdapterID
	// connected 由测试持有的连接通知通道；适配器只发送名称，不关闭通道。
	connected chan<- string
}

// Name 返回适配器名称。
func (a testAdapter) Name() protocol.AdapterID { return a.name }

// ServeConn 发送连接进入通知，等待 context 取消并返回其错误；连接由 Serve 关闭。
func (a testAdapter) ServeConn(ctx context.Context, conn net.Conn, _ protocol.Sink) error {
	a.connected <- string(a.name)
	<-ctx.Done()
	return ctx.Err()
}

// testSink 忽略注册和事件，用于检查监听生命周期。
type testSink struct{}

// Register 忽略设备注册并返回 nil。
func (testSink) Register(context.Context, protocol.Registration) error { return nil }

// Record 忽略事件并返回 nil。
func (testSink) Record(context.Context, protocol.Event) error { return nil }

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
		result <- Serve(ctx, []protocol.Endpoint{{Address: ports[0], Adapter: testAdapter{protocol.AdapterID("dc589"), connected}}, {Address: ports[1], Adapter: testAdapter{protocol.AdapterID("future"), connected}}}, testSink{}, 2)
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
