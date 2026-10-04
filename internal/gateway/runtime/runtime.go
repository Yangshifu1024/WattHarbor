// Package runtime 管理设备接入与停止重试，不创建进程资源。
package runtime

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/listener"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

type Runtime struct {
	Endpoints      []protocol.Endpoint
	Sink           protocol.Sink
	MaxConnections int
	Compensation   interface{ RetryStopping(context.Context) error }
	UserStops      interface{ RetryPending(context.Context) error }
}

func (r Runtime) Serve(ctx context.Context) error {
	err := listener.Serve(ctx, r.Endpoints, r.Sink, r.MaxConnections)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (r Runtime) RetryStops(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if ctx.Err() != nil {
				continue
			}
			if err := r.Compensation.RetryStopping(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("STOP compensation: %v", err)
			}
			if ctx.Err() != nil {
				continue
			}
			if err := r.UserStops.RetryPending(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("user STOP retry: %v", err)
			}
		}
	}
}
