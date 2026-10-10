package device

import "errors"

// 报修只允许 open→dispatched→fixed→closed 顺序变更，不能跳级或回退。
// 权限由调用方按当前处理人核对。

// FaultStatus 是 device_fault_report.status 列的报修单状态，用于受理、指派、
// 修复与关闭的顺序流转。取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致。
// 注意 device_fault_report_event.event_type 是另一列，取值含 reassigned，不属于本词表。
type FaultStatus string

// 报修单状态。
const (
	FaultOpen       FaultStatus = "open"       // 待受理
	FaultDispatched FaultStatus = "dispatched" // 已指派处理人
	FaultFixed      FaultStatus = "fixed"      // 已修复
	FaultClosed     FaultStatus = "closed"     // 已关闭
)

// Valid 判断是否为该列已定义的取值。仅用于识别未知值，不决定任何状态能否流转；
// 读取路径遇到未知值时保持原值透传，流转合法性由 CanTransitionFault 判断。
func (s FaultStatus) Valid() bool {
	switch s {
	case FaultOpen, FaultDispatched, FaultFixed, FaultClosed:
		return true
	default:
		return false
	}
}

// ErrFaultTransition 表示这次状态变更不被允许。
var ErrFaultTransition = errors.New("报修单状态不允许此流转")

// CanTransitionFault 检查相邻状态变更，同状态允许重放，跳级或回退返回 ErrFaultTransition。
func CanTransitionFault(from, to FaultStatus) error {
	if from == to {
		return nil
	}
	switch {
	case from == FaultOpen && to == FaultDispatched:
		return nil
	case from == FaultDispatched && to == FaultFixed:
		return nil
	case from == FaultFixed && to == FaultClosed:
		return nil
	default:
		return ErrFaultTransition
	}
}

// MaxFaultNoteLength 是处理备注与修复说明的字数上限。
const MaxFaultNoteLength = 2000

// FaultNoteRequired 判断目标状态是否需要说明；标记已修复时必须填写用户可见说明。
func FaultNoteRequired(to FaultStatus) bool {
	return to == FaultFixed
}
