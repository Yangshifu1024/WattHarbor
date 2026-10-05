package ops

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台的通用表读写：公告与后台账号（告警归device）。
// 这些表结构简单、没有跨聚合不变量，各自有专门的仓储；这里提供的是它们
// 共用的表级原语，以及"按 id 倒序列出整表"这种无过滤列表。

// AdminOpsStore 是运营类接口的读写入口。
type AdminOpsStore struct{ DB *gorm.DB }

// AdminListSpec 声明一个"按 id 倒序列出整表"的列表。
// Columns 在 SQL 表达式里就裁掉敏感列（如订阅密钥只取前 8 位），
// 不先查出来再丢弃。
type AdminListSpec struct {
	Table   string // 表名
	Columns string // 返回的列清单，可含 SQL 表达式
	Soft    bool   // 为真时过滤掉软删除行
}

// adminListRegistry 登记允许整表列出的运营资源。新增资源必须显式登记并给出列清单。
var adminListRegistry = map[string]AdminListSpec{

	"announcements": {Table: "announcement", Columns: "id,title,content,scope,target_ids,status,start_at,end_at", Soft: true},
}

// AdminListSpec 按资源名取列表声明，第二个返回值为 false 表示未登记。
func AdminListSpecOf(resource string) (AdminListSpec, bool) {
	spec, ok := adminListRegistry[resource]
	return spec, ok
}

// ListRows 按声明列出整表，返回通用行集合。字段的响应形态由处理器归一化。
func (s AdminOpsStore) ListRows(ctx context.Context, spec AdminListSpec) ([]map[string]any, error) {
	query := s.DB.WithContext(ctx).Table(spec.Table).Select(spec.Columns)
	if spec.Soft {
		query = query.Where("deleted_at IS NULL")
	}
	rows := []map[string]any{}
	if err := query.Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// LockRow 在行锁下读出整行，供更新前取快照或做状态机判断。
func (s AdminOpsStore) LockRow(tx *gorm.DB, table string, id uint64) (map[string]any, error) {
	var row map[string]any
	err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error
	return row, err
}

// InsertRow 插入一行并回填自增主键。调用方负责把这次写入与审计放进同一事务。
func (s AdminOpsStore) InsertRow(tx *gorm.DB, table string, values map[string]any) (uint64, error) {
	if err := tx.Table(table).Create(values).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// UpdateRow 按主键更新指定列。
func (s AdminOpsStore) UpdateRow(tx *gorm.DB, table string, id uint64, values map[string]any) error {
	return tx.Table(table).Where("id = ?", id).Updates(values).Error
}

// RolePermissionCodes 返回角色持有的权限编码。角色不存在或已软删除时返回空切片。
func (s AdminOpsStore) RolePermissionCodes(db *gorm.DB, roleID uint64) ([]string, error) {
	codes := []string{}
	err := db.Table("role AS r").
		Joins("JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id").
		Where("r.id=? AND r.deleted_at IS NULL", roleID).Pluck("p.code", &codes).Error
	return codes, err
}

// CreateAdminUser 落一条后台账号，返回新账号 ID。密码散列由调用方算好传入。
func (s AdminOpsStore) CreateAdminUser(tx *gorm.DB, row map[string]any) (uint64, error) {
	return s.InsertRow(tx, "admin_user", row)
}
