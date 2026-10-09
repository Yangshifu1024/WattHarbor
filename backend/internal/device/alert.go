package device

import (
	"context"
	"time"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"gorm.io/gorm"
)

// AlertService 负责后台告警查询、认领校验和审计，告警同步由 AlertSyncService 处理。
type AlertService struct {
	DB *gorm.DB /* central 所属连接或调用方事务 */
}

// AlertInputError 表示告警请求不合法；数据库故障不能转换成输入错误。
type AlertInputError struct {
	Message string /* 公开校验提示 */
}

// Error 返回公开输入提示。
func (e *AlertInputError) Error() string { return e.Message }

// List 核对 alert.read 权限后读取当前范围内告警，无范围记录时不限制。
// 受限范围只依据现有设备资料，不能补造未知设备。
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

// Acknowledge 锁定范围内 active 告警后改为 acknowledged，保存处理人与审计。
// 已认领重放不换处理人，不重复审计。
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
