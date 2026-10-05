// Package alerts 只保留设备告警同步的触发编排；所属状态与恢复在device，证据由gateway提供。
package alerts

import (
	"context"
	"errors"
)

// CentralAlerts 是worker原每秒触发所需的窄能力，不暴露URL、令牌或数据库。
type CentralAlerts interface {
	// SyncDeviceAlerts 在ctx下执行一次受管理同步，返回新告警数或调用／业务错误，不替worker重试。
	SyncDeviceAlerts(ctx context.Context) (int, error)
}

// Sync 只按原节奏调用所属central能力，无设备规则或两个库的IO。
type Sync struct {
	Central CentralAlerts /* cmd装配的具名central客户端 */
}

// Run 在ctx下触发一次同步，返回原新告警数或未配置／调用错误，不启动后台任务。
func (s Sync) Run(ctx context.Context) (int, error) {
	if s.Central == nil {
		return 0, errors.New("device alert sync is not configured")
	}
	return s.Central.SyncDeviceAlerts(ctx)
}
