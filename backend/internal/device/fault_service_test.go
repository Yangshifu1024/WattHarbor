package device

import (
	"context"
	"errors"
	"strings"
	"testing"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
)

// TestFaultAdmissionBeforeStorage 用空连接验证无权限和不合规输入在任何IO之前拒绝。
func TestFaultAdmissionBeforeStorage(t *testing.T) {
	s := FaultService{}
	ctx := context.Background()
	long := strings.Repeat("字", 2001)
	for _, in := range []FaultSubmission{{}, {DeviceID: "device", FaultType: "bad"}, {DeviceID: "device", FaultType: "other", Description: &long}, {DeviceID: "device", FaultType: "other", Images: make([]string, 10)}} {
		_, err := s.Report(ctx, 1, in)
		var input *FaultInputError
		if !errors.As(err, &input) {
			t.Fatalf("report=%v", err)
		}
	}
	if err := s.Dispatch(ctx, ops.AuditContext{}, 1, FaultDispatch{AssignedTo: 1}); !errors.Is(err, operations.ErrForbidden) {
		t.Fatal(err)
	}
	actor := operations.Profile{ID: 1, Permissions: []string{"fault.resolve", "fault.dispatch"}}
	for _, in := range []FaultResolution{{Status: "closed", Note: &long}, {Status: "fixed"}, {Status: "fixed", Note: new(string)}, {Status: "dispatched"}} {
		var input *FaultInputError
		if err := s.Resolve(ctx, ops.AuditContext{Actor: actor}, 1, in); !errors.As(err, &input) {
			t.Fatal(err)
		}
	}
	var input *FaultInputError
	if err := s.Dispatch(ctx, ops.AuditContext{Actor: actor}, 1, FaultDispatch{}); !errors.As(err, &input) {
		t.Fatal(err)
	}
}
