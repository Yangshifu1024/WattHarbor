package database

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台的通用表读写：告警、公告、Webhook 订阅、白标配置与后台账号。
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
	"alerts":        {Table: "alert_event", Columns: "id,device_id,severity,metric,status,created_at"},
	"announcements": {Table: "announcement", Columns: "id,title,content,scope,target_ids,status,start_at,end_at", Soft: true},
	"webhooks":      {Table: "webhook_subscription", Columns: "id,name,url,enabled,event_types,LEFT(secret,8) AS secret_prefix", Soft: true},
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
	return LastInsertID(tx)
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

// AlertStatus 是在行锁下读出的告警状态。状态机规则在 operations，这里只报告事实。
type AlertStatus struct {
	Status string // active 未认领 / acknowledged 已认领 / 其余终态不可再认领
}

// LockAlertStatus 在行锁下读出告警的当前状态。
func (s AdminOpsStore) LockAlertStatus(tx *gorm.DB, id uint64) (AlertStatus, error) {
	var row struct{ Status string }
	err := tx.Table("alert_event").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
	return AlertStatus{Status: row.Status}, err
}

// AckAlert 把一条 active 告警置为 acknowledged 并记下认领人与时刻。
func (s AdminOpsStore) AckAlert(tx *gorm.DB, id, actorID uint64, at time.Time) error {
	return s.UpdateRow(tx, "alert_event", id, map[string]any{
		"status": "acknowledged", "acked_by": actorID, "acked_at": at,
	})
}

// CreateWebhookSubscription 落一条订阅，返回新订阅 ID。
// secret 只在创建响应里返回一次，之后的查询一律不返回完整值。
func (s AdminOpsStore) CreateWebhookSubscription(tx *gorm.DB, row map[string]any) (uint64, error) {
	return s.InsertRow(tx, "webhook_subscription", row)
}

// WhitelabelConfigJSON 读出白标配置原文。该表固定只有 id=1 一行；
// 没有记录时返回 nil, nil，表示尚未配置过，调用方用内置默认值。
func (s AdminOpsStore) WhitelabelConfigJSON(ctx context.Context) (*string, error) {
	var row struct{ ConfigJSON *string }
	err := s.DB.WithContext(ctx).Table("whitelabel_config").Where("id=1").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 尚未配置过是正常状态，不是错误：调用方据此套用内置默认值。
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row.ConfigJSON, nil
}

// SaveWhitelabel 整体覆盖 id=1 的白标配置。mini_program_name 与 theme_color
// 另有独立列，供不带配置 JSON 的查询直接使用。
func (s AdminOpsStore) SaveWhitelabel(tx *gorm.DB, miniProgramName, themeColor, configJSON string) error {
	return tx.Exec(
		`INSERT INTO whitelabel_config (id,name,mini_program_name,theme_color,config_json)
		 VALUES (1,?,?,?,?)
		 ON DUPLICATE KEY UPDATE mini_program_name=VALUES(mini_program_name),theme_color=VALUES(theme_color),config_json=VALUES(config_json)`,
		"default", miniProgramName, themeColor, configJSON).Error
}
