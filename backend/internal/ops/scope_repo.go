package ops

import (
	"context"

	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dataScopeRow 是 admin_data_scope 数据库记录，读取后合并为 operations.DataScope。
type dataScopeRow struct {
	AdminUserID uint64 `gorm:"column:admin_user_id"` // 账号 ID
	ScopeType   string `gorm:"column:scope_type"`    // 范围类型：station 限站点 / vendor 限厂商
	ScopeID     uint64 `gorm:"column:scope_id"`      // 该类型下的对象 ID
}

// TableName 指明 dataScopeRow 映射到 admin_data_scope。
func (dataScopeRow) TableName() string { return "admin_data_scope" }

// fieldMaskRow 是 admin_field_mask 数据库记录。
type fieldMaskRow struct {
	RoleID   uint64 `gorm:"column:role_id"`  // 规则所属角色 ID
	Resource string `gorm:"column:resource"` // 资源名，对应业务模块
	Field    string `gorm:"column:field"`    // 需打码的列
}

// TableName 指明 fieldMaskRow 映射到 admin_field_mask。
func (fieldMaskRow) TableName() string { return "admin_field_mask" }

// LoadDataScope 读取并合并账号的站点和厂商范围；无记录返回 Unrestricted=true。
func LoadDataScope(ctx context.Context, db *gorm.DB, p operations.Profile) (operations.DataScope, error) {
	return readDataScope(ctx, db, p)
}

// LockDataScopeInTx 用共享锁读取最新站点和厂商范围，使导出文件与保存的范围记录一致。
// 调用方先锁角色集合和账号，所有锁保持到发布事务结束。
func LockDataScopeInTx(ctx context.Context, tx *gorm.DB, p operations.Profile) (operations.DataScope, error) {
	return readDataScope(ctx, tx.Clauses(clause.Locking{Strength: "SHARE"}), p)
}

// readDataScope 在已有事务和锁约束下读取账号范围并合并。
func readDataScope(ctx context.Context, db *gorm.DB, p operations.Profile) (operations.DataScope, error) {
	rows := []dataScopeRow{}
	if err := db.WithContext(ctx).Where("admin_user_id = ?", p.ID).Find(&rows).Error; err != nil {
		return operations.DataScope{AdminUserID: p.ID}, err
	}
	items := make([]operations.DataScope, 0, len(rows))
	for _, row := range rows {
		items = append(items, operations.DataScope{ScopeType: row.ScopeType, ScopeID: row.ScopeID})
	}
	return operations.NewDataScope(p.ID, items), nil
}

// LoadFieldMask 读取角色脱敏规则；roleID=0 返回空规则，不脱敏。
func LoadFieldMask(ctx context.Context, db *gorm.DB, roleID uint64) (operations.FieldMask, error) {
	return readFieldMask(ctx, db, roleID)
}

// LockFieldMaskInTx 在集合和角色锁内用共享锁读取最新脱敏规则，保持对新增规则的事务隔离。
func LockFieldMaskInTx(ctx context.Context, tx *gorm.DB, roleID uint64) (operations.FieldMask, error) {
	return readFieldMask(ctx, tx.Clauses(clause.Locking{Strength: "SHARE"}), roleID)
}

// readFieldMask 在已有事务和锁约束下读取脱敏规则。
func readFieldMask(ctx context.Context, db *gorm.DB, roleID uint64) (operations.FieldMask, error) {
	mask := operations.FieldMask{RoleID: roleID}
	if roleID == 0 {
		return mask, nil
	}
	rows := []fieldMaskRow{}
	if err := db.WithContext(ctx).Where("role_id = ?", roleID).Find(&rows).Error; err != nil {
		return mask, err
	}
	mask.Fields = make([]operations.MaskedField, 0, len(rows))
	for _, row := range rows {
		mask.Fields = append(mask.Fields, operations.MaskedField{Resource: row.Resource, Field: row.Field})
	}
	return mask, nil
}
