package device

import (
	"context"
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
)

// FaultInputError 表示工单输入不满足原准入；存储错误不得转换为输入或记录缺失。
type FaultInputError struct {
	Message string // 原公开准入文案
}

// Error 返回 e 原公开文案，不执行IO。
func (e *FaultInputError) Error() string { return e.Message }

// FaultSubmission 保留用户原设备、类型、文本和图片输入；历史port_no未入库，不增加推断端口。
type FaultSubmission struct {
	DeviceID    string   `json:"device_id"`   // 原设备编号，非空最多64字节；兼容未知设备报告
	PortNo      uint8    `json:"port_no"`     // 原兼容输入；既有表无端口字段，不保存或推断
	FaultType   string   `json:"fault_type"`  // 原mechanical／electrical／communication／display／other
	Description *string  `json:"description"` // 原可空描述，最多2000字
	Images      []string `json:"images"`      // 原图片地址数组，最多9项，nil保留原JSON null
}

// FaultSubmissionResult 是首次报告的原自增主键与open状态，提交失败不保留报告或事件。
type FaultSubmissionResult struct {
	ID     uint64 `json:"id"`     // 原工单自增主键
	Status string `json:"status"` // 原首次open
}

// FaultService 拥有设备报修准入、完整流转事务和所属查询；连接由进程管理。
type FaultService struct {
	DB *gorm.DB // central所属工单连接或调用方事务
}

// Report 在 ctx 下按 userID 原用户和 in 原内容验证后原子创建报告及首条可见事件。
// 返回原主键／open或输入／存储错误；不写设备配置，不依据未知设备号创造设备或端口事实。
func (s FaultService) Report(ctx context.Context, userID uint64, in FaultSubmission) (FaultSubmissionResult, error) {
	if userID == 0 {
		return FaultSubmissionResult{}, &FaultInputError{"报修请求无效"}
	}
	if in.DeviceID == "" || len(in.DeviceID) > 64 || !validFaultType(in.FaultType) {
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

// validFaultType 核对 value 原五类故障类型，无IO。
func validFaultType(value string) bool {
	switch value {
	case "mechanical", "electrical", "communication", "display", "other":
		return true
	}
	return false
}

// validFaultPage 核对 q 原有界分页，无IO；状态由调用者的业务族另行验证。
func validFaultPage(q persist.PageQuery) bool {
	return q.Page >= 1 && q.Page <= 100000 && q.PageSize >= 1 && q.PageSize <= 100
}

// UserReports 在 ctx 下按 userID 和 q 原分页只读取自己的报告，返回原投影页或输入／存储错误，无写入。
func (s FaultService) UserReports(ctx context.Context, userID uint64, q persist.PageQuery) (persist.Page[map[string]any], error) {
	if userID == 0 || !validFaultPage(q) {
		return persist.Page[map[string]any]{}, &FaultInputError{"分页或用户编号无效"}
	}
	return ListUserFaultReports(ctx, s.DB, userID, q.Page, q.PageSize)
}

// UserHistory 在 ctx 下先核对 userID 对 id 原报告归属，再读取可见事件；不存在／他人同为缺失。
// 返回原正序数组或输入／缺失／存储错误，不把数据库故障隐藏成404，不泄漏内部不可见事件。
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

// requireFaultPermission 核对 actor 原工单权限，返回禁止或nil，不执行IO。
func requireFaultPermission(actor operations.Profile, permission string) error {
	if !actor.HasPermission(permission) {
		return operations.ErrForbidden
	}
	return nil
}
