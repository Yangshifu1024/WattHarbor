package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 开通按 device_provision 首次请求原文与 device 已存归属分别核对幂等身份。
// 重放保留原设备、端口和归属，不覆盖首次登记内容。

// ErrProvisionConflict 表示设备已存在但参数不同，或厂商不可用。
var ErrProvisionConflict = errors.New("provision conflict")

// ErrVendorAdapterImmutable 表示厂商的适配器或协议不允许在有设备时更改。
var ErrVendorAdapterImmutable = errors.New("vendor adapter is immutable once devices exist")

// ProvisionDevice 是一台待开通设备的参数。
type ProvisionDevice struct {
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id"`
	// VendorID 原所属厂商身份。
	VendorID uint64 `json:"vendor_id"`
	// StationID 原所属站点身份，保留可空映射。
	StationID uint64 `json:"station_id"`
	// PortCount 原开通端口数量。
	PortCount uint8 `json:"port_count"`
	// Model 原可空设备型号。
	Model *string `json:"model"`
}

// lockVendorForProvision 接收所属 tx 与 vendorID，以 SHARE 当前读取得未删除厂商状态及适配器；返回两字段和缺失或 SQL 错误，锁由调用事务释放。
func lockVendorForProvision(tx *gorm.DB, vendorID uint64) (status, adapterClass string, err error) {
	var row struct {
		// Status 所属记录的原状态值。
		Status string
		// AdapterClass 原厂商设备适配器类型。
		AdapterClass string
	}
	err = tx.Table("vendor").Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id=? AND deleted_at IS NULL", vendorID).Take(&row).Error
	return row.Status, row.AdapterClass, err
}

// maxDC589Ports 是原 dc589 开通端口数上限。
const maxDC589Ports = 20

// lockProvisionIdentity 接收所属 tx 与 deviceID，以 UPDATE 锁读取首次开通请求原文；返回原文或缺失、SQL 错误，锁由调用事务释放。
func lockProvisionIdentity(tx *gorm.DB, deviceID string) (string, error) {
	var row struct {
		// RequestJSON 设备首次开通请求的原 JSON 文本。
		RequestJSON string
	}
	err := tx.Table("device_provision").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id=?", deviceID).Take(&row).Error
	return row.RequestJSON, err
}

// recordProvisionIdentity 接收所属 tx、deviceID 与 requestJSON，以 INSERT IGNORE 登记原身份，已有原文不覆盖；返回 SQL 错误，不自行提交。
func recordProvisionIdentity(tx *gorm.DB, deviceID, requestJSON string) error {
	return tx.Exec("INSERT IGNORE INTO device_provision(device_id,request_json) VALUES (?,?)", deviceID, requestJSON).Error
}

// provisionedDevice 是已开通设备的归属信息。
type provisionedDevice struct {
	// ID device 表的原自增主键。
	ID uint64
	// VendorID 原所属厂商身份。
	VendorID uint64
	// StationID 原所属站点身份，保留可空映射。
	StationID uint64
	// PortCount 原开通端口数量。
	PortCount uint8
	// Model 原可空设备型号。
	Model *string
}

// findProvisionedDevice 接收所属 tx 与 deviceID，读取已开通设备归属；返回记录或 gorm.ErrRecordNotFound、SQL 错误，无写入。
func findProvisionedDevice(tx *gorm.DB, deviceID string) (provisionedDevice, error) {
	var row provisionedDevice
	err := tx.Table("device").Where("device_id=?", deviceID).Take(&row).Error
	return row, err
}

// createProvisionedDevice 接收所属 tx 与 d，插入启用设备及原厂商、站点、端口数、可空型号；返回 SQL 错误，不自行提交。
func createProvisionedDevice(tx *gorm.DB, d ProvisionDevice) error {
	return tx.Table("device").Create(map[string]any{
		"device_id": d.DeviceID, "vendor_id": d.VendorID, "station_id": d.StationID,
		"port_count": d.PortCount, "model": d.Model, "status": "enabled",
	}).Error
}

// createDevicePorts 接收所属 tx、deviceID 与 portCount，按一到端口数插入原设备号-端口号编码的空闲端口；首次 SQL 错误返回，不自行回滚或提交。
func createDevicePorts(tx *gorm.DB, deviceID string, portCount uint8) error {
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

// sameProvisionIdentity 接收 registeredJSON 与 want，将首次请求解码后按原 JSON 编码结果比较；返回是否相同，原文无效返回 ErrProvisionConflict，无 IO。
func sameProvisionIdentity(registeredJSON string, want ProvisionDevice) (bool, error) {
	var existing ProvisionDevice
	if json.Unmarshal([]byte(registeredJSON), &existing) != nil {
		return false, ErrProvisionConflict
	}
	a, _ := json.Marshal(existing)
	b, _ := json.Marshal(want)
	return string(a) == string(b), nil
}

// CountVendorDevices 接收所属 tx 与 vendorID，统计厂商下设备记录（不追加软删过滤）；返回数量及 SQL 错误，无写入。
func CountVendorDevices(tx *gorm.DB, vendorID uint64) (int64, error) {
	var count int64
	err := tx.Table("device").Where("vendor_id = ?", vendorID).Count(&count).Error
	return count, err
}

// Vendor 是设备厂商的记录类型。
type Vendor struct {
	// ID vendor 表的原自增主键。
	ID uint64 `json:"id"`
	// VendorCode 原厂商业务编码。
	VendorCode string `json:"vendor_code"`
	// VendorName 原厂商名称，读取时保留可空映射。
	VendorName string `json:"vendor_name"`
	// AdapterClass 原厂商设备适配器类型。
	AdapterClass string `json:"adapter_class"`
	// Protocol 所属设备或连接的原协议名称。
	Protocol string `json:"protocol"`
	// Status 所属记录的原状态值。
	Status string `json:"status"`
	// EnabledAt 原厂商最近启用时刻，可空。
	EnabledAt *time.Time `json:"enabled_at"`
	// CreatedAt 原记录创建时刻。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 原记录更新时刻。
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 返回原固定表名 vendor，供 GORM 定位模型；无数据库 IO。
func (Vendor) TableName() string { return "vendor" }

// VendorColumns 是厂商的完整列清单，查询时显式列出避免 gorm 按字段名推导。
const VendorColumns = "id,vendor_code,vendor_name,adapter_class,protocol,status,enabled_at,created_at,updated_at"

// VendorQuery 是厂商列表的筛选条件。
type VendorQuery struct {
	Keyword string   // 编码或名称的模糊关键字
	Pattern string   // 转义并加好通配符的关键字，直接用于 LIKE
	Status  string   // 状态过滤，空表示不限
	IDs     []uint64 // 指定厂商范围，空表示不限
	// Page 原从一开始的页码。
	Page int
	// PageSize 原每页记录上限。
	PageSize int
}

// ListVendors 接收 ctx、db 与 q，按原范围、状态和已用 ! 转义的 Pattern 读取未删除厂商，倒序分页；返回记录、总数及 SQL 错误，无写入。
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

// LockVendor 接收所属 tx 与 id，以 UPDATE 锁读取未删除厂商完整公开列；返回记录或缺失、SQL 错误，不自行提交。
func LockVendor(tx *gorm.DB, id uint64) (Vendor, error) {
	var row Vendor
	err := tx.Select(VendorColumns).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// UpdateVendor 接收所属 tx、id、wasEnabled 与 values，按调用方列值更新未删除厂商并锁定回读；wasEnabled 保留原签名但不参与列构造，返回记录或 SQL 错误，不提交。
func UpdateVendor(tx *gorm.DB, id uint64, wasEnabled bool, values map[string]any) (Vendor, error) {
	if err := tx.Model(&Vendor{}).Where("id = ? AND deleted_at IS NULL", id).Updates(values).Error; err != nil {
		return Vendor{}, err
	}
	return LockVendor(tx, id)
}

// ProvisionBatch 在 ctx 下按 devices 输入顺序执行原整批开通事务，返回首个厂商、身份冲突或 SQL／取消错误。
// 任一设备失败会回滚整批首次身份、设备与端口写入；同身份重放沿用原归属，不发送设备命令或自行重试。
func ProvisionBatch(ctx context.Context, db *gorm.DB, devices []ProvisionDevice) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, d := range devices {
			if err := provisionOne(tx, d); err != nil {
				return err
			}
		}
		return nil
	})
}

// provisionOne 接收所属 tx 与 d，核对厂商及原开通身份后插入设备、端口；相同归属重放返回 nil，失配或不可用返回冲突，SQL 失败原样返回，不自行提交。
func provisionOne(tx *gorm.DB, d ProvisionDevice) error {
	status, adapterClass, err := lockVendorForProvision(tx, d.VendorID)
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
	if err := recordProvisionIdentity(tx, d.DeviceID, string(raw)); err != nil {
		return err
	}
	registered, err := lockProvisionIdentity(tx, d.DeviceID)
	if err != nil {
		return err
	}
	same, err := sameProvisionIdentity(registered, d)
	if err != nil {
		return err
	}
	if !same {
		return ErrProvisionConflict
	}
	existing, err := findProvisionedDevice(tx, d.DeviceID)
	if err == nil {
		if existing.VendorID != d.VendorID || existing.StationID != d.StationID || existing.PortCount != d.PortCount {
			return ErrProvisionConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := createProvisionedDevice(tx, d); err != nil {
		return err
	}
	return createDevicePorts(tx, d.DeviceID, d.PortCount)
}
