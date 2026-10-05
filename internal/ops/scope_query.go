package ops

import (
	"context"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
)

// ScopeQueryService 拥有后台当前账号的数据范围查询，供HTTP与导入执行前的窄能力注入。
// 只读取所属范围记录，不扩展账号权限，不开启新事务或持有长期快照。
type ScopeQueryService struct {
	DB *gorm.DB // central范围连接，池与既有事务生命周期由装配者管理。
}

// Load 在ctx下读取已鉴权actor当前站点／厂商范围，返回原集合或取消／存储错误。
// 无范围记录仍表示不受限；原连接的隔离及锁约束不改变，不写入范围。
func (s ScopeQueryService) Load(ctx context.Context, actor operations.Profile) (operations.DataScope, error) {
	return LoadDataScope(ctx, s.DB, actor)
}
