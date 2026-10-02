package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
)

// Device 是充电桩（设备）的列表/详情模型，取自 central_db.device_meta 并左连站点带出
// 站点名。站点统一用主键 ID 关联，列表不再返回站点编码，关键词搜索也不再匹配站点编码。
type Device struct {
	ID               uint64             `json:"id"`           // 设备在 central_db 的内部主键，仅用于列表排序。
	DeviceID         string             `json:"device_id"`    // 设备编号（device_id），最长 64 字符，是设备对外的业务标识，也是详情接口的寻址键。
	StationID        *uint64            `json:"station_id"`   // 所属站点 ID，可空表示尚未归属站点。
	StationName      *string            `json:"station_name"` // 所属站点名称，联表带出；站点被软删除时为空。
	VendorID         *uint64            `json:"vendor_id"`    // 设备厂商 ID，可空。
	VendorName       *string            `json:"vendor_name" gorm:"-"`
	LastHeartbeatAt  *time.Time         `json:"last_heartbeat_at" gorm:"-"`
	SignalStrength   *uint8             `json:"signal_strength" gorm:"-"` // 最新设备信号；未上报时为空，0 仍是有效读数。
	SignalAt         *time.Time         `json:"signal_at" gorm:"-"`
	Ports            []DevicePortStatus `json:"ports" gorm:"-"` // 当前端口状态；未读取到端口时返回空数组。
	RuntimeAvailable bool               `json:"runtime_available" gorm:"-"`
	Model            *string            `json:"model"`     // 设备型号，可空；关键词搜索会匹配它。
	SerialNo         *string            `json:"serial_no"` // 设备出厂序列号，可空。
	Status           string             `json:"status"`    // 设备状态：enabled 启用、disabled 停用、retired 退役、fault 故障。
	WarrantyUntil    *time.Time         `json:"warranty_until"`
	Tags             []string           `json:"tags" gorm:"-"` // 对外标签数组；数据库 NULL 统一读为空数组。
	TagsJSON         *string            `json:"-" gorm:"column:tags_json"`
	UpdatedAt        time.Time          `json:"updated_at"` // 资料编辑的并发校验值，使用 UTC 毫秒精度。
	// 设备协议支持的计量能力与当前计费模式，用于展示计费兼容性。
	ProtocolAdapter       string `json:"protocol_adapter"`
	ChargeMode            string `json:"charge_mode"`                      // 该设备当前实际生效的计费方式，取自 pricing 引擎的 charging.ChargeMode（server_realtime_power / server_max_power / server_energy / device_duration / device_energy / device_power）。
	ReportsEnergy         bool   `json:"reports_energy" gorm:"-"`          // 协议是否支持电量上报；按电量计费要求此能力。
	ReportsSegmentedPower bool   `json:"reports_segmented_power" gorm:"-"` // 协议是否支持分段功率上报；按功率计费要求此能力。
}

// DevicePortStatus 保留设备原始状态码和采样时刻，未上报的状态不默认为空闲。
type DevicePortStatus struct {
	PortNo     uint8      `json:"port_no"`
	StatusCode *uint8     `json:"status_code"`
	StatusAt   *time.Time `json:"status_at"`
}

// deviceQuery 组装设备的基础查询：device_meta 左连未删除的站点，并排除已软删除的设备。
// 设备的所有查询都从这里派生，保证站点名口径和过滤条件一致。
func DeviceQuery(ctx context.Context, db *gorm.DB) *gorm.DB {
	return db.WithContext(ctx).Table("device_meta AS d").Joins("LEFT JOIN station AS s ON s.id=d.station_id AND s.deleted_at IS NULL").Where("d.deleted_at IS NULL")
}

// DeviceColumns 是设备列表和详情共用的查询列。
const DeviceColumns = "d.id,d.device_id,d.station_id,d.vendor_id,d.model,d.serial_no,d.status,d.warranty_until,d.tags_json,d.updated_at,d.charge_mode,d.protocol_adapter,s.name AS station_name"

// normalizeMetadata 隐藏内部 JSON 存储形式，并让所有日期保持数据库的 UTC 毫秒精度。
func (d *Device) NormalizeMetadata() error {
	d.Tags = []string{}
	if d.Ports == nil {
		d.Ports = []DevicePortStatus{}
	}
	if d.TagsJSON != nil {
		if err := json.Unmarshal([]byte(*d.TagsJSON), &d.Tags); err != nil {
			return err
		}
		if d.Tags == nil {
			d.Tags = []string{}
		}
	}
	d.UpdatedAt = d.UpdatedAt.UTC().Truncate(time.Millisecond)
	if d.WarrantyUntil != nil {
		*d.WarrantyUntil = d.WarrantyUntil.UTC().Truncate(time.Millisecond)
	}
	return nil
}

// Devices 分页查询设备，支持按状态过滤和按设备编号/型号/站点名模糊搜索（通配符已转义）。
// 先 Count 再按内部主键倒序取当页。注意关键词不再匹配站点编码。
func ListDevices(ctx context.Context, db *gorm.DB, q PageQuery, scopes ...operations.DataScope) (Page[Device], error) {
	out := Page[Device]{Items: []Device{}, Page: q.Page, PageSize: q.PageSize}
	query := DeviceQuery(ctx, db)
	if q.StationID > 0 {
		query = query.Where("d.station_id=?", q.StationID)
	}
	if len(scopes) > 0 {
		query = ScopeStations(scopes[0], query, "d.station_id")
		query = ScopeVendors(scopes[0], query, "d.vendor_id")
	}
	if q.Status != "" {
		query = query.Where("d.status = ?", q.Status)
	}
	if q.Keyword != "" {
		v := dbutil.LikePattern(q.Keyword)
		query = query.Where("(d.device_id LIKE ? ESCAPE '!' OR d.model LIKE ? ESCAPE '!' OR s.name LIKE ? ESCAPE '!')", v, v, v)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select(DeviceColumns).Order("d.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error
	if err != nil {
		return out, err
	}
	for i := range out.Items {
		if err := out.Items[i].NormalizeMetadata(); err != nil {
			return out, err
		}
		cap, _ := charging.ProtocolCapabilities(out.Items[i].ProtocolAdapter)
		out.Items[i].ReportsEnergy = cap.ReportsEnergy
		out.Items[i].ReportsSegmentedPower = cap.ReportsSegmentedPower
	}
	return out, err
}

// paystore.DeviceInput 要求完整提交资料字段；RawMessage 区分缺失字段和显式 null，避免意外清空。
type DeviceInput struct {
	Model             json.RawMessage `json:"model"`
	SerialNo          json.RawMessage `json:"serial_no"`
	WarrantyUntil     json.RawMessage `json:"warranty_until"`
	Tags              json.RawMessage `json:"tags"`
	ExpectedUpdatedAt json.RawMessage `json:"expected_updated_at"`
}

// Metadata 把请求体解析成设备资料行与并发校验基准时间。
func (in DeviceInput) Metadata() (Device, time.Time, error) {
	var row Device
	var expected time.Time
	for _, value := range []json.RawMessage{in.Model, in.SerialNo, in.WarrantyUntil, in.Tags, in.ExpectedUpdatedAt} {
		if len(value) == 0 {
			return row, expected, fmt.Errorf("请完整提交设备资料和原更新时间")
		}
	}
	text := func(raw json.RawMessage) (*string, error) {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("型号和序列号必须是文本或 null")
		}
		if value == nil {
			return nil, nil
		}
		trimmed := strings.TrimSpace(*value)
		if utf8.RuneCountInString(trimmed) > 128 {
			return nil, fmt.Errorf("型号和序列号不能超过 128 个字符")
		}
		if trimmed == "" {
			return nil, nil
		}
		return &trimmed, nil
	}
	date := func(raw json.RawMessage) (*time.Time, error) {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("日期必须是带时区的 ISO 8601 时间或 null")
		}
		if value == nil {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			return nil, fmt.Errorf("日期必须是带时区的 ISO 8601 时间或 null")
		}
		parsed = parsed.UTC().Truncate(time.Millisecond)
		if parsed.Year() < 1000 || parsed.Year() > 9999 {
			return nil, fmt.Errorf("日期年份必须在 1000 至 9999 之间")
		}
		return &parsed, nil
	}
	var err error
	if row.Model, err = text(in.Model); err != nil {
		return row, expected, err
	}
	if row.SerialNo, err = text(in.SerialNo); err != nil {
		return row, expected, err
	}
	if row.WarrantyUntil, err = date(in.WarrantyUntil); err != nil {
		return row, expected, err
	}
	if err := json.Unmarshal(in.Tags, &row.Tags); err != nil || row.Tags == nil || len(row.Tags) > 20 {
		return row, expected, fmt.Errorf("标签必须是数组，最多 20 项")
	}
	seen := map[string]bool{}
	for i, tag := range row.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || utf8.RuneCountInString(tag) > 32 || seen[tag] {
			return row, expected, fmt.Errorf("标签不能为空、重复或超过 32 个字符")
		}
		seen[tag] = true
		row.Tags[i] = tag
	}
	expectedAt, err := date(in.ExpectedUpdatedAt)
	if err != nil || expectedAt == nil {
		return row, expected, fmt.Errorf("请提供设备原更新时间 expected_updated_at")
	}
	return row, *expectedAt, nil
}

// nextDeviceUpdateTime 保证同一毫秒连续写入也改变校验值，避免旧表单覆盖刚保存的资料。
// NextDeviceUpdateTime 计算资料编辑后的新 updated_at。
// 乐观并发要求时间戳严格递增，因此同一毫秒内的连续编辑也要往后推一毫秒。
func NextDeviceUpdateTime(previous time.Time) time.Time {
	next := time.Now().UTC().Truncate(time.Millisecond)
	if !next.After(previous) {
		next = previous.Add(time.Millisecond)
	}
	return next
}

// updateDevice 只编辑运营资料，设备身份、归属、通信、计费和状态由各自接口负责。
