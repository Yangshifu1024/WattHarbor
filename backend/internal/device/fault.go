package device

import "errors"

// 报修只允许 open→dispatched→fixed→closed 顺序变更，不能跳级或回退。
// 权限由调用方按当前处理人核对。

// 报修单状态。
const (
	FaultOpen       = "open"       // 待受理
	FaultDispatched = "dispatched" // 已指派处理人
	FaultFixed      = "fixed"      // 已修复
	FaultClosed     = "closed"     // 已关闭
)

// ErrFaultTransition 表示这次状态变更不被允许。
var ErrFaultTransition = errors.New("报修单状态不允许此流转")

// CanTransitionFault 检查相邻状态变更，同状态允许重放，跳级或回退返回 ErrFaultTransition。
func CanTransitionFault(from, to string) error {
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
func FaultNoteRequired(to string) bool {
	return to == FaultFixed
}
