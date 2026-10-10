package device

import (
	"chargepilot/internal/persist"
	"context"
	"encoding/json"
	"time"

	"chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
)

// Device 是设备列表和详情资料，从 device_meta 读取并关联站点名；关联、返回和搜索都使用站点 ID。
type Device struct {
	ID               uint64                   `json:"id"`                         // 设备在 central_db 的内部主键，仅用于列表排序。
	DeviceID         string                   `json:"device_id"`                  // 设备业务编号，也是详情查询键；存储上限 64 字符，新导入限定 8–32 位。
	StationID        *uint64                  `json:"station_id"`                 // 所属站点 ID，可空表示尚未归属站点。
	StationName      *string                  `json:"station_name"`               // 所属站点名称，联表带出；站点被软删除时为空。
	VendorID         *uint64                  `json:"vendor_id"`                  // 设备厂商 ID，可空。
	VendorName       *string                  `json:"vendor_name" gorm:"-"`       // 网关补充的厂商名，未知或已删除为 null
	LastHeartbeatAt  *time.Time               `json:"last_heartbeat_at" gorm:"-"` // 网关最后心跳时刻，未知为 null
	SignalStrength   *uint8                   `json:"signal_strength" gorm:"-"`   // 最新设备信号；未上报时为空，0 仍是有效读数。
	SignalAt         *time.Time               `json:"signal_at" gorm:"-"`         // 信号采样时刻，未知为 null
	Ports            []DevicePortStatus       `json:"ports" gorm:"-"`             // 当前端口状态；未读取到端口时返回空数组。
	RuntimeAvailable bool                     `json:"runtime_available" gorm:"-"` // 是否读取到网关摘要；不表示设备在线或无故障
	Model            *string                  `json:"model"`                      // 设备型号，可空；关键词搜索会匹配它。
	SerialNo         *string                  `json:"serial_no"`                  // 设备出厂序列号，可空。
	Status           devices.DeviceMetaStatus `json:"status"`                     // 设备状态：enabled 启用、disabled 停用、retired 退役、fault 故障。
	WarrantyUntil    *time.Time               `json:"warranty_until"`             // 保修截止，UTC 毫秒时间，可空
	Tags             []string                 `json:"tags" gorm:"-"`              // 对外标签数组；数据库 NULL 统一读为空数组。
	TagsJSON         *string                  `json:"-" gorm:"column:tags_json"`  // 内部持久标签数组，响应隐藏
	UpdatedAt        time.Time                `json:"updated_at"`                 // 资料编辑的并发校验值，使用 UTC 毫秒精度。
	// 设备协议支持的计量功能和资料中保存的计费模式。
	ProtocolAdapter       string              `json:"protocol_adapter"`                 // 导入时按厂商资料确定的协议适配器
	ChargeMode            charging.ChargeMode `json:"charge_mode"`                      // device_meta.charge_mode，词表复用 charging.ChargeMode；当前生效的套餐和价格须另查 pricing_rule。
	ReportsEnergy         bool                `json:"reports_energy" gorm:"-"`          // 协议是否支持电量上报；按电量计费要求此能力。
	ReportsSegmentedPower bool                `json:"reports_segmented_power" gorm:"-"` // 协议是否支持分段功率上报；按功率计费要求此能力。
}

// DevicePortStatus 保留设备状态码和采样时间，未上报不能视为空闲。
type DevicePortStatus struct {
	PortNo     uint8      `json:"port_no"`     // 物理端口号
	StatusCode *uint8     `json:"status_code"` // 原始协议状态，null 未知、零有效
	StatusAt   *time.Time `json:"status_at"`   // 当前状态上报时刻，未知为 null
}

// DeviceQuery 构造未删除设备查询，并关联未删除站点，尚不执行 SQL。
func DeviceQuery(ctx context.Context, db *gorm.DB) *gorm.DB {
	return db.WithContext(ctx).Table("device_meta AS d").Joins("LEFT JOIN station AS s ON s.id=d.station_id AND s.deleted_at IS NULL").Where("d.deleted_at IS NULL")
}

// DeviceColumns 是设备列表和详情共用的查询列。
const DeviceColumns = "d.id,d.device_id,d.station_id,d.vendor_id,d.model,d.serial_no,d.status,d.warranty_until,d.tags_json,d.updated_at,d.charge_mode,d.protocol_adapter,s.name AS station_name"

// NormalizeMetadata 解析设备标签，统一空数组和 UTC 毫秒时间。
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

// ListDevices 按站点和厂商范围分页倒序读取设备，转义搜索关键词并补充协议能力。
func ListDevices(ctx context.Context, db *gorm.DB, q persist.PageQuery, scopes ...operations.DataScope) (persist.Page[Device], error) {
	out := persist.Page[Device]{Items: []Device{}, Page: q.Page, PageSize: q.PageSize}
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
