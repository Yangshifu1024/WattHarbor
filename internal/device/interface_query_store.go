package device

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// UserStationRow 是用户在地图上看到的站点。
// 经纬度在库里是 DECIMAL，这里 CAST 成字符串原样返回：调用方要拿它去算
// 距离，转成 float64 会引入精度损失，字符串让前端自己决定怎么用。
type UserStationRow struct {
	ID        uint64  `json:"id" gorm:"column:id"`
	Name      string  `json:"name" gorm:"column:name"`
	Address   *string `json:"address" gorm:"column:address"`
	Longitude string  `json:"longitude" gorm:"column:longitude"`
	Latitude  string  `json:"latitude" gorm:"column:latitude"`
	Status    string  `json:"status" gorm:"column:status"`
	Phone     *string `json:"contact_phone" gorm:"column:contact_phone"`
	// DistanceKM 只在按距离查询时填充；未定位分支下保持 nil，
	// 调用方据此表达"距离未知"，不得当成 0 公里。
	DistanceKM *float64 `json:"distance_km" gorm:"column:distance_km"`
}

// userStationSelect 刻意把经纬度 CAST 成字符串，见 UserStationRow 的说明。
const userStationSelect = "id, name, address, CAST(longitude AS CHAR) AS longitude, " +
	"CAST(latitude AS CHAR) AS latitude, status, contact_phone"

// ListActiveStations 返回启用中的站点，按创建时间倒序。
func ListActiveStations(ctx context.Context, db *gorm.DB, page, pageSize int) ([]UserStationRow, error) {
	rows := []UserStationRow{}
	err := db.WithContext(ctx).Table("station").Select(userStationSelect).
		Where("deleted_at IS NULL AND status = 'active'").
		Order("created_at DESC, id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error
	return rows, err
}

// FindStationForUser 按主键取一个未删除的站点，是否 active 由调用方判定。
func FindStationForUser(ctx context.Context, db *gorm.DB, id uint64) (UserStationRow, error) {
	var row UserStationRow
	err := db.WithContext(ctx).Table("station").Select(userStationSelect).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// ListStationScopedAnnouncements 返回站内可见的公告：全局公告加上指向本// ListStationDevices 返回站下未退役的设备，按设备号升序。
// StationDeviceRow 是站点详情里列出的单台设备。
type StationDeviceRow struct {
	DeviceID string `json:"device_id" gorm:"column:device_id"`
	Status   string `json:"status" gorm:"column:status"`
}

func ListStationDevices(ctx context.Context, db *gorm.DB, stationID uint64) ([]StationDeviceRow, error) {
	rows := []StationDeviceRow{}
	err := db.WithContext(ctx).Table("device_meta").Select("device_id, status").
		Where("station_id = ? AND deleted_at IS NULL AND status <> 'retired'", stationID).
		Order("device_id ASC").Find(&rows).Error
	return rows, err
}

// ListStationsNearBy 返回距给定坐标 radius 公里内的启用站点，按距离升序。
//

// 由调用方解码后做归属判断。
type UserAnnouncementRow struct {
	ID      uint64     `gorm:"column:id"`
	Title   string     `gorm:"column:title"`
	Content string     `gorm:"column:content"`
	Scope   string     `gorm:"column:scope"`
	RawIDs  *string    `gorm:"column:target_ids"`
	StartAt time.Time  `gorm:"column:start_at"`
	EndAt   *time.Time `gorm:"column:end_at"`
}

// 多取一些才够填满一页。这是取数，真正的分页在过滤之后。
func ListActiveAnnouncements(ctx context.Context, db *gorm.DB, now time.Time, offset, fetch int) ([]UserAnnouncementRow, error) {
	rows := []UserAnnouncementRow{}
	err := db.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now).
		Order("start_at DESC, id DESC").Offset(offset).Limit(fetch).Find(&rows).Error
	return rows, err
}

// 站点的公告。归属判断在调用方按 target_ids 做，这里只按 scope 取数。
func ListStationScopedAnnouncements(ctx context.Context, db *gorm.DB, now time.Time, limit int) ([]UserAnnouncementRow, error) {
	rows := []UserAnnouncementRow{}
	err := db.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND scope IN ('global','station') "+
			"AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now).
		Order("start_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// 否则 ACOS 的参数偶尔会略大于 1 而报 NaN。
func ListStationsNearBy(ctx context.Context, db *gorm.DB, latitude, longitude, radius float64, page, pageSize int) ([]UserStationRow, error) {
	rows := []UserStationRow{}
	err := db.WithContext(ctx).Raw(`
		SELECT * FROM (SELECT id, name, address,
		       CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude,
		       status, contact_phone,
		       ROUND(6371 * ACOS(LEAST(1, COS(RADIANS(?)) * COS(RADIANS(latitude)) * COS(RADIANS(longitude) - RADIANS(?))
		         + SIN(RADIANS(?)) * SIN(RADIANS(latitude)))), 3) AS distance_km
		FROM station
		WHERE deleted_at IS NULL AND status = 'active') nearby WHERE distance_km <= ?
		ORDER BY distance_km ASC, id ASC
		LIMIT ? OFFSET ?`,
		latitude, longitude, latitude, radius, pageSize, (page-1)*pageSize).Scan(&rows).Error
	return rows, err
}
