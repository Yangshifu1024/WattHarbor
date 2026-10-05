package export

import (
	"context"
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
// 受理申请的唯一键登记、当前重放读取和审计写在同一事务内；普通连接仅供无锁读取。
// ExportRepo 持有 central_db 的连接，不拥有连接池生命周期。

// ExportTaskRow 对应 central_db.export_task，保存导出申请和有效期。
type ExportTaskRow struct {
	ID          uint64  `json:"id"`                                      // 导出任务主键 ID
	TaskNo      string  `json:"task_no" gorm:"column:task_no"`           // 任务号,形如 EXP + 32 位 UUID(大写无横线),同时用作文件名
	Resource    string  `json:"resource" gorm:"column:resource"`         // 导出资源名,取自 exportRegistry：orders / stations / devices / bills
	FileFormat  string  `json:"file_format" gorm:"column:file_format"`   // 文件格式：csv / xlsx
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
	builder    func(scope operations.DataScope, q *gorm.DB) *gorm.DB // 追加数据范围等条件的钩子,为空表示不额外过滤
}

// 允许导出的资源登记表。未登记的资源一律拒绝导出，避免新增表时漏配列或权限。
// bills涉及原全局财务数据，由所属服务要求全局数据范围。
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
			// 站点已统一用主键 ID，不再有 code 列；早先留着它会让导出恒定多出一列空值。
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

// ExportRepo 访问central_db自有导出任务及已登记跨模块只读投影，不写业务源表。
type ExportRepo struct {
	DB *gorm.DB // central任务及只读投影连接，生命周期由cmd管理
}

// ListExportTasks 在调用方db下按status、page与pageSize读取原ID倒序分页，返回清单、总数或存储错误。
// db须携请求context；计数独立session不污染列表，页码及状态由所属服务核对，不暴露文件路径。
func (s ExportRepo) ListExportTasks(db *gorm.DB, status string, page, pageSize int) ([]ExportTaskRow, int64, error) {
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

// FindExportTaskByID 在携context的db下按id读取原任务，返回完整服务端记录或缺失／存储错误，不加锁。
func (s ExportRepo) FindExportTaskByID(db *gorm.DB, id uint64) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Where("id = ?", id).Take(&row).Error
	return row, err
}

// LockExportTaskByID 在携context的db下按id当前锁读原任务，返回服务端记录或缺失／存储错误。
// 行锁仅在调用方明确事务内持续到提交；普通连接的单语句锁不能覆盖后续文件操作。
func (s ExportRepo) LockExportTaskByID(db *gorm.DB, id uint64) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error
	return row, err
}

// FindExportTaskByNo 在携context的db下按taskNo读取原任务，返回记录或gorm.ErrRecordNotFound／存储错误。
func (s ExportRepo) FindExportTaskByNo(db *gorm.DB, taskNo string) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Where("task_no = ?", taskNo).Take(&row).Error
	return row, err
}

// ListExpirableExportTasks 在携context的db下按now读取至多limit条已到期完成／失败任务，返回清单或存储错误。
// 单批上限 limit，避免一次锁定过多行。
func (s ExportRepo) ListExpirableExportTasks(db *gorm.DB, now time.Time, limit int) ([]ExportTaskRow, error) {
	rows := []ExportTaskRow{}
	err := db.Table("export_task").
		Where("status IN ? AND expires_at <= ?", []string{"completed", "failed"}, now).
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// ExportTaskInsert 是受理导出申请时的写入参数，调用方负责幂等比对。
type ExportTaskInsert struct {
	TaskNo      string    // 任务号,由请求号推导,同时用作文件名
	Resource    string    // 已登记的资源名
	FileFormat  string    // csv / xlsx
	RequestedBy uint64    // 发起导出的管理员 ID
	FilterJSON  string    // 筛选条件 JSON 原文
	ExpiresAt   time.Time // 原24小时UTC下载截止
}

// ExpireExportTask 在db下按taskNo写入expired，返回存储错误，不删除文件。
// 当前SQL仅匹配任务号，调用方应在同任务锁内核对状态，不得把它描述为状态CAS。
func (s ExportRepo) ExpireExportTask(db *gorm.DB, taskNo string) error {
	return db.Model(&ExportTaskRow{}).Where("task_no = ?", taskNo).Update("status", "expired").Error
}

// QueryExportSource 在ctx下按resource、scope、filters及limit读取central原事实，返回行集合或取消／存储错误：
// 再依次应用软删除、日期区间与数据范围，最后按 limit 截断。
// limit 由调用方给成「上限 + 1」，多出的那一行用来判断是否超限。
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
		// to 是开票日，取次日零点作为开区间上界，含当天。
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

// sourceQuery 在ctx下为代码登记的table建立只读查询，不接受HTTP表名，不执行或返回业务数据。
func (s ExportRepo) sourceQuery(ctx context.Context, table string) *gorm.DB {
	return s.DB.WithContext(ctx).Table(table)
}

// ClaimExportTask 在db原事务下按in唯一任务号登记，不覆盖已存在任务；返回首次登记与存储错误。
// 不先锁缺失行；重复写入等待原提交后由LockExportTaskByNo当前读取完整原申请，不截断UUID。
func (s ExportRepo) ClaimExportTask(db *gorm.DB, in ExportTaskInsert) (bool, error) {
	res := db.Exec("INSERT INTO export_task(task_no,resource,file_format,status,requested_by,filter_json,expires_at) VALUES (?,?,?,'pending',?,?,?) ON DUPLICATE KEY UPDATE task_no=task_no", in.TaskNo, in.Resource, in.FileFormat, in.RequestedBy, in.FilterJSON, in.ExpiresAt)
	return res.RowsAffected == 1, res.Error
}

// LockExportTaskByNo 在db原事务下当前锁读taskNo，返回原任务或缺失／存储错误，不改变状态。
func (s ExportRepo) LockExportTaskByNo(db *gorm.DB, taskNo string) (ExportTaskRow, error) {
	var row ExportTaskRow
	err := db.Table("export_task").Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_no=?", taskNo).Take(&row).Error
	return row, err
}

// Expiration 在db原任务锁内读取id的原可空UTC到期事实，返回时刻或存储错误。
// SHARE保证旧RR事务也读取当前事实；私有time.Time不经文本猜测，公开原字符串保持兼容。
func (s ExportRepo) Expiration(db *gorm.DB, id uint64) (*time.Time, error) {
	var row struct {
		ExpiresAt *time.Time // 原可空下载截止，驱动按UTC解析
	}
	err := db.Table("export_task").Clauses(clause.Locking{Strength: "SHARE"}).Select("expires_at").Where("id=?", id).Take(&row).Error
	return row.ExpiresAt, err
}

// CompleteInTx 在db原任务锁内推进taskNo的pending／running，写rowCount、path及now，返回状态冲突／存储错误。
// 文件与私有范围凭据已发布才调用，不自行提交；未确认SQL提交时不得对外声称文件可下载。
func (s ExportRepo) CompleteInTx(db *gorm.DB, taskNo string, rowCount int, path string, now time.Time) error {
	res := db.Model(&ExportTaskRow{}).Where("task_no=? AND status IN ('pending','running')", taskNo).Updates(map[string]any{"status": "completed", "row_count": rowCount, "file_path": path, "completed_at": now, "error_msg": nil})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return operations.ErrConflict
	}
	return nil
}

// FailInTx 在db原任务锁内按taskNo写reason并推进pending／running为failed，返回状态冲突／存储错误。
// 不覆盖其它请求已完成或已到期的任务，不自行提交或执行文件操作。
func (s ExportRepo) FailInTx(db *gorm.DB, taskNo, reason string) error {
	res := db.Model(&ExportTaskRow{}).Where("task_no=? AND status IN ('pending','running')", taskNo).Updates(map[string]any{"status": "failed", "error_msg": reason})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return operations.ErrConflict
	}
	return nil
}
