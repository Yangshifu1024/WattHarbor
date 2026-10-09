package admin

import (
	"context"

	"chargepilot/internal/domain/operations"
)

// ScopeReader 是后台 HTTP 调用方所需的当前账号范围读取能力，由 cmd 注入 ops 服务。
// 不暴露连接、事务、表名或业务写入能力。
type ScopeReader interface {
	// Load 读取已鉴权 actor 当前范围，返回集合或读取／取消错误；无记录沿用不受限。
	Load(ctx context.Context, actor operations.Profile) (operations.DataScope, error)
}
