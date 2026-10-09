// Package alerts 只保留设备告警同步的触发编排；状态与恢复在 device，证据由 gateway 提供。
package alerts

import (
	"context"
	"errors"
)

// CentralAlerts 是 worker 每秒触发所需的接口，不暴露 URL、令牌或数据库。
type CentralAlerts interface {
	// SyncDeviceAlerts 执行一次受管理同步，返回新告警数或调用／业务错误，不替 worker 重试。
	SyncDeviceAlerts(ctx context.Context) (int, error)
}

// Sync 只按节奏调用 central 能力，无设备规则或两个库的 IO。
type Sync struct {
	Central CentralAlerts /* cmd 装配的专用 central 客户端 */
}

// Run 触发一次同步，返回新告警数或未配置／调用错误，不启动后台任务。
func (s Sync) Run(ctx context.Context) (int, error) {
	if s.Central == nil {
		return 0, errors.New("device alert sync is not configured")
	}
	return s.Central.SyncDeviceAlerts(ctx)
}
