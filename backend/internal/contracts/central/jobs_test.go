package central_test

import (
	"encoding/json"
	"testing"

	"wattharbor/internal/contracts/central"
)

// TestJobReplyShapes 检查各任务的响应 JSON；退款失败且未提供 enabled 时应省略该字段。
func TestJobReplyShapes(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name  string
		reply any
		want  string
	}{
		{"billing", central.BillingDispatchReply{Completed: 2}, `{"completed":2}`},
		{"auto-stop", central.AutoStopReply{Stopped: 3}, `{"stopped":3}`},
		{"refund-enabled", central.RefundDispatchReply{Enabled: &enabled, Processed: 4}, `{"enabled":true,"processed":4}`},
		{"refund-disabled", central.RefundDispatchReply{Enabled: &disabled}, `{"enabled":false,"processed":0}`},
		{"refund-incomplete", central.RefundDispatchReply{Processed: 1}, `{"processed":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.reply)
			if err != nil || string(body) != tc.want {
				t.Fatalf("reply=%s err=%v want=%s", body, err, tc.want)
			}
		})
	}
}
