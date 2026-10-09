package ops

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/persist"
	"context"
	"gorm.io/gorm"
)

// AuditQueryService 核对当前权限后分页读取后台审计。
type AuditQueryService struct {
	DB *gorm.DB /* 审计日志和后台账号数据库 */
}

// List 按 audit.read 权限读取全局审计；起始时间晚于截止时返回空列表。
// 模块和动作精确匹配，关键词由仓储转义，不附加设备范围。
func (s AuditQueryService) List(ctx context.Context, actor operations.Profile, q AuditQuery, page persist.PageQuery) (persist.Page[AuditRow], error) {
	if page.Page < 1 || page.Page > 1000000 || page.PageSize < 1 || page.PageSize > 100 {
		return persist.Page[AuditRow]{}, &ManagementInputError{Message: "分页参数无效"}
	}
	if _, err := readManagementActor(ctx, s.DB, actor, "audit.read"); err != nil {
		return persist.Page[AuditRow]{}, err
	}
	return (AuditRepo{DB: s.DB}).List(ctx, q, page.Page, page.PageSize)
}
