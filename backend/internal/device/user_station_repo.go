package device

import (
	"context"
	"gorm.io/gorm"
)

// UserStationRepo 读取站点和设备运营资料，运行心跳由网关接口提供。
type UserStationRepo struct {
	DB *gorm.DB // central 所属连接或调用方事务
}

// userStationRow 保留 DECIMAL 坐标的字符串和可空距离。
type userStationRow struct {
	ID         uint64   `gorm:"column:id"`            // 站点主键
	Name       string   `gorm:"column:name"`          // 站名
	Address    *string  `gorm:"column:address"`       // 可空地址
	Longitude  string   `gorm:"column:longitude"`     // DECIMAL 经度
	Latitude   string   `gorm:"column:latitude"`      // DECIMAL 纬度
	Status     string   `gorm:"column:status"`        // 运营状态
	Phone      *string  `gorm:"column:contact_phone"` // 可空电话
	DistanceKM *float64 `gorm:"column:distance_km"`   // 公里；未定位保持 null
}

const userStationSelect = "id,name,address,CAST(longitude AS CHAR) AS longitude,CAST(latitude AS CHAR) AS latitude,status,contact_phone"

// List 查询启用站点，定位时在 SQL 分页前按距离排序，未定位按创建时间倒序。
// LEAST 限制 ACOS 参数，避免浮点舍入超出范围。
func (r UserStationRepo) List(ctx context.Context, q StationQuery) ([]userStationRow, error) {
	rows := []userStationRow{}
	if !q.Located {
		err := r.DB.WithContext(ctx).Table("station").Select(userStationSelect).Where("deleted_at IS NULL AND status='active'").Order("created_at DESC,id DESC").Limit(q.PageSize).Offset((q.Page - 1) * q.PageSize).Find(&rows).Error
		return rows, err
	}
	err := r.DB.WithContext(ctx).Raw(`SELECT * FROM (SELECT id,name,address,CAST(longitude AS CHAR) AS longitude,CAST(latitude AS CHAR) AS latitude,status,contact_phone,
	ROUND(6371*ACOS(LEAST(1,COS(RADIANS(?))*COS(RADIANS(latitude))*COS(RADIANS(longitude)-RADIANS(?))+SIN(RADIANS(?))*SIN(RADIANS(latitude)))),3) AS distance_km
	FROM station WHERE deleted_at IS NULL AND status='active') nearby WHERE distance_km<=? ORDER BY distance_km ASC,id ASC LIMIT ? OFFSET ?`, q.Latitude, q.Longitude, q.Latitude, q.RadiusKM, q.PageSize, (q.Page-1)*q.PageSize).Scan(&rows).Error
	return rows, err
}

// Find 查询启用且未删除的站点。
func (r UserStationRepo) Find(ctx context.Context, id uint64) (userStationRow, error) {
	var row userStationRow
	err := r.DB.WithContext(ctx).Table("station").Select(userStationSelect).Where("id=? AND deleted_at IS NULL AND status='active'", id).Take(&row).Error
	return row, err
}

// stationDeviceRow 保存未退役设备的编号和运营状态。
type stationDeviceRow struct {
	DeviceID string `gorm:"column:device_id"` // 设备业务号
	Status   string `gorm:"column:status"`    // central 运营状态
}

// Devices 按设备号读取站点中未删除、未退役设备，返回非 nil 数组。
func (r UserStationRepo) Devices(ctx context.Context, stationID uint64) ([]stationDeviceRow, error) {
	rows := []stationDeviceRow{}
	err := r.DB.WithContext(ctx).Table("device_meta").Select("device_id,status").Where("station_id=? AND deleted_at IS NULL AND status<>'retired'", stationID).Order("device_id ASC").Find(&rows).Error
	return rows, err
}
