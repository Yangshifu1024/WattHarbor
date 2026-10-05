package device

import "errors"

// 设备报修单的状态流转。
//
// open → dispatched → fixed → closed 是唯一的推进路径，不能跳级，也不能回退：
// fixed 表示有人已经动手修过，closed 表示处理人已完成收尾。
// 跳级会让"是否真的有人修过"这件事失去依据，回退会让已关闭的工单重新打开。
//
// 这里只判定状态是否可推进；"谁有权推进"由调用方按当前处理人判断。

// 报修单状态。
const (
	FaultOpen       = "open"       // 待受理
	FaultDispatched = "dispatched" // 已指派处理人
	FaultFixed      = "fixed"      // 已修复
	FaultClosed     = "closed"     // 已关闭
)

// ErrFaultTransition 表示这次状态变更不被允许。
var ErrFaultTransition = errors.New("报修单状态不允许此流转")

// CanTransitionFault 按原from／to状态判定线性推进，允许返回nil，跳级／回退返回ErrFaultTransition，无IO。
// 相同状态视为幂等重放，返回 nil——重复提交同一次修复不该报错。
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

// FaultNoteRequired 按to原目标状态返回是否必须填写说明，无IO。
// 标记为"已修复"必须留下说明——用户会看到它，没有说明的"已修复"等于没修。
func FaultNoteRequired(to string) bool {
	return to == FaultFixed
}
