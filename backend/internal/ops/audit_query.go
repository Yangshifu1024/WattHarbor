package ops

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
)

// AuditQueryService 拥有后台审计分页读取及实时权限准入，不参与原业务提交。
type AuditQueryService struct {
	DB *gorm.DB /* 所属审计及后台身份数据库 */
}

// List 在ctx下按actor当前audit.read权限及q／page读取原审计分页，返回输入／授权／存储错误。
// 保留原from大于to时空列表、模块及动作原精确匹配，关键词由repo转义，不按设备范围截断全局审计。
func (s AuditQueryService) List(ctx context.Context, actor operations.Profile, q AuditQuery, page persist.PageQuery) (persist.Page[AuditRow], error) {
	if page.Page < 1 || page.Page > 1000000 || page.PageSize < 1 || page.PageSize > 100 {
		return persist.Page[AuditRow]{}, &ManagementInputError{Message: "分页参数无效"}
	}
	if _, err := readManagementActor(ctx, s.DB, actor, "audit.read"); err != nil {
		return persist.Page[AuditRow]{}, err
	}
	return (AuditRepo{DB: s.DB}).List(ctx, q, page.Page, page.PageSize)
}
