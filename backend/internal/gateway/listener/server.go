// Package listener 管理设备 TCP 监听、连接数量和取消后的退出等待。
package listener

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"chargepilot/internal/protocol"
)

// Serve 先绑定全部 TCP 端口，任一失败会关闭此前已打开的监听器。
// endpoints 须非空、地址唯一且适配器有效，sink 不能为 nil，maxConnections 须大于零。
// 连接上限由所有端点共享；取消或 Accept 失败后关闭监听及连接，等待全部处理器退出。
// 返回配置、监听或取消错误；endpoints 在运行期间不得并发修改，sink 由调用方关闭。
func Serve(ctx context.Context, endpoints []protocol.Endpoint, sink protocol.Sink, maxConnections int) error {
	ports, err := Bind(endpoints)
	if err != nil {
		return err
	}
	return ports.Serve(ctx, ctx, sink, maxConnections)
}

// Ports 持有已绑定的设备端口。
type Ports struct {
	listeners []net.Listener
	endpoints []protocol.Endpoint
}

// Bind 先占用全部设备端口，失败时释放此前绑定的端口。
func Bind(endpoints []protocol.Endpoint) (Ports, error) {
	if len(endpoints) == 0 {
		return Ports{}, errors.New("invalid protocol listener configuration")
	}
	listeners := make([]net.Listener, 0, len(endpoints))
	seen := make(map[string]bool, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Adapter == nil || endpoint.Address == "" || seen[endpoint.Address] {
			closeAll(listeners)
			return Ports{}, errors.New("duplicate address or missing adapter")
		}
		seen[endpoint.Address] = true
		bound, err := net.Listen("tcp", endpoint.Address)
		if err != nil {
			closeAll(listeners)
			return Ports{}, fmt.Errorf("listen device %s: %w", endpoint.Address, err)
		}
		listeners = append(listeners, bound)
	}
	return Ports{listeners: listeners, endpoints: endpoints}, nil
}

// Close 释放监听，允许在装配失败时清理。
func (p Ports) Close() { closeAll(p.listeners) }

// Serve 独立关闭接入和现有会话，让在途 HTTP 操作保留对设备的访问。
func (p Ports) Serve(acceptCtx, sessionCtx context.Context, sink protocol.Sink, maxConnections int) error {
	defer p.Close()
	if len(p.listeners) == 0 || sink == nil || maxConnections < 1 {
		return errors.New("invalid protocol listener configuration")
	}
	listeners, endpoints := p.listeners, p.endpoints
	sessions, cancelSessions := context.WithCancel(sessionCtx)
	defer cancelSessions()
	limit := make(chan struct{}, maxConnections)
	errorsOut := make(chan error, len(listeners))
	var acceptWG sync.WaitGroup
	var connWG sync.WaitGroup
	var connMu sync.Mutex
	active := make(map[net.Conn]struct{})
	for i, listener := range listeners {
		adapter := endpoints[i].Adapter
		log.Printf("device listener ready protocol=%s address=%s", adapter.Name(), listener.Addr())
		acceptWG.Go(func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					if acceptCtx.Err() == nil {
						errorsOut <- fmt.Errorf("accept %s: %w", adapter.Name(), err)
					}
					return
				}
				select {
				case limit <- struct{}{}:
					connMu.Lock()
					active[conn] = struct{}{}
					connMu.Unlock()
					connWG.Go(func() {
						defer func() {
							_ = conn.Close()
							connMu.Lock()
							delete(active, conn)
							connMu.Unlock()
							<-limit
						}()
						// 记录注册、协议解码等异常会话的结束原因；正常断开不记为错误。
						if err := adapter.ServeConn(sessions, conn, sink); err != nil && sessions.Err() == nil {
							log.Printf("device session ended on %s: %v", adapter.Name(), err)
						}
					})
				default:
					log.Printf("device connection rejected protocol=%s remote=%s reason=connection_limit", adapter.Name(), conn.RemoteAddr())
					_ = conn.Close()
				}
			}
		})
	}
	var result error
	select {
	case <-acceptCtx.Done():
		result = acceptCtx.Err()
	case err := <-errorsOut:
		result = err
	}
	// 先等待接收循环，防止活跃连接集合或连接 WaitGroup 在等待期间继续增长。
	closeAll(listeners)
	acceptWG.Wait()
	if acceptCtx.Err() != nil {
		<-sessionCtx.Done()
	}
	cancelSessions()
	connMu.Lock()
	for conn := range active {
		_ = conn.Close()
	}
	connMu.Unlock()
	connWG.Wait()
	return result
}

// closeAll 关闭全部监听器，解除 Accept 阻塞。
func closeAll(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}
