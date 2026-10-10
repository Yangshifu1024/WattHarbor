package device

import (
	"context"
	"fmt"
	"math"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/ops"
	"gorm.io/gorm"
)

// StationAnnouncementReader 只提供已指定站点的公开公告，不写运营资料。
type StationAnnouncementReader interface {
	// StationAnnouncements 读取指定站点可见的公开公告。
	StationAnnouncements(ctx context.Context, stationID uint64) ([]ops.PublicAnnouncement, error)
}

// UserStationService 组合公开站点资料和运行状态，依赖由 cmd 注入。
type UserStationService struct {
	DB            *gorm.DB                  // 所属站点／设备只读连接
	Announcements StationAnnouncementReader // ops 提供的公开站点公告查询
	Summaries     SummaryReader             // 网关设备摘要；不可用保留未知
}

// StationQuery 保存分页、定位和公里半径。
type StationQuery struct {
	Page      int     // 从1开始的页码
	PageSize  int     // 每页1至100
	Located   bool    // 经纬度是否同时明确提供
	Longitude float64 // 明确提供的经度，-180至180
	Latitude  float64 // 明确提供的纬度，-90至90
	RadiusKM  float64 // 公里半径1至50，未定位仍核对输入
}

// StationInputError 表示站点查询参数不合法，不掩盖存储或依赖错误。
type StationInputError struct {
	Message string /* 公开错误文案 */
}

// Error 返回公开输入提示。
func (e *StationInputError) Error() string { return e.Message }

// StationQueryError 记录查询失败阶段，供 HTTP 返回对应提示。
type StationQueryError struct {
	Stage string // station、announcements 或 devices
	Cause error  // 存储／取消错误
}

// Error 返回失败阶段和底层错误。
func (e *StationQueryError) Error() string { return fmt.Sprintf("station %s: %v", e.Stage, e.Cause) }

// Unwrap 返回底层错误，供判定取消或缺失。
func (e *StationQueryError) Unwrap() error { return e.Cause }

// stationView 是公开站点字段，坐标保留字符串、ID 保留数字，并保留可空字段。
type stationView struct {
	ID        uint64        `json:"id"`            // 主键
	Name      string        `json:"name"`          // 站名
	Address   *string       `json:"address"`       // 可空地址
	Longitude string        `json:"longitude"`     // DECIMAL 经度
	Latitude  string        `json:"latitude"`      // DECIMAL 纬度
	Status    StationStatus `json:"status"`        // station.status 三值：active／disabled／construction
	Phone     *string       `json:"contact_phone"` // 可空电话
}

// stationListView 在站点资料上添加距离，未定位时距离为 null。
type stationListView struct {
	stationView          // 公开资料
	DistanceKM  *float64 `json:"distance_km"` // 公里；null 表示未知
}

// StationPage 保存列表分页和是否定位。
type StationPage struct {
	Items    []stationListView `json:"items"`     // 非 nil 数组
	Page     int               `json:"page"`      // 请求页码
	PageSize int               `json:"page_size"` // 请求每页数
	Located  bool              `json:"located"`   // 是否明确定位
}

// stationRuntimeView 组合中央运营状态和网关运行信息，未知时不补造心跳。
type stationRuntimeView struct {
	DeviceID         string                   `json:"device_id"`         // 设备业务号
	Status           devices.DeviceMetaStatus `json:"status"`            // 中央运营状态，词表见 domain/devices
	LastHeartbeatAt  *time.Time               `json:"last_heartbeat_at"` // 心跳；未知为 null
	Online           bool                     `json:"online"`            // 已知且心跳在1小时内
	RuntimeAvailable bool                     `json:"runtime_available"` // 是否读取到该设备的网关摘要
}

// StationDetail 返回站点、公告和设备，后两项为非 nil 数组，不附加距离字段。
type StationDetail struct {
	stationView                            // 公开资料
	Announcements []ops.PublicAnnouncement `json:"announcements"` // 当前可见公告
	Devices       []stationRuntimeView     `json:"devices"`       // 站点设备及运行状态；未收到心跳时返回 null
}

// Nearby 分页查询启用站点，有定位时按距离排序后截页。
// HTTP 未定位默认每页 10 条，定位默认 20 条，由调用方显式传入。
func (s UserStationService) Nearby(ctx context.Context, q StationQuery) (StationPage, error) {
	if q.Page < 1 || q.PageSize < 1 || q.PageSize > 100 {
		return StationPage{}, &StationInputError{"分页参数无效"}
	}
	if !finite(q.RadiusKM) || q.RadiusKM < 1 || q.RadiusKM > 50 {
		return StationPage{}, &StationInputError{"搜索半径须为1至50公里"}
	}
	if q.Located && (!finite(q.Longitude) || !finite(q.Latitude) || q.Longitude < -180 || q.Longitude > 180 || q.Latitude < -90 || q.Latitude > 90) {
		return StationPage{}, &StationInputError{"经纬度超出有效范围"}
	}
	rows, err := (UserStationRepo{DB: s.DB}).List(ctx, q)
	if err != nil {
		return StationPage{}, err
	}
	out := StationPage{Items: []stationListView{}, Page: q.Page, PageSize: q.PageSize, Located: q.Located}
	for _, row := range rows {
		if q.Located && row.DistanceKM != nil && *row.DistanceKM > 50 {
			continue
		}
		var distance *float64
		if q.Located {
			distance = row.DistanceKM
		}
		out.Items = append(out.Items, stationListView{stationPublicView(row), distance})
	}
	return out, nil
}

// Detail 先确认站点启用，再查询公告和设备；各阶段失败返回对应错误。
// 中央查询失败不能返回不完整详情，网关失败只将对应批次运行字段设为未知；取消后停止查询。
func (s UserStationService) Detail(ctx context.Context, id uint64) (StationDetail, error) {
	if id == 0 {
		return StationDetail{}, &StationInputError{"站点 ID 无效"}
	}
	repo := UserStationRepo{DB: s.DB}
	row, err := repo.Find(ctx, id)
	if err != nil {
		return StationDetail{}, &StationQueryError{"station", err}
	}
	if s.Announcements == nil {
		return StationDetail{}, &StationQueryError{"announcements", fmt.Errorf("announcement reader unavailable")}
	}
	announcements, err := s.Announcements.StationAnnouncements(ctx, id)
	if err != nil {
		return StationDetail{}, &StationQueryError{"announcements", err}
	}
	if announcements == nil {
		announcements = []ops.PublicAnnouncement{}
	}
	rows, err := repo.Devices(ctx, id)
	if err != nil {
		return StationDetail{}, &StationQueryError{"devices", err}
	}
	devices, err := s.runtime(ctx, rows)
	if err != nil {
		return StationDetail{}, &StationQueryError{"devices", err}
	}
	return StationDetail{stationPublicView(row), announcements, devices}, nil
}

// runtime 按设备顺序每 100 台查询一次摘要，共用五秒请求超时。
// 失败批次保留未知，不合并额外或重复设备；取消后不继续查询，结果为非 nil 数组。
func (s UserStationService) runtime(ctx context.Context, rows []stationDeviceRow) ([]stationRuntimeView, error) {
	heartbeats := map[string]contract.DeviceSummary{}
	if s.Summaries != nil && len(rows) > 0 {
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for start := 0; start < len(rows); start += 100 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if queryCtx.Err() != nil {
				break
			}
			end := min(start+100, len(rows))
			ids := make([]string, 0, end-start)
			allowed := map[string]bool{}
			for _, row := range rows[start:end] {
				ids = append(ids, row.DeviceID)
				allowed[row.DeviceID] = true
			}
			extra, err := s.Summaries.DeviceSummaries(queryCtx, ids)
			if err != nil {
				continue
			}
			seen := map[string]bool{}
			valid := true
			for _, summary := range extra {
				if !allowed[summary.DeviceID] || seen[summary.DeviceID] {
					valid = false
					break
				}
				seen[summary.DeviceID] = true
			}
			if valid {
				for _, summary := range extra {
					heartbeats[summary.DeviceID] = summary
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	items := make([]stationRuntimeView, 0, len(rows))
	for _, row := range rows {
		summary, known := heartbeats[row.DeviceID]
		last := summary.LastHeartbeatAt
		online := known && last != nil && now.Sub(*last) < time.Hour
		items = append(items, stationRuntimeView{row.DeviceID, row.Status, last, online, known})
	}
	return items, nil
}

// stationPublicView 只返回公开站点字段，不含数据库映射或距离。
func stationPublicView(row userStationRow) stationView {
	return stationView{row.ID, row.Name, row.Address, row.Longitude, row.Latitude, row.Status, row.Phone}
}

// finite 检查数值有限，禁止将 NaN 或无穷值传入 SQL。
func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
