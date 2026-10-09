package admin

import (
	"context"

	"chargepilot/internal/domain/operations"
)

// ScopeReader 是后台HTTP消费者所需的当前账号范围读取能力，由cmd注入所属ops服务。
// 不暴露连接、事务、表名或业务写入能力。
type ScopeReader interface {
	// Load 在ctx下读取已鉴权actor当前范围，返回集合或读取／取消错误；无记录沿用不受限。
	Load(ctx context.Context, actor operations.Profile) (operations.DataScope, error)
}
