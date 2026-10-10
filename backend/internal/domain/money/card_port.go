// Package money 定义资金用例共用的退款、复核与刷卡查询参数，不依赖数据库。
package money

import (
	"chargepilot/internal/domain/devices"
	"context"
)

// PortRef 保存刷卡校验所需的设备、站点和端口状态。
type PortRef struct {
	Found        bool     // 端口是否解析成功（scan 侧返回 200 且端口存在）
	Kind         ScanKind // 解析结果类型，刷卡要求 ScanKindPort
	DeviceID     string
	StationID    uint64
	DeviceStatus devices.DeviceMetaStatus // 中央 device_meta.status，取值含 enabled
	PortID       string
	PortNo       uint8
	Online       bool
	Available    bool
}

// PortLookup 按端口编码查询状态；第二返回值是查询对应的 HTTP 状态码。
type PortLookup interface {
	// LookupPort 查询 portID 的当前状态；ctx 控制查询取消。
	LookupPort(ctx context.Context, portID string) (PortRef, int)
}
