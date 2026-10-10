package device

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/persist"
	"context"
	"encoding/json"
	"gorm.io/gorm"
)

// FaultInputError 表示工单请求不合法；存储故障不能当作输入错误或记录缺失。
type FaultInputError struct {
	Message string // 公开校验提示
}

// Error 返回公开工单输入提示。
func (e *FaultInputError) Error() string { return e.Message }

// FaultSubmission 保存用户提交的设备、类型、说明和图片；port_no 未入库，不推断端口。
type FaultSubmission struct {
	DeviceID    string    `json:"device_id"`   // 设备编号，非空最多64字节；兼容未知设备报告
	PortNo      uint8     `json:"port_no"`     // 兼容输入；既有表无端口字段，不保存或推断
	FaultType   FaultType `json:"fault_type"`  // device_fault_report.fault_type 五值，见 FaultType
	Description *string   `json:"description"` // 可空描述，最多2000字
	Images      []string  `json:"images"`      // 图片地址数组，最多9项，nil 保留 JSON null
}

// FaultSubmissionResult 返回首次报告主键和 open 状态，失败不留下报告或事件。
type FaultSubmissionResult struct {
	ID     uint64      `json:"id"`     // 工单自增主键
	Status FaultStatus `json:"status"` // 首次 open
}

// FaultService 负责报修校验、状态事务和查询，连接由进程管理。
type FaultService struct {
	DB *gorm.DB // central 所属工单连接或调用方事务
}

// Report 校验本人请求后，在同一事务创建 open 报告和首条可见事件；未知设备号不会补建设备或端口。
func (s FaultService) Report(ctx context.Context, userID uint64, in FaultSubmission) (FaultSubmissionResult, error) {
	if userID == 0 {
		return FaultSubmissionResult{}, &FaultInputError{"报修请求无效"}
	}
	// empty 先于 Known 短路：两者合并会失去必填语义（Known 对空串返回 false，与本组合等价，
	// 但保留短路是为了让“设备无效”与“故障类型无效”的判定顺序与原实现一致）。
	if in.DeviceID == "" || len(in.DeviceID) > 64 || in.FaultType == "" || !in.FaultType.Known() {
		return FaultSubmissionResult{}, &FaultInputError{"设备或故障类型无效"}
	}
	if in.Description != nil && len([]rune(*in.Description)) > MaxFaultNoteLength {
		return FaultSubmissionResult{}, &FaultInputError{"故障描述过长"}
	}
	if len(in.Images) > 9 {
		return FaultSubmissionResult{}, &FaultInputError{"图片最多 9 张"}
	}
	images, err := json.Marshal(in.Images)
	if err != nil {
		return FaultSubmissionResult{}, err
	}
	out := FaultSubmissionResult{Status: FaultOpen}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out.ID, err = InsertFaultReport(tx, FaultReportInput{DeviceID: in.DeviceID, UserID: userID, FaultType: in.FaultType, Description: in.Description, ImagesJSON: string(images)})
		return err
	})
	return out, err
}

// validFaultPage 检查分页上下限，状态由对应接口用例校验。
func validFaultPage(q persist.PageQuery) bool {
	return q.Page >= 1 && q.Page <= 100000 && q.PageSize >= 1 && q.PageSize <= 100
}

// UserReports 分页读取本人报告。
func (s FaultService) UserReports(ctx context.Context, userID uint64, q persist.PageQuery) (persist.Page[map[string]any], error) {
	if userID == 0 || !validFaultPage(q) {
		return persist.Page[map[string]any]{}, &FaultInputError{"分页或用户编号无效"}
	}
	return ListUserFaultReports(ctx, s.DB, userID, q.Page, q.PageSize)
}

// UserHistory 核对本人报告后正序读取用户可见事件。
// 他人报告和不存在都按缺失处理，存储故障不能隐藏为 404。
func (s FaultService) UserHistory(ctx context.Context, userID, id uint64) ([]map[string]any, error) {
	if userID == 0 || id == 0 {
		return nil, &FaultInputError{"报修编号无效"}
	}
	owned, err := OwnsFaultReport(ctx, s.DB, id, userID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, gorm.ErrRecordNotFound
	}
	return ListUserVisibleFaultEvents(ctx, s.DB, id)
}

// requireFaultPermission 检查当前工单权限。
func requireFaultPermission(actor operations.Profile, permission string) error {
	if !actor.HasPermission(permission) {
		return operations.ErrForbidden
	}
	return nil
}
