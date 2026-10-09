package ops

import (
	"context"

	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dataScopeRow 是 admin_data_scope 的一行落库形态，仅用于扫描；
// 归并后的内存范围是 operations.DataScope，不带 gorm 标记。
type dataScopeRow struct {
	AdminUserID uint64 `gorm:"column:admin_user_id"` // 账号 ID
	ScopeType   string `gorm:"column:scope_type"`    // 范围类型：station 限站点 / vendor 限厂商
	ScopeID     uint64 `gorm:"column:scope_id"`      // 该类型下的对象 ID
}

// TableName 指明 dataScopeRow 映射到 admin_data_scope。
func (dataScopeRow) TableName() string { return "admin_data_scope" }

// fieldMaskRow 是 admin_field_mask 的一行落库形态。
type fieldMaskRow struct {
	RoleID   uint64 `gorm:"column:role_id"`  // 规则所属角色 ID
	Resource string `gorm:"column:resource"` // 资源名，对应业务模块
	Field    string `gorm:"column:field"`    // 需打码的列
}

// TableName 指明 fieldMaskRow 映射到 admin_field_mask。
func (fieldMaskRow) TableName() string { return "admin_field_mask" }

// LoadDataScope 在ctx下从db读取p原账号的站点与厂商范围，返回归并集合或取消／存储错误，不写入。
// 无范围记录的账号返回 Unrestricted=true，此时不附加数据范围限制。
func LoadDataScope(ctx context.Context, db *gorm.DB, p operations.Profile) (operations.DataScope, error) {
	return readDataScope(ctx, db, p)
}

// LockDataScopeInTx 在ctx及调用方tx下当前共享锁读p原账号的范围，返回集合或存储错误。
// 导出持久范围凭据必须对应实际生成查询；调用方先锁角色集合及账号，锁持续到发布事务结束。
func LockDataScopeInTx(ctx context.Context, tx *gorm.DB, p operations.Profile) (operations.DataScope, error) {
	return readDataScope(ctx, tx.Clauses(clause.Locking{Strength: "SHARE"}), p)
}

// readDataScope 在ctx及db既有锁约束下读取p原账号范围，返回归并集合或存储错误，不写入。
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

// LoadFieldMask 在ctx下从db读取roleID绑定的遮蔽规则，返回规则集或取消／存储错误，不写入。
// roleID 为 0 表示不限角色，直接返回空规则集（不遮蔽）。
func LoadFieldMask(ctx context.Context, db *gorm.DB, roleID uint64) (operations.FieldMask, error) {
	return readFieldMask(ctx, db, roleID)
}

// LockFieldMaskInTx 在ctx及调用方tx下当前共享锁读roleID脱敏规则，返回集合或存储错误。
// 调用方先持角色集合及角色行锁；无规则时仍保留数据库对新增规则的事务隔离约束。
func LockFieldMaskInTx(ctx context.Context, tx *gorm.DB, roleID uint64) (operations.FieldMask, error) {
	return readFieldMask(ctx, tx.Clauses(clause.Locking{Strength: "SHARE"}), roleID)
}

// readFieldMask 在ctx及db既有锁约束下读取roleID遮蔽规则，返回原集合或存储错误，不写入。
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
