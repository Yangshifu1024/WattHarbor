package export

import (
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrUnknownExportResource 表示资源名未在登记表内。处理器应先校验资源再取数，
// 取数路径上再兜一层，避免漏登记的资源被当成空结果静默导出。
var ErrUnknownExportResource = errors.New("未登记的导出资源")

// 导出任务的记录类型与读写。导出申请与生成结果都落在 export_task，
// 落地文件路径只留在服务端，不随任何响应返回给前端。
//
// 本文件所有方法都接收调用方自备的 *gorm.DB，而不是在方法内部取连接：
// 受理申请时查找重放与插入必须与审计写在同一事务里，由处理器决定传入 tx 还是
// 普通连接；ExportStore 持有 central_db 的读连接。

// ExportTaskRow 对应 central_db.export_task，保存导出申请和有效期。
type ExportTaskRow struct {
	ID          uint64  `json:"id"`                                      // 导出任务主键 ID
	TaskNo      string  `json:"task_no" gorm:"column:task_no"`           // 任务号,形如 EXP + 32 位 UUID(大写无横线),同时用作文件名
	Resource    string  `json:"resource" gorm:"column:resource"`         // 导出资源名,取自 exportRegistry：orders / stations / devices / bills
	FileFormat  string  `json:"file_format" gorm:"column:file_format"`   // 文件格式：csv / xlsx / pdf；PDF 只支持 bills
	Status      string  `json:"status" gorm:"column:status"`             // 任务状态:pending 待生成 / running 生成中 / completed 已完成 / failed 失败 / expired 已过期
	RequestedBy uint64  `json:"requested_by" gorm:"column:requested_by"` // 发起导出的管理员 ID,下载时用于校验归属
	FilterJSON  *string `json:"filter_json" gorm:"column:filter_json"`   // 筛选条件 JSON 原文(可空),如 from/to 日期区间
	RowCount    uint32  `json:"row_count" gorm:"column:row_count"`       // 实际导出行数,生成完成时写入
	FilePath    *string `json:"-" gorm:"column:file_path"`               // 服务器上的文件路径,不返回给前端(json:"-"),只在下载与清理时内部使用
	ErrorMsg    *string `json:"error_msg" gorm:"column:error_msg"`       // 失败原因(可空),最长 255 字符
	ExpiresAt   *string `json:"expires_at" gorm:"column:expires_at"`     // 下载截止时间,创建时为 24 小时后
	CreatedAt   string  `json:"created_at" gorm:"column:created_at"`     // 申请时间,UTC
	CompletedAt *string `json:"completed_at" gorm:"column:completed_at"` // 生成完成时间(可空)
}

// TableName 指定 export_task 表映射。
func (ExportTaskRow) TableName() string { return "export_task" }

// ExportColumn 是一列的表头与数据库列名映射，两者分开，表头才能用中文。
type ExportColumn struct {
	Header string // 文件里的中文表头
	Column string // 数据库列名,也是字段脱敏规则匹配用的字段名
}

// ExportSpec 声明一个可导出资源的权限、来源表、列与数据范围过滤。
// Permission 是读该资源所需的权限，导出申请和下载时都要校验。
type ExportSpec struct {
	Permission string                                                // 导出该资源所需的读权限
	Table      string                                                // 数据来源表名
	Columns    []ExportColumn                                        // 导出列清单,顺序即文件列顺序
	Builder    func(scope operations.DataScope, q *gorm.DB) *gorm.DB // 追加数据范围等条件的钩子,为空表示不额外过滤
}

// 允许导出的资源登记表。未登记的资源一律拒绝导出，避免新增表时漏配列或权限。
// bills 涉及全量财务数据，由处理方另行要求全局数据范围。
var exportRegistry = map[string]ExportSpec{
	"orders": {
		Permission: "order.read",
		Table:      "charge_order",
		Columns: []ExportColumn{
			{"订单号", "order_no"}, {"用户", "user_id"}, {"设备", "device_id"}, {"端口", "port_no"},
			{"状态", "status"}, {"开始时间", "started_at"}, {"结束时间", "ended_at"},
			{"电费(分)", "electric_cents"}, {"服务费(分)", "service_cents"}, {"合计(分)", "total_cents"},
		},
		Builder: func(scope operations.DataScope, q *gorm.DB) *gorm.DB {
			return device.ScopeStations(scope, q, "station_id")
		},
	},
	"stations": {
		Permission: "station.read",
		Table:      "station",
		Columns: []ExportColumn{
			// 站点已统一用主键 ID，不再有 code 列；早先留着它会让导出恒定多出一列空值。
			{"名称", "name"}, {"地址", "address"}, {"状态", "status"},
		},
		Builder: func(scope operations.DataScope, q *gorm.DB) *gorm.DB {
			return device.ScopeStations(scope, q, "id")
		},
	},
	"devices": {
		Permission: "device.read",
		Table:      "device_meta",
		Columns: []ExportColumn{
			{"设备号", "device_id"}, {"型号", "model"}, {"站点", "station_id"}, {"厂商", "vendor_id"}, {"状态", "status"},
		},
		Builder: func(scope operations.DataScope, q *gorm.DB) *gorm.DB {
			return device.ScopeStations(scope, q, "station_id")
		},
	},
	"bills": {
		Permission: "finance.read",
		Table:      "charge_bill",
		Columns: []ExportColumn{
			{"账单号", "bill_no"}, {"订单ID", "charge_order_id"}, {"用户ID", "user_id"},
			{"设备号", "device_id"}, {"电费(分)", "electric_cents"}, {"服务费(分)", "service_cents"},
			{"合计(分)", "total_cents"}, {"退款(分)", "refund_cents"}, {"状态", "status"}, {"开具时间", "issued_at"},
		},
	},
}

// ExportResources 返回全部已登记的可导出资源。调用方只读，不得修改。
func ExportResources() map[string]ExportSpec { return exportRegistry }

// ExportResource 按资源名取登记信息，第二个返回值为 false 表示该资源不可导出。
func ExportResource(name string) (ExportSpec, bool) {
	spec, ok := exportRegistry[name]
	return spec, ok
}

// ExportStore 持有 central_db 的读连接。
type ExportStore struct {
	DB *gorm.DB
}

// ListExportTasks 分页返回导出任务，可按状态过滤，按 ID 倒序。
func (s ExportStore) ListExportTasks(db *gorm.DB, status string, page, pageSize int) ([]ExportTaskRow, int64, error) {
	query := db.Table("export_task")
	if status != "" {
		query = query.Where("status = ?", status)
	}
	total := int64(0)
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []ExportTaskRow{}
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindExportTaskByID 按主键取一条导出任务。
func (s ExportStore) FindExportTaskByID(db *gorm.DB, id uint64) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Where("id = ?", id).Take(&row).Error
	return row, err
}

// LockExportTaskByID 在行锁下取导出任务，下载时用来避免并发重复删除同一文件。
func (s ExportStore) LockExportTaskByID(db *gorm.DB, id uint64) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error
	return row, err
}

// FindExportTaskByNo 按任务号取一条导出任务，不存在时返回 gorm.ErrRecordNotFound。
func (s ExportStore) FindExportTaskByNo(db *gorm.DB, taskNo string) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Where("task_no = ?", taskNo).Take(&row).Error
	return row, err
}

// ListExpirableExportTasks 返回下载窗口已过的任务，调用方删文件后逐条置为 expired。
// 单批上限 limit，避免一次锁定过多行。
func (s ExportStore) ListExpirableExportTasks(db *gorm.DB, now time.Time, limit int) ([]ExportTaskRow, error) {
	rows := []ExportTaskRow{}
	err := db.Table("export_task").
		Where("status IN ? AND expires_at <= ?", []string{"completed", "failed"}, now).
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// ExportTaskInsert 是受理导出申请时的写入参数，调用方负责幂等比对。
type ExportTaskInsert struct {
	TaskNo      string // 任务号,由请求号推导,同时用作文件名
	Resource    string // 已登记的资源名
	FileFormat  string // csv / xlsx / pdf
	RequestedBy uint64 // 发起导出的管理员 ID
	FilterJSON  string // 筛选条件 JSON 原文
	ExpiresAt   time.Time
}

// InsertExportTask 落一条 pending 任务。必须与调用方的事务同库，
// 让申请记录与审计写入一起提交。
func (s ExportStore) InsertExportTask(db *gorm.DB, in ExportTaskInsert) error {
	return db.Table("export_task").Create(map[string]any{
		"task_no": in.TaskNo, "resource": in.Resource, "file_format": in.FileFormat,
		"status": "pending", "requested_by": in.RequestedBy,
		"filter_json": in.FilterJSON, "expires_at": in.ExpiresAt,
	}).Error
}

// MarkExportCompleted 记录生成成功：实际行数、落地路径与完成时间。
func (s ExportStore) MarkExportCompleted(db *gorm.DB, taskNo string, rowCount int, path string, completedAt time.Time) error {
	return db.Model(&ExportTaskRow{}).Where("task_no = ?", taskNo).
		Updates(map[string]any{
			"status": "completed", "row_count": rowCount, "file_path": path,
			"completed_at": completedAt,
		}).Error
}

// MarkExportFailed 记录生成失败及截断后的原因。
func (s ExportStore) MarkExportFailed(db *gorm.DB, taskNo, reason string) error {
	return db.Model(&ExportTaskRow{}).Where("task_no = ?", taskNo).
		Updates(map[string]any{"status": "failed", "error_msg": reason}).Error
}

// ExpireExportTask 把任务置为过期，条件带上原状态，避免覆盖并发生成中的任务。
func (s ExportStore) ExpireExportTask(db *gorm.DB, taskNo string) error {
	return db.Model(&ExportTaskRow{}).Where("task_no = ?", taskNo).Update("status", "expired").Error
}

// QueryExportSource 从 central_db 按资源取数到通用行集合：
// 再依次应用软删除、日期区间与数据范围，最后按 limit 截断。
// limit 由调用方给成「上限 + 1」，多出的那一行用来判断是否超限。
func (s ExportStore) QueryExportSource(resource string, scope operations.DataScope, filters map[string]any, limit int) ([]map[string]any, error) {
	spec, known := exportRegistry[resource]
	if !known {
		return nil, ErrUnknownExportResource
	}
	query := s.sourceQuery(spec.Table)
	// 账单是不可软删的流水表，其余资源统一排除已删除行。
	if spec.Table != "charge_bill" {
		query = query.Where(map[string]any{"deleted_at": nil})
	}
	switch spec.Table {
	case "charge_bill":
		// to 是开票日，取次日零点作为开区间上界，含当天。
		query = query.Where("issued_at >= ? AND issued_at < ?", filters["from"], operations.NextExportDate(filters["to"]))
	}
	if spec.Builder != nil {
		query = spec.Builder(scope, query)
	}
	records := []map[string]any{}
	if err := query.Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// sourceQuery 为已登记资源建立 central_db 查询。
func (s ExportStore) sourceQuery(table string) *gorm.DB {
	return s.DB.Table(table)
}
