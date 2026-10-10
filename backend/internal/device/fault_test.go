package device

import (
	"errors"
	"testing"
)

func TestCanTransitionFaultOnlyWalksTheLinearPath(t *testing.T) {
	allowed := [][2]FaultStatus{
		{FaultOpen, FaultDispatched},
		{FaultDispatched, FaultFixed},
		{FaultFixed, FaultClosed},
	}
	for _, pair := range allowed {
		if err := CanTransitionFault(pair[0], pair[1]); err != nil {
			t.Fatalf("%s → %s 应允许：%v", pair[0], pair[1], err)
		}
	}
	// 跳级、回退、以及任何未知状态都要拒绝
	denied := [][2]FaultStatus{
		{FaultOpen, FaultFixed},              // 跳级：没人修过就标记已修
		{FaultOpen, FaultClosed},             // 跳级到终态
		{FaultDispatched, FaultClosed},       // 跳级
		{FaultFixed, FaultDispatched},        // 回退
		{FaultClosed, FaultFixed},            // 终态回退
		{FaultClosed, FaultOpen},             // 终态重开
		{FaultStatus(""), FaultFixed},        // 状态缺失
		{FaultOpen, FaultStatus("resolved")}, // 未知目标
	}
	for _, pair := range denied {
		if err := CanTransitionFault(pair[0], pair[1]); !errors.Is(err, ErrFaultTransition) {
			t.Fatalf("%q → %q 应拒绝", pair[0], pair[1])
		}
	}
}

func TestCanTransitionFaultIsIdempotentForSameStatus(t *testing.T) {
	// 同状态提交允许重放。
	for _, status := range []FaultStatus{FaultOpen, FaultDispatched, FaultFixed, FaultClosed} {
		if err := CanTransitionFault(status, status); err != nil {
			t.Fatalf("同状态重放 %s 被拒：%v", status, err)
		}
	}
}

func TestFaultNoteRequiredOnlyForFixed(t *testing.T) {
	if !FaultNoteRequired(FaultFixed) {
		t.Fatal("标记已修复必须写说明")
	}
	for _, status := range []FaultStatus{FaultOpen, FaultDispatched, FaultClosed} {
		if FaultNoteRequired(status) {
			t.Fatalf("%s 不应强制写说明", status)
		}
	}
}
