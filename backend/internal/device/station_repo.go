package device

import (
	"context"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Station 是站点的对外模型，映射 central_db 的 station 表。站点是整套计费配置的
// 挂载点——分账模板、计费规则、充电套餐都挂在站点 ID 上。站点已无独立的业务编码
// 字段，列表、详情和所有下级配置的引用一律使用主键 ID。
type Station struct {
	ID           uint64  `json:"id" gorm:"primaryKey"` // 站点主键，也是分账模板、计费规则、充电套餐引用站点的外键。
	Name         string  `json:"name"`                 // 站点名称，列表展示与关键词搜索都匹配它。
	Address      *string `json:"address"`              // 站点地址，可空；关键词搜索会同时匹配名称和地址。
	Longitude    float64 `json:"longitude"`            // 经度，有效范围 [-180,180]，站点地图定位用。
	Latitude     float64 `json:"latitude"`             // 纬度，有效范围 [-90,90]。
	Status       string  `json:"status"`               // 站点状态：active 营业中、disabled 停用、construction 建设中。
	ContactPhone *string `json:"contact_phone"`        // 对外联系电话，可空，最长 32 字符。
}

// TableName 返回 Station 的固定表名，无参数、IO 或修改。
func (Station) TableName() string { return "station" }

// CountExistingStations 在 ctx 下用 db 统计 ids 中未删除站点；返回数量或存储错误，只读。
func CountExistingStations(ctx context.Context, db *gorm.DB, ids []uint64) (int64, error) {
	var found int64
	err := db.WithContext(ctx).Table("station").Where("id IN ? AND deleted_at IS NULL", ids).Count(&found).Error
	return found, err
}

// FindStation 在 ctx 下用 db 按 id 取未删除站点；返回资料、缺失或存储错误，不负责权限核对。
func FindStation(ctx context.Context, db *gorm.DB, id uint64) (Station, error) {
	var row Station
	err := db.WithContext(ctx).Table("station").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// LockStationForUpdate 在调用方 tx 中锁定未删除的 id，返回更新前快照、缺失或存储错误；不提交事务。
func LockStationForUpdate(tx *gorm.DB, id uint64) (Station, error) {
	var row Station
	err := tx.Table("station").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// CreateStationInTx 在调用方 tx 中创建 row 并回填主键；返回存储错误，不审计或提交事务。
func CreateStationInTx(tx *gorm.DB, row *Station) error { return tx.Create(row).Error }

// UpdateStation 在调用方 tx 中只写 row 的六项运营资料；返回存储错误，不改变身份或提交事务。
func UpdateStation(tx *gorm.DB, row Station) error {
	return tx.Model(&Station{}).Where("id = ?", row.ID).Updates(map[string]any{
		"name": row.Name, "address": row.Address, "longitude": row.Longitude,
		"latitude": row.Latitude, "status": row.Status, "contact_phone": row.ContactPhone,
	}).Error
}

// ListStations 在 ctx 下用 db 按 q 分页，scopes 首项存在时限定站点范围。
// 返回未删除资料及总数或存储错误；按 ID 倒序，转义搜索通配符，只读。
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
