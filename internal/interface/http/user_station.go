package http

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 站点查询：附近站点、站点详情及其下的公告与设备。
// stationRow 是站点列表的查询投影，坐标统一转为字符串以保留库中 DECIMAL 的原始精度。
// DistanceKM 只在按距离查询时填充；未定位分支下保持 nil。
type stationRow struct {
	ID         uint64   `gorm:"column:id"`
	Name       string   `gorm:"column:name"`
	Address    *string  `gorm:"column:address"`
	Longitude  string   `gorm:"column:longitude"`
	Latitude   string   `gorm:"column:latitude"`
	Status     string   `gorm:"column:status"`
	Phone      *string  `gorm:"column:contact_phone"`
	DistanceKM *float64 `gorm:"column:distance_km"`
}

func (a UserAccountAPI) nearbyStations(c *gin.Context) {
	longitude, latitude, hasCoordinates, ok := readCoordinates(c)
	if !ok {
		return
	}
	// 未开启定位时按创建时间倒序返回最新站点，默认 10 条；
	// 已定位时按距离返回，默认条数与其它列表接口一致。
	pageSizeDefault := 20
	if !hasCoordinates {
		pageSizeDefault = 10
	}
	page, pageSize, ok := readPagingDefault(c, pageSizeDefault)
	if !ok {
		return
	}
	radius := 50.0
	if raw := c.Query("radius_km"); raw != "" {
		var err error
		radius, err = strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(radius) || math.IsInf(radius, 0) || radius < 1 || radius > 50 {
			httpapi.BadRequest(c, "搜索半径须为1至50公里")
			return
		}
	}
	ctx := c.Request.Context()
	if !hasCoordinates {
		// 未开启定位时无法计算距离，distance_km 固定为 null，
		// 调用方须按“距离未知”展示，不得按 0 公里处理。
		rows := []stationRow{}
		err := a.AdminDB.WithContext(ctx).Table("station").
			Select("id, name, address, CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude, status, contact_phone").
			Where("deleted_at IS NULL AND status = 'active'").
			Order("created_at DESC, id DESC").
			Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error
		if err != nil {
			httpapi.Write(c, 503, 5003, "站点暂时无法读取", nil)
			return
		}
		items := make([]gin.H, 0, len(rows))
		for _, row := range rows {
			items = append(items, gin.H{
				"id": row.ID, "name": row.Name, "address": row.Address,
				"longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status,
				"contact_phone": row.Phone, "distance_km": nil,
			})
		}
		httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": pageSize, "located": false})
		return
	}
	// 距离由 SQL 直接基于库里的 DECIMAL 坐标算出，
	// 这样排序和分页都发生在结果被截断之前。
	rows := []stationRow{}
	err := a.AdminDB.WithContext(ctx).Raw(`
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
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点暂时无法读取", nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		if row.DistanceKM != nil && *row.DistanceKM > 50 {
			continue
		}
		items = append(items, gin.H{
			"id": row.ID, "name": row.Name, "address": row.Address,
			"longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status,
			"contact_phone": row.Phone, "distance_km": row.DistanceKM,
		})
	}
	httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": pageSize, "located": true})
}

func (a UserAccountAPI) stationDetail(c *gin.Context) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "站点 ID 无效")
		return
	}
	var row struct {
		ID        uint64  `gorm:"column:id"`
		Name      string  `gorm:"column:name"`
		Address   *string `gorm:"column:address"`
		Longitude string  `gorm:"column:longitude"`
		Latitude  string  `gorm:"column:latitude"`
		Status    string  `gorm:"column:status"`
		Phone     *string `gorm:"column:contact_phone"`
	}
	err = a.AdminDB.WithContext(c.Request.Context()).Table("station").
		Select("id, name, address, CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude, status, contact_phone").
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "站点不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点暂时无法读取", nil)
		return
	}
	if row.Status != "active" {
		httpapi.Write(c, 404, 1004, "站点不存在", nil)
		return
	}
	ctx := c.Request.Context()
	announcements, err := a.stationAnnouncements(ctx, row.ID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点公告暂时无法读取", nil)
		return
	}
	devices, err := a.stationDevices(ctx, row.ID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点设备暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"id": row.ID, "name": row.Name, "address": row.Address,
		"longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status,
		"contact_phone": row.Phone,
		"announcements": announcements, "devices": devices,
	})
}

// deviceOnlineWindow 是判定设备在线的心跳有效期。超过该窗口即视为离线。
const deviceOnlineWindow = time.Hour

// stationAnnouncements 返回对指定站点生效的公告：全部全局公告，加上把该站点
// 列入 target_ids 的站点维度公告。城市维度公告需要用户位置才能判定归属，
// 在站点详情这一无位置上下文中不做猜测，故不返回。
func (a UserAccountAPI) stationAnnouncements(ctx context.Context, stationID uint64) ([]gin.H, error) {
	now := time.Now().UTC()
	var rows []struct {
		ID      uint64     `gorm:"column:id"`
		Title   string     `gorm:"column:title"`
		Content string     `gorm:"column:content"`
		Scope   string     `gorm:"column:scope"`
		RawIDs  *string    `gorm:"column:target_ids"`
		StartAt time.Time  `gorm:"column:start_at"`
		EndAt   *time.Time `gorm:"column:end_at"`
	}
	if err := a.AdminDB.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND scope IN ('global','station') AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now).
		Order("start_at DESC, id DESC").Limit(50).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := []gin.H{}
	for _, row := range rows {
		if row.Scope == "station" && !announcementTargets(row.RawIDs)[strconv.FormatUint(stationID, 10)] {
			continue
		}
		items = append(items, gin.H{"id": row.ID, "title": row.Title, "content": row.Content, "start_at": row.StartAt, "end_at": row.EndAt})
	}
	return items, nil
}

// announcementTargets 把 target_ids 的 JSON 字符串数组解析成集合。
// 后台按字符串写入 id，这里保持同样的比较口径；解析失败时返回空集合。
func announcementTargets(raw *string) map[string]bool {
	targets := map[string]bool{}
	if raw == nil {
		return targets
	}
	var ids []string
	if err := json.Unmarshal([]byte(*raw), &ids); err != nil {
		return targets
	}
	for _, id := range ids {
		targets[id] = true
	}
	return targets
}

// stationDevices 返回站点下的设备及其在线状态。设备清单来自 central 的
// device_meta 冗余表，运行态心跳再向 gateway 批量补齐。
// 在线判定只依据最后心跳时间是否落在 deviceOnlineWindow 内；
// 网关不可用或设备从未上报时分别用 runtime_available 和 last_heartbeat_at
// 表达，不把未知状态当作离线。
func (a UserAccountAPI) stationDevices(ctx context.Context, stationID uint64) ([]gin.H, error) {
	var rows []struct {
		DeviceID string `gorm:"column:device_id"`
		Status   string `gorm:"column:status"`
	}
	if err := a.AdminDB.WithContext(ctx).Table("device_meta").
		Select("device_id, status").
		Where("station_id = ? AND deleted_at IS NULL AND status <> 'retired'", stationID).
		Order("device_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]gin.H, 0, len(rows))
	heartbeats := a.deviceHeartbeats(ctx, rows)
	now := time.Now().UTC()
	for _, row := range rows {
		summary, known := heartbeats[row.DeviceID]
		last := summary.LastHeartbeatAt
		online := known && last != nil && now.Sub(*last) < deviceOnlineWindow
		items = append(items, gin.H{
			"device_id": row.DeviceID, "status": row.Status,
			"last_heartbeat_at": last, "online": online, "runtime_available": known,
		})
	}
	return items, nil
}

// deviceHeartbeats 批量取回设备最后心跳。网关未配置或调用失败时返回空集合，
// 调用方据此把设备标为状态未知，而不是离线。
func (a UserAccountAPI) deviceHeartbeats(ctx context.Context, rows []struct {
	DeviceID string `gorm:"column:device_id"`
	Status   string `gorm:"column:status"`
}) map[string]struct {
	DeviceID        string     `json:"device_id"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
} {
	result := map[string]struct {
		DeviceID        string     `json:"device_id"`
		LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	}{}
	if len(rows) == 0 || a.GatewayURL == "" || a.ServiceToken == "" {
		return result
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.DeviceID)
	}
	query := url.Values{}
	for _, id := range ids {
		query.Add("device_id", id)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				DeviceID        string     `json:"device_id"`
				LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := (serviceclient.Client{Timeout: 5 * time.Second}).GetJSON(ctx, a.GatewayURL, a.ServiceToken, "/api/v1/internal/device-summaries?"+query.Encode(), &response); err != nil || response.Code != 0 {
		return result
	}
	for _, item := range response.Data.Items {
		result[item.DeviceID] = item
	}
	return result
}

// ---- 手机号 ----

// ---- 报修 ----
