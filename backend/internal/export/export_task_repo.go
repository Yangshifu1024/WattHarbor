package export

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/device"
	"wattharbor/internal/domain/operations"
)

// ErrUnknownExportResource 表示资源未登记，必须拒绝导出，不能当作空结果。
var ErrUnknownExportResource = errors.New("未登记的导出资源")

// export_task 保存导出请求和结果，文件路径仅供服务端使用。
// 受理、锁读重放和审计共用调用方事务，普通连接只供无锁查询；连接池由进程管理。

// ExportStatus 是 export_task.status 列的导出任务状态，用于列表筛选、生成完成与到期清理。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
type ExportStatus string

const (
	// ExportStatusPending 表示已受理待生成。
	ExportStatusPending ExportStatus = "pending"
	// ExportStatusRunning 表示正在同步生成文件。
	ExportStatusRunning ExportStatus = "running"
	// ExportStatusCompleted 表示文件已发布，可下载。
	ExportStatusCompleted ExportStatus = "completed"
	// ExportStatusFailed 表示生成失败，已记录失败因。
	ExportStatusFailed ExportStatus = "failed"
	// ExportStatusExpired 表示超过下载截止，已不允许下载。
	ExportStatusExpired ExportStatus = "expired"
)

// Known 判断是否属于该列的数据库合法集合（五值）。
func (s ExportStatus) Known() bool {
	switch s {
	case ExportStatusPending, ExportStatusRunning, ExportStatusCompleted, ExportStatusFailed, ExportStatusExpired:
		return true
	default:
		return false
	}
}

// AllExportStatuses 是 ExportStatus 的全量取值串，供列表筛选白名单直接引用。
const AllExportStatuses = "pending running completed failed expired"

// SettableExportStatuses 是生成流程可写入的状态集合：pending 是受理默认值，
// running、completed 与 failed 由执行路径写入；expired 只由到期清理写入，不在此集合内。
const SettableExportStatuses = "pending running completed failed"

// TerminalExportStatuses 是已结束、可进入到期清理的状态集合（completed 与 failed）。
const TerminalExportStatuses = "completed failed"

// terminalExportStatuses 是 TerminalExportStatuses 对应的取值切片，供 SQL 绑定使用。
// 白名单串是空格分隔的展示口径，不能直接拼进 IN 括号——那会得到单元素 IN ('completed failed') 而匹配不到任何行。
var terminalExportStatuses = []ExportStatus{ExportStatusCompleted, ExportStatusFailed}

// ActiveExportStatuses 是生成流程的允许起始状态集合（pending 与 running）。
const ActiveExportStatuses = "pending running"

// activeExportStatuses 是 ActiveExportStatuses 对应的取值切片，供 SQL 绑定使用。
var activeExportStatuses = []ExportStatus{ExportStatusPending, ExportStatusRunning}

// ExportFileFormat 是 export_task.file_format 列的导出文件格式。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（csv／xlsx）。
type ExportFileFormat string

const (
	// ExportFormatCSV 表示逗号分隔文本格式，也是列的默认值。
	ExportFormatCSV ExportFileFormat = "csv"
	// ExportFormatXLSX 表示 Excel 工作簿格式。
	ExportFormatXLSX ExportFileFormat = "xlsx"
	// LegacyExportFormatPDF 是历史遗留的 PDF 取值。
	// 它不在 export_task.file_format 的 ENUM 约束内，只为兼容早期已写入的行与旧文件：
	// 当前受理接口不再接受该格式，下载路径也一律拒绝，只有到期清理需要识别并删除这类文件。
	// 因此单独定义，且不计入 Known() 的合法集合，避免被误当作可写入取值。
	LegacyExportFormatPDF ExportFileFormat = "pdf"
)

// Known 判断是否属于该列的数据库合法集合（两值），不包含历史 PDF。
func (f ExportFileFormat) Known() bool {
	switch f {
	case ExportFormatCSV, ExportFormatXLSX:
		return true
	default:
		return false
	}
}

// ExportTaskRow 对应 central_db.export_task，保存导出申请和有效期。
type ExportTaskRow struct {
	ID          uint64           `json:"id"`                                      // 导出任务主键 ID
	TaskNo      string           `json:"task_no" gorm:"column:task_no"`           // 任务号,形如 EXP + 32 位 UUID(大写无横线),同时用作文件名
	Resource    string           `json:"resource" gorm:"column:resource"`         // 导出资源名,取自 exportRegistry：orders / stations / devices / bills
	FileFormat  ExportFileFormat `json:"file_format" gorm:"column:file_format"`   // 文件格式：csv / xlsx
	Status      ExportStatus     `json:"status" gorm:"column:status"`             // 任务状态:pending 待生成 / running 生成中 / completed 已完成 / failed 失败 / expired 已过期
	RequestedBy uint64           `json:"requested_by" gorm:"column:requested_by"` // 发起导出的管理员 ID,下载时用于校验归属
	FilterJSON  *string          `json:"filter_json" gorm:"column:filter_json"`   // 筛选条件 JSON 原文(可空),如 from/to 日期区间
	RowCount    uint32           `json:"row_count" gorm:"column:row_count"`       // 实际导出行数,生成完成时写入
	FilePath    *string          `json:"-" gorm:"column:file_path"`               // 服务器上的文件路径,不返回给前端(json:"-"),只在下载与清理时内部使用
	ErrorMsg    *string          `json:"error_msg" gorm:"column:error_msg"`       // 失败因(可空),最长 255 字符
	ExpiresAt   *string          `json:"expires_at" gorm:"column:expires_at"`     // 下载截止时间,创建时为 24 小时后
	CreatedAt   string           `json:"created_at" gorm:"column:created_at"`     // 申请时间,UTC
	CompletedAt *string          `json:"completed_at" gorm:"column:completed_at"` // 生成完成时间(可空)
}

// TableName 指定 export_task 表映射。
func (ExportTaskRow) TableName() string { return "export_task" }

// ExportColumn 分别指定中文表头和数据库列名。
type ExportColumn struct {
	Header string // 文件里的中文表头
	Column string // 数据库列名,也是字段脱敏规则匹配用的字段名
}

// ExportSpec 指定资源权限、来源表、列和范围过滤，申请及下载均检查权限。
type ExportSpec struct {
	Permission string                                                // 导出该资源所需的读权限
	Table      string                                                // 数据来源表名
	Columns    []ExportColumn                                        // 导出列清单,顺序即文件列顺序
	builder    func(scope operations.DataScope, q *gorm.DB) *gorm.DB // 追加数据范围等条件的钩子,为空表示不额外过滤
}

// 只有登记过的资源允许导出；账单包含全局财务数据，只允许全局范围。
var exportRegistry = map[string]ExportSpec{
	"orders": {
		Permission: "order.read",
		Table:      "charge_order",
		Columns: []ExportColumn{
			{"订单号", "order_no"}, {"用户", "user_id"}, {"设备", "device_id"}, {"端口", "port_no"},
			{"状态", "status"}, {"开始时间", "started_at"}, {"结束时间", "ended_at"},
			{"电费(分)", "electric_cents"}, {"服务费(分)", "service_cents"}, {"合计(分)", "total_cents"},
		},
		builder: func(scope operations.DataScope, q *gorm.DB) *gorm.DB {
			return device.ScopeStations(scope, q, "station_id")
		},
	},
	"stations": {
		Permission: "station.read",
		Table:      "station",
		Columns: []ExportColumn{
			// 站点 ID 仅用于范围筛选，文件只包含名称、地址和状态。
			{"名称", "name"}, {"地址", "address"}, {"状态", "status"},
		},
		builder: func(scope operations.DataScope, q *gorm.DB) *gorm.DB {
			return device.ScopeStations(scope, q, "id")
		},
	},
	"devices": {
		Permission: "device.read",
		Table:      "device_meta",
		Columns: []ExportColumn{
			{"设备号", "device_id"}, {"型号", "model"}, {"站点", "station_id"}, {"厂商", "vendor_id"}, {"状态", "status"},
		},
		builder: func(scope operations.DataScope, q *gorm.DB) *gorm.DB {
			// 设备导出与设备列表采用站点、厂商两维交集；仅厂商范围不得退化为全库。
			return device.ScopeVendors(scope, device.ScopeStations(scope, q, "station_id"), "vendor_id")
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

// ExportRepo 保存导出任务并读取登记过的业务字段，不写来源表。
type ExportRepo struct {
	DB *gorm.DB // 中央导出数据库连接，由 cmd 管理
}

// ListExportTasks 按状态分页倒序读取完整任务和总数，路径字段不参与 JSON 输出。
// 计数用独立查询会话，分页和状态由服务校验，连接须带请求 context。
// status 是共用筛选项的入口，保持 string，在仓储内转为 ExportStatus 作为查询参数。
func (s ExportRepo) ListExportTasks(db *gorm.DB, status string, page, pageSize int) ([]ExportTaskRow, int64, error) {
	query := db.Table("export_task")
	if status != "" {
		query = query.Where("status = ?", ExportStatus(status))
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

// FindExportTaskByID 读取服务端完整任务，不加锁。
func (s ExportRepo) FindExportTaskByID(db *gorm.DB, id uint64) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Where("id = ?", id).Take(&row).Error
	return row, err
}

// LockExportTaskByID 在调用方事务中锁读任务，锁保持到提交。
// 普通连接的单语句锁不能保护后续文件操作。
func (s ExportRepo) LockExportTaskByID(db *gorm.DB, id uint64) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error
	return row, err
}

// FindExportTaskByNo 按任务号读取任务，缺失返回 gorm.ErrRecordNotFound。
func (s ExportRepo) FindExportTaskByNo(db *gorm.DB, taskNo string) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Where("task_no = ?", taskNo).Take(&row).Error
	return row, err
}

// ListExpirableExportTasks 读取到期完成或失败任务，每批最多 limit 条。
func (s ExportRepo) ListExpirableExportTasks(db *gorm.DB, now time.Time, limit int) ([]ExportTaskRow, error) {
	rows := []ExportTaskRow{}
	err := db.Table("export_task").
		Where("status IN ? AND expires_at <= ?", terminalExportStatuses, now).
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// ExportTaskInsert 是受理导出申请时的写入参数，调用方负责幂等比对。
type ExportTaskInsert struct {
	TaskNo      string           // 任务号,由请求号推导,同时用作文件名
	Resource    string           // 已登记的资源名
	FileFormat  ExportFileFormat // csv / xlsx
	RequestedBy uint64           // 发起导出的管理员 ID
	FilterJSON  string           // 筛选条件 JSON 原文
	ExpiresAt   time.Time        // 24 小时 UTC 下载截止
}

// ExpireExportTask 按任务号标为 expired，不删除文件。
// SQL 只匹配任务号，调用方须在任务锁内核对状态，不能把它当作比较并交换更新。
func (s ExportRepo) ExpireExportTask(db *gorm.DB, taskNo string) error {
	return db.Model(&ExportTaskRow{}).Where("task_no = ?", taskNo).Update("status", ExportStatusExpired).Error
}

// QueryExportSource 按登记资源读取业务数据，依次过滤软删除、日期和数据范围。
// 查询 limit 为上限加一，多出的一行用于判断是否超限。
func (s ExportRepo) QueryExportSource(ctx context.Context, resource string, scope operations.DataScope, filters map[string]any, limit int) ([]map[string]any, error) {
	spec, known := exportRegistry[resource]
	if !known {
		return nil, ErrUnknownExportResource
	}
	columns := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		columns = append(columns, column.Column)
	}
	query := s.sourceQuery(ctx, spec.Table).Select(columns)
	// 账单是不可软删的流水表，其余资源统一排除已删除行。
	if spec.Table != "charge_bill" {
		query = query.Where(map[string]any{"deleted_at": nil})
	}
	switch spec.Table {
	case "charge_bill":
		// 截止日期以次日零点为不含上界，包含指定当天。
		query = query.Where("issued_at >= ? AND issued_at < ?", filters["from"], operations.NextExportDate(filters["to"]))
	}
	if spec.builder != nil {
		query = spec.builder(scope, query)
	}
	records := []map[string]any{}
	if err := query.Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// sourceQuery 只为代码登记的表构造只读查询，不接受 HTTP 表名。
func (s ExportRepo) sourceQuery(ctx context.Context, table string) *gorm.DB {
	return s.DB.WithContext(ctx).Table(table)
}

// ClaimExportTask 按任务号唯一键登记首次请求，不覆盖已有任务。
// 不要先锁缺失行；重复插入等待提交后，由 LockExportTaskByNo 当前读核对。
// SQL 内的 'pending' 对应 export_task.status 词表中的受理默认值，语句不因类型化拆分。
func (s ExportRepo) ClaimExportTask(db *gorm.DB, in ExportTaskInsert) (bool, error) {
	res := db.Exec("INSERT INTO export_task(task_no,resource,file_format,status,requested_by,filter_json,expires_at) VALUES (?,?,?,'pending',?,?,?) ON DUPLICATE KEY UPDATE task_no=task_no", in.TaskNo, in.Resource, in.FileFormat, in.RequestedBy, in.FilterJSON, in.ExpiresAt)
	return res.RowsAffected == 1, res.Error
}

// LockExportTaskByNo 在调用方事务中按任务号锁读记录，不改变状态。
func (s ExportRepo) LockExportTaskByNo(db *gorm.DB, taskNo string) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_no=?", taskNo).Take(&row).Error
	return row, err
}

// Expiration 在任务锁内共享锁读 UTC 到期时间，避免旧 RR 快照。
// 内部使用 time.Time，公开字符串格式保持不变。
func (s ExportRepo) Expiration(db *gorm.DB, id uint64) (*time.Time, error) {
	var row struct {
		ExpiresAt *time.Time // 可空下载截止，驱动按 UTC 解析
	}
	err := db.Table("export_task").Clauses(clause.Locking{Strength: "SHARE"}).Select("expires_at").Where("id=?", id).Take(&row).Error
	return row.ExpiresAt, err
}

// CompleteInTx 在任务锁内将 pending／running 改为 completed，并保存行数、路径和完成时间。
// 仅在内容文件及范围、脱敏和摘要校验文件已发布后调用；事务提交后才能下载。
// WHERE 内的状态取值对应 ActiveExportStatuses，语句不因类型化拆分。
func (s ExportRepo) CompleteInTx(db *gorm.DB, taskNo string, rowCount int, path string, now time.Time) error {
	res := db.Model(&ExportTaskRow{}).Where("task_no=? AND status IN ?", taskNo, activeExportStatuses).Updates(map[string]any{"status": ExportStatusCompleted, "row_count": rowCount, "file_path": path, "completed_at": now, "error_msg": nil})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return operations.ErrConflict
	}
	return nil
}

// FailInTx 在任务锁内将 pending／running 改为 failed 并记录原因，不覆盖 completed 或 expired。
func (s ExportRepo) FailInTx(db *gorm.DB, taskNo, reason string) error {
	res := db.Model(&ExportTaskRow{}).Where("task_no=? AND status IN ?", taskNo, activeExportStatuses).Updates(map[string]any{"status": ExportStatusFailed, "error_msg": reason})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return operations.ErrConflict
	}
	return nil
}
