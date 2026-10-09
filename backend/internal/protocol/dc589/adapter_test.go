package dc589

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"testing/synctest"
)

// gatedCloseConn 包装真实内存双向连接，在关闭已唤醒读取后保持 Close 在途，用确定性闸门观察取消 watcher 的退出等待。
type gatedCloseConn struct {
	// Conn 测试持有的真实 net.Pipe 本端，协议读取与 deadline 原样委托；清理由测试关闭连接。
	net.Conn
	// reading 仅首次 Read 进入时关闭，确保测试取消发生于受管理连接读取期间。
	reading chan struct{}
	// readOnce 保证多次协议读取不会重复关闭 reading。
	readOnce sync.Once
	// closing 连接已关闭时发送信号；信号之后 Close 仍等待 release，不表示 watcher 已退出。
	closing chan struct{}
	// release 由测试关闭，允许 watcher 的 Close 完成，不受 ctx 取消自动放行。
	release chan struct{}
}

// Read 接收 p 并原样委托 Conn.Read，首次调用发出 reading 信号；返回真实读取数量及错误，不解析或记录事件。
func (c *gatedCloseConn) Read(p []byte) (int, error) {
	c.readOnce.Do(func() { close(c.reading) })
	return c.Conn.Read(p)
}

// Close 先关闭真实 Conn 解除 Read 阻塞，再通知 closing 并等待 release；返回 Close 错误，不访问 sink 或存储。
// 夹具只由本次取消 watcher 调用一次，测试清理直接关闭底层 Conn。
func (c *gatedCloseConn) Close() error {
	err := c.Conn.Close()
	close(c.closing)
	<-c.release
	return err
}

// TestServeConnWaitsForCancellationWatcherClose 用真实 net.Pipe 的受控 Close 验证 ServeConn 不能在取消 watcher 尚未退出时返回。
// synctest.Wait 确定所有 goroutine 到达阻塞点；无墙钟睡眠、真实设备或数据库，失败路径也放行闸门并关闭双方连接。
func TestServeConnWaitsForCancellationWatcherClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn, peer := net.Pipe()
		defer conn.Close()
		defer peer.Close()
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		gated := &gatedCloseConn{Conn: conn, reading: make(chan struct{}), closing: make(chan struct{}), release: release}
		returned := make(chan error, 1)
		go func() { returned <- (TCPAdapter{}).ServeConn(ctx, gated, &recordingSink{}) }()
		<-gated.reading
		cancel()
		<-gated.closing
		synctest.Wait()
		select {
		case err := <-returned:
			t.Fatalf("ServeConn returned while cancellation watcher Close remained active: %v", err)
		default:
		}
		unblock()
		if err := <-returned; !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("closed connection error changed: %v", err)
		}
	})
}

// TestServeConnEarlyReadErrorDoesNotWaitForContext 关闭真实 net.Pipe 对端制造首帧 EOF，验证正常错误退出等待 watcher 而不要求取消 context。
// 返回仍保留 EOF，不注册设备或调用 sink；bubble 等待所有 goroutine，测试关闭本端连接。
func TestServeConnEarlyReadErrorDoesNotWaitForContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn, peer := net.Pipe()
		defer conn.Close()
		defer peer.Close()
		reading := make(chan struct{})
		release := make(chan struct{})
		close(release)
		gated := &gatedCloseConn{Conn: conn, reading: reading, closing: make(chan struct{}), release: release}
		returned := make(chan error, 1)
		go func() { returned <- (TCPAdapter{}).ServeConn(ctx, gated, &recordingSink{}) }()
		<-reading
		if err := peer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-returned; !errors.Is(err, io.EOF) {
			t.Fatalf("initial read error changed: %v", err)
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("early read failure required context cancellation: %v", err)
		}
		synctest.Wait()
		select {
		case <-gated.closing:
			t.Fatal("normal read failure invoked cancellation Close")
		default:
		}
	})
}
