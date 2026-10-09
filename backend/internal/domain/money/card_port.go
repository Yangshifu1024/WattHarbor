// 端口发现由 charging 提供，card 只通过接口读取，由 cmd/central 注入。
package money

import "context"

// PortRef 是刷卡事务所需的最小端口信息（来自 scan 模块的实时查询）。
type PortRef struct {
	Found        bool   // 端口是否解析成功（scan 侧返回 200 且端口存在）
	Kind         string // 解析结果类型，刷卡要求 "port"
	DeviceID     string
	StationID    uint64
	DeviceStatus string // 设备服务状态，如 enabled
	PortID       string
	PortNo       uint8
	Online       bool
	Available    bool
}

// PortLookup 由 scan 模块实现：按端口编码查询实时端口状态，第二返回值为 HTTP 状态码语义。
type PortLookup interface {
	LookupPort(ctx context.Context, portID string) (PortRef, int)
}
