package database

// 导出任务的记录类型。导出申请与生成结果都落在 export_task，
// 落地文件路径只留在服务端，不随任何响应返回给前端。

// ExportTaskRow 对应 central_db.export_task，保存导出申请和有效期。
type ExportTaskRow struct {
	ID          uint64  `json:"id"`                                      // 导出任务主键 ID
	TaskNo      string  `json:"task_no" gorm:"column:task_no"`           // 任务号,形如 EXP + 32 位 UUID(大写无横线),同时用作文件名
	Resource    string  `json:"resource" gorm:"column:resource"`         // 导出资源名,取自 exportRegistry:orders/stations/devices/settlements/bills/reconciles
	FileFormat  string  `json:"file_format" gorm:"column:file_format"`   // 文件格式:csv / xlsx / pdf;PDF 只支持 bills 与 reconciles
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
