package device

import (
	"context"
	"time"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"gorm.io/gorm"
)

// AlertService 拥有后台告警查询、认领准入及审计事务；同步由AlertSyncService提供。
type AlertService struct {
	DB *gorm.DB /* central所属连接或调用方事务 */
}

// AlertInputError 表示告警准入错误，不将数据库故障转换为输入错误。
type AlertInputError struct {
	Message string /* 原公开准入文案 */
}

// Error 返回e的公开文案，无IO。
func (e *AlertInputError) Error() string { return e.Message }

// List 在ctx下按actor原alert.read和当前范围读取原告警数组，返回禁止／存储错误，无写入。
// 无范围记录仍不受限，受限查询只采用已有设备元数据，不补造未知设备身份。
func (s AlertService) List(ctx context.Context, actor operations.Profile) ([]map[string]any, error) {
	if !actor.HasPermission("alert.read") {
		return nil, operations.ErrForbidden
	}
	scope, err := ops.LoadDataScope(ctx, s.DB, actor)
	if err != nil {
		return nil, err
	}
	return (AlertRepo{DB: s.DB}).ListAlerts(ctx, scope)
}

// Acknowledge 在ctx下以audit原身份认领id，返回禁止／输入／缺失／冲突／存储错误。
// 锁原告警，范围内active推进acknowledged并同事务审计；已认领重放不更换原处理人或重复审计。
func (s AlertService) Acknowledge(ctx context.Context, audit ops.AuditContext, id uint64) error {
	if !audit.Actor.HasPermission("alert.ack") {
		return operations.ErrForbidden
	}
	if id == 0 {
		return &AlertInputError{"告警编号无效"}
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := AlertRepo{DB: tx}
		row, err := repo.LockAlert(tx, id)
		if err != nil {
			return err
		}
		scope, err := ops.LoadDataScope(ctx, tx, audit.Actor)
		if err != nil {
			return err
		}
		if !scope.Unrestricted {
			if _, err := FindDeviceByID(ctx, tx, scope, row.DeviceID); err != nil {
				return err
			}
		}
		if row.Status == "acknowledged" {
			return nil
		}
		if row.Status != "active" {
			return operations.ErrConflict
		}
		if err := repo.Acknowledge(tx, row, audit.Actor.ID, time.Now().UTC()); err != nil {
			return err
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "ack", Target: "alert", ID: id, Before: map[string]any{"status": row.Status}, After: map[string]any{"status": "acknowledged"}})
	})
}
