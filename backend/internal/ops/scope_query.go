package ops

import (
	"context"

	"gorm.io/gorm"
	"wattharbor/internal/domain/operations"
)

// ScopeQueryService 读取当前账号允许操作的站点和厂商，供 HTTP 和导入调用。
type ScopeQueryService struct {
	DB *gorm.DB // central 范围连接，池与既有事务生命周期由装配者管理。
}

// Load 读取已鉴权账号的数据范围；无记录表示不受范围限制。
func (s ScopeQueryService) Load(ctx context.Context, actor operations.Profile) (operations.DataScope, error) {
	return LoadDataScope(ctx, s.DB, actor)
}
