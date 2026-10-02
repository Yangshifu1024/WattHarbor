package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 设备开通。开通是不可逆的：设备一旦建出来，它的端口与身份就固定了，
// 后续改厂商或站点会让历史报文对不上。所以这里的每一步都先判冲突再写。
//
// 两条幂等凭据：device_provision 存首次开通的请求原文，同一设备号再次提交
// 必须内容一致；device 表本身存已开通的归属，与请求不符同样判冲突。

// ErrProvisionConflict 表示设备已存在但参数不同，或厂商不可用。
var ErrProvisionConflict = errors.New("provision conflict")

// ErrVendorAdapterImmutable 表示厂商的适配器或协议不允许在有设备时更改。
var ErrVendorAdapterImmutable = errors.New("vendor adapter is immutable once devices exist")

// ProvisionDevice 是一台待开通设备的参数。
type ProvisionDevice struct {
	DeviceID  string  `json:"device_id"`
	VendorID  uint64  `json:"vendor_id"`
	StationID uint64  `json:"station_id"`
	PortCount uint8   `json:"port_count"`
	Model     *string `json:"model"`
}

// LockVendorForProvision 以共享锁取厂商。它与厂商编辑的排他锁互斥：
// 厂商编辑改协议的同时这里正在建设备，两者会各自看到对方的前一个状态。
func LockVendorForProvision(tx *gorm.DB, vendorID uint64) (status, adapterClass string, err error) {
	var row struct {
		Status       string
		AdapterClass string
	}
	err = tx.Table("vendor").Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id=? AND deleted_at IS NULL", vendorID).Take(&row).Error
	return row.Status, row.AdapterClass, err
}

// maxDC589Ports 是 dc589 协议允许的端口数上限。超出时设备接不上去，
// 与其建成一个永远不在线的设备，不如在开通时拒��。
const maxDC589Ports = 20

// LockProvisionIdentity 在行锁下取设备首次开通时登记的请求原文。
func LockProvisionIdentity(tx *gorm.DB, deviceID string) (string, error) {
	var row struct {
		RequestJSON string
	}
	err := tx.Table("device_provision").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id=?", deviceID).Take(&row).Error
	return row.RequestJSON, err
}

// RecordProvisionIdentity 以 INSERT IGNORE 登记设备编号与请求原文。
// 已有登记的行不会被覆盖——内容比对由调用方做，覆盖了就无从判断改没改。
func RecordProvisionIdentity(tx *gorm.DB, deviceID, requestJSON string) error {
	return tx.Exec("INSERT IGNORE INTO device_provision(device_id,request_json) VALUES (?,?)", deviceID, requestJSON).Error
}

// ProvisionedDevice 是已开通设备的归属信息。
type ProvisionedDevice struct {
	ID        uint64
	VendorID  uint64
	StationID uint64
	PortCount uint8
	Model     *string
}

// FindProvisionedDevice 取已开通的设备。取不到返回 gorm.ErrRecordNotFound。
func FindProvisionedDevice(tx *gorm.DB, deviceID string) (ProvisionedDevice, error) {
	var row ProvisionedDevice
	err := tx.Table("device").Where("device_id=?", deviceID).Take(&row).Error
	return row, err
}

// CreateProvisionedDevice 建设备主记录。
func CreateProvisionedDevice(tx *gorm.DB, d ProvisionDevice) error {
	return tx.Table("device").Create(map[string]any{
		"device_id": d.DeviceID, "vendor_id": d.VendorID, "station_id": d.StationID,
		"port_count": d.PortCount, "model": d.Model, "status": "enabled",
	}).Error
}

// CreateDevicePorts 按端口数建端口。端口编码是"设备号-端口号"，
// 心跳与历史数据都按它寻址，建错之后没法补救。
func CreateDevicePorts(tx *gorm.DB, deviceID string, portCount uint8) error {
	for port := 1; port <= int(portCount); port++ {
		if err := tx.Table("device_port").Create(map[string]any{
			"device_id": deviceID, "port_no": port,
			"port_code": fmt.Sprintf("%s-%d", deviceID, port), "status": "idle",
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// SameProvisionRequest 比对首次登记的请求原文与当前请求。
// 任一侧解析失败都判冲突：宁可让运营重来一次，也不要放行一台参数不明的设备。
func SameProvisionIdentity(registeredJSON string, want ProvisionDevice) (bool, error) {
	var existing ProvisionDevice
	if json.Unmarshal([]byte(registeredJSON), &existing) != nil {
		return false, ErrProvisionConflict
	}
	a, _ := json.Marshal(existing)
	b, _ := json.Marshal(want)
	return string(a) == string(b), nil
}

// CountVendorDevices 统计厂商名下的设备数。
func CountVendorDevices(tx *gorm.DB, vendorID uint64) (int64, error) {
	var count int64
	err := tx.Table("device").Where("vendor_id = ?", vendorID).Count(&count).Error
	return count, err
}

// Vendor 是设备厂商的记录类型。
type Vendor struct {
	ID           uint64     `json:"id"`
	VendorCode   string     `json:"vendor_code"`
	VendorName   string     `json:"vendor_name"`
	AdapterClass string     `json:"adapter_class"`
	Protocol     string     `json:"protocol"`
	Status       string     `json:"status"`
	EnabledAt    *time.Time `json:"enabled_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (Vendor) TableName() string { return "vendor" }

// vendorColumns 是厂商的完整列清单，查询时显式列出避免 gorm 按字段名推导。
// VendorColumns 是厂商的完整列清单，查询时显式列出避免 gorm 按字段名推导。
const VendorColumns = "id,vendor_code,vendor_name,adapter_class,protocol,status,enabled_at,created_at,updated_at"

// VendorQuery 是厂商列表的筛选条件。
type VendorQuery struct {
	Keyword  string   // 编码或名称的模糊关键字
	Pattern  string   // 转义并加好通配符的关键字，直接用于 LIKE
	Status   string   // 状态过滤，空表示不限
	IDs      []uint64 // 指定厂商范围，空表示不限
	Page     int
	PageSize int
}

// ListVendors 分页返回未删除的厂商，按 ID 倒序。
// pattern 必须已转义：用户输入里的 % 与 _ 不该被当成通配符，
// 否则搜 "a%" 会列出全部厂商。
func ListVendors(ctx context.Context, db *gorm.DB, q VendorQuery) ([]Vendor, int64, error) {
	query := db.WithContext(ctx).Table("vendor").Where("deleted_at IS NULL")
	if q.Keyword != "" {
		query = query.Where("(vendor_code LIKE ? ESCAPE '!' OR vendor_name LIKE ? ESCAPE '!')", q.Pattern, q.Pattern)
	}
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if len(q.IDs) > 0 {
		query = query.Where("id IN ?", q.IDs)
	}
	total := int64(0)
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := []Vendor{}
	err := query.Select(VendorColumns).Order("id DESC").
		Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&items).Error
	return items, total, err
}

// LockVendor 在行锁下取一个未删除的厂商。它与设备开通时的共享锁互斥，
// 保证"厂商改协议"与"按协议建设备"不会交错。
func LockVendor(tx *gorm.DB, id uint64) (Vendor, error) {
	var row Vendor
	err := tx.Select(VendorColumns).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// UpdateVendor 写入厂商字段并回读最新行。enabled_at 只在从非启用变为启用时更新。
func UpdateVendor(tx *gorm.DB, id uint64, wasEnabled bool, values map[string]any) (Vendor, error) {
	if err := tx.Model(&Vendor{}).Where("id = ? AND deleted_at IS NULL", id).Updates(values).Error; err != nil {
		return Vendor{}, err
	}
	return LockVendor(tx, id)
}

// ProvisionOne 开通一台设备：校验厂商、比对首次登记的内容、创建设备与端口。
// 已是同一台且参数一致的设备按幂等处理直接返回。
func ProvisionOne(tx *gorm.DB, d ProvisionDevice) error {
	status, adapterClass, err := LockVendorForProvision(tx, d.VendorID)
	if err != nil {
		return err
	}
	if status != "enabled" {
		return ErrProvisionConflict
	}
	if adapterClass == "dc589" && d.PortCount > maxDC589Ports {
		return ErrProvisionConflict
	}
	raw, _ := json.Marshal(d)
	if err := RecordProvisionIdentity(tx, d.DeviceID, string(raw)); err != nil {
		return err
	}
	registered, err := LockProvisionIdentity(tx, d.DeviceID)
	if err != nil {
		return err
	}
	same, err := SameProvisionIdentity(registered, d)
	if err != nil {
		return err
	}
	if !same {
		return ErrProvisionConflict
	}
	existing, err := FindProvisionedDevice(tx, d.DeviceID)
	if err == nil {
		if existing.VendorID != d.VendorID || existing.StationID != d.StationID || existing.PortCount != d.PortCount {
			return ErrProvisionConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := CreateProvisionedDevice(tx, d); err != nil {
		return err
	}
	return CreateDevicePorts(tx, d.DeviceID, d.PortCount)
}
