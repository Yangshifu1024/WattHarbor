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

// Serve 在接收任何设备之前先把所有协议端口都绑定好。任何一个绑定失败
// 都不会留下半启动状态的监听器。
// ctx 为设备处理器共享的生命周期；endpoints 必须非空、地址不重复且适配器非 nil，sink 必须非 nil，maxConnections 至少为一。
// 返回配置或绑定错误、未取消时的 Accept 错误，或调用方取消错误；处理器错误只记录日志。
// 同时活跃连接上限作用于全部端点，超限连接立即关闭；所有端口绑定成功后才开始接收。
// 取消或 Accept 故障后先关监听、等待接收循环，再关闭活跃连接并等待处理器；Accept 故障不会自行取消 ctx，处理器须能随连接关闭返回。
// 本函数管理监听与连接，不关闭 sink；调用方持有的 endpoints 在运行中不得并发修改。
func Serve(ctx context.Context, endpoints []protocol.Endpoint, sink protocol.Sink, maxConnections int) error {
	if len(endpoints) == 0 || sink == nil || maxConnections < 1 {
		return errors.New("invalid protocol listener configuration")
	}
	listeners := make([]net.Listener, 0, len(endpoints))
	seen := make(map[string]bool, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Adapter == nil || endpoint.Address == "" || seen[endpoint.Address] {
			closeAll(listeners)
			return errors.New("duplicate address or missing adapter")
		}
		seen[endpoint.Address] = true
		listener, err := net.Listen("tcp", endpoint.Address)
		if err != nil {
			closeAll(listeners)
			return fmt.Errorf("listen %s: %w", endpoint.Address, err)
		}
		listeners = append(listeners, listener)
		log.Printf("device listener ready protocol=%s address=%s", endpoint.Adapter.Name(), listener.Addr())
	}
	defer closeAll(listeners)
	limit := make(chan struct{}, maxConnections)
	errorsOut := make(chan error, len(listeners))
	var acceptWG sync.WaitGroup
	var connWG sync.WaitGroup
	var connMu sync.Mutex
	active := make(map[net.Conn]struct{})
	for i, listener := range listeners {
		adapter := endpoints[i].Adapter
		acceptWG.Go(func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					if ctx.Err() == nil {
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
						if err := adapter.ServeConn(ctx, conn, sink); err != nil && ctx.Err() == nil {
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
	case <-ctx.Done():
		result = ctx.Err()
	case err := <-errorsOut:
		result = err
	}
	// 先等待接收循环，防止活跃连接集合或连接 WaitGroup 在等待期间继续增长。
	closeAll(listeners)
	acceptWG.Wait()
	connMu.Lock()
	for conn := range active {
		_ = conn.Close()
	}
	connMu.Unlock()
	connWG.Wait()
	return result
}

// closeAll 接收本次调用已打开的 listeners，逐一关闭以解除 Accept 阻塞；无返回值，关闭错误被忽略，不清空切片或等待连接处理器。
func closeAll(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}
