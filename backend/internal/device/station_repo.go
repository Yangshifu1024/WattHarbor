package device

import (
	"context"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Station 是站点对外资料，设备和计费配置均通过主键 ID 关联，不使用独立站点编码。
type Station struct {
	ID           uint64        `json:"id" gorm:"primaryKey"` // 站点主键，也是分账模板、计费规则、充电套餐引用站点的外键。
	Name         string        `json:"name"`                 // 站点名称，列表展示与关键词搜索都匹配它。
	Address      *string       `json:"address"`              // 站点地址，可空；关键词搜索会同时匹配名称和地址。
	Longitude    float64       `json:"longitude"`            // 经度，有效范围 [-180,180]，站点地图定位用。
	Latitude     float64       `json:"latitude"`             // 纬度，有效范围 [-90,90]。
	Status       StationStatus `json:"status"`               // 站点状态：active 营业中、disabled 停用、construction 建设中。
	ContactPhone *string       `json:"contact_phone"`        // 对外联系电话，可空，最长 32 字符。
}

// TableName 指定 station 表。
func (Station) TableName() string { return "station" }

// CountExistingStations 统计给定 ID 中未删除站点数量。
func CountExistingStations(ctx context.Context, db *gorm.DB, ids []uint64) (int64, error) {
	var found int64
	err := db.WithContext(ctx).Table("station").Where("id IN ? AND deleted_at IS NULL", ids).Count(&found).Error
	return found, err
}

// FindStation 按 ID 读取未删除站点，权限由调用方核对。
func FindStation(ctx context.Context, db *gorm.DB, id uint64) (Station, error) {
	var row Station
	err := db.WithContext(ctx).Table("station").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// LockStationForUpdate 在调用方事务中锁定未删除站点，返回更新前资料。
func LockStationForUpdate(tx *gorm.DB, id uint64) (Station, error) {
	var row Station
	err := tx.Table("station").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// CreateStationInTx 保存站点并回填主键，审计和提交由调用方负责。
func CreateStationInTx(tx *gorm.DB, row *Station) error { return tx.Create(row).Error }

// UpdateStation 在调用方事务中更新六项运营资料，不改变站点 ID。
func UpdateStation(tx *gorm.DB, row Station) error {
	return tx.Model(&Station{}).Where("id = ?", row.ID).Updates(map[string]any{
		"name": row.Name, "address": row.Address, "longitude": row.Longitude,
		"latitude": row.Latitude, "status": row.Status, "contact_phone": row.ContactPhone,
	}).Error
}

// ListStations 按站点范围分页倒序查询，转义关键词中的通配符。
func ListStations(ctx context.Context, db *gorm.DB, q persist.PageQuery, scopes ...operations.DataScope) (persist.Page[Station], error) {
	out := persist.Page[Station]{Items: []Station{}, Page: q.Page, PageSize: q.PageSize}
	query := db.WithContext(ctx).Model(&Station{}).Where("deleted_at IS NULL")
	if len(scopes) > 0 {
		query = ScopeStations(scopes[0], query, "id")
	}
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		pattern := dbutil.LikePattern(q.Keyword)
		query = query.Where("(name LIKE ? ESCAPE '!' OR address LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}
