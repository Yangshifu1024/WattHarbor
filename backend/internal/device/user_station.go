package device

import (
	"context"
	"fmt"
	"math"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/ops"
	"gorm.io/gorm"
)

// StationAnnouncementReader 只提供已指定站点的公开公告，不写运营资料。
type StationAnnouncementReader interface {
	// StationAnnouncements 在ctx下返回stationID可见的原公告数组或存储错误，无写入。
	StationAnnouncements(ctx context.Context, stationID uint64) ([]ops.PublicAnnouncement, error)
}

// UserStationService 拥有公开站点查询及运行态组合；依赖由cmd管理和注入。
type UserStationService struct {
	DB            *gorm.DB                  // 所属站点／设备只读连接
	Announcements StationAnnouncementReader // ops所属公告能力
	Summaries     SummaryReader             // 网关原设备摘要；不可用保留未知
}

// StationQuery 表达已解析的分页、定位及公里半径，不持有HTTP或服务凭据。
type StationQuery struct {
	Page      int     // 从1开始的页码
	PageSize  int     // 每页1至100
	Located   bool    // 经纬度是否同时明确提供
	Longitude float64 // 明确提供的经度，-180至180
	Latitude  float64 // 明确提供的纬度，-90至90
	RadiusKM  float64 // 公里半径1至50，未定位仍核对原输入
}

// StationInputError 表示站点用例准入错误，不掩盖存储或依赖错误。
type StationInputError struct {
	Message string /* 公开错误文案 */
}

// Error 返回e的公开文案，无IO。
func (e *StationInputError) Error() string { return e.Message }

// StationQueryError 记录组合读取失败阶段供HTTP保留原公开文案。
type StationQueryError struct {
	Stage string // station、announcements或devices
	Cause error  // 原存储／取消错误
}

// Error 返回e的阶段及原错误，无IO。
func (e *StationQueryError) Error() string { return fmt.Sprintf("station %s: %v", e.Stage, e.Cause) }

// Unwrap 返回e的原错误，使取消与缺失仍可判定，无IO。
func (e *StationQueryError) Unwrap() error { return e.Cause }

// stationView 保留原公开坐标字符串、数字ID和可空字段，不含ORM映射。
type stationView struct {
	ID        uint64  `json:"id"`            // 原主键
	Name      string  `json:"name"`          // 站名
	Address   *string `json:"address"`       // 可空地址
	Longitude string  `json:"longitude"`     // 原DECIMAL经度
	Latitude  string  `json:"latitude"`      // 原DECIMAL纬度
	Status    string  `json:"status"`        // 原运营状态
	Phone     *string `json:"contact_phone"` // 可空电话
}

// stationListView 在原站点资料上增加距离，未定位为null。
type stationListView struct {
	stationView          // 原公开资料
	DistanceKM  *float64 `json:"distance_km"` // 公里；null表示未知
}

// StationPage 保留原公开列表分页和定位标志。
type StationPage struct {
	Items    []stationListView `json:"items"`     // 非nil数组
	Page     int               `json:"page"`      // 请求页码
	PageSize int               `json:"page_size"` // 请求每页数
	Located  bool              `json:"located"`   // 是否明确定位
}

// stationRuntimeView 组合原中央运营状态和网关运行事实，未知不伪造心跳。
type stationRuntimeView struct {
	DeviceID         string     `json:"device_id"`         // 原设备业务号
	Status           string     `json:"status"`            // 中央运营状态
	LastHeartbeatAt  *time.Time `json:"last_heartbeat_at"` // 原心跳；未知为null
	Online           bool       `json:"online"`            // 已知且原心跳在1小时内
	RuntimeAvailable bool       `json:"runtime_available"` // 是否获得该设备的网关事实
}

// StationDetail 保留原详情形状，公告与设备均为非nil数组，不附加distance_km。
type StationDetail struct {
	stationView                            // 原公开资料
	Announcements []ops.PublicAnnouncement `json:"announcements"` // 当前可见公告
	Devices       []stationRuntimeView     `json:"devices"`       // 原站点设备与nullable运行态
}

// Nearby 在ctx下读取q指定的站点页，返回原投影或准入／存储错误，不写资料。
// 未定位由HTTP保留默认10，定位默认20；调用者传显式分页，距离在SQL分页前排序。
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

// Detail 在ctx下读取id的启用站点，先确认存在再取公告和设备，返回原组合或带阶段错误。
// 公告／设备存储失败不返回伪完整详情；网关失败只降级该批运行态；取消停止后续取证。
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

// runtime 在ctx下按rows原顺序每100台取一次摘要，返回非nil设备投影或取消错误。
// 总取证5秒、顺序执行无后台任务；独立失败批未知，不把额外／重复身份或未来补造值写入资料。
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

// stationPublicView 从row返回原公开字段，不返回ORM列或额外距离，无IO。
func stationPublicView(row userStationRow) stationView {
	return stationView{row.ID, row.Name, row.Address, row.Longitude, row.Latitude, row.Status, row.Phone}
}

// finite 返回value是否有限，NaN和无限值不进入SQL，无IO。
func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
