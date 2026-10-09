package central_test

import (
	"encoding/json"
	"testing"

	"chargepilot/internal/contracts/central"
)

// TestJobReplyShapes 用 t 核对共享回复的原 JSON 形状，尤其退款失败不能增加 enabled；无网络或资金 IO。
func TestJobReplyShapes(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name  string // 被核对的成功、禁用或未完成回执场景。
		reply any    // 共享端点 DTO；只验证序列化形状，不模拟用例处理。
		want  string // 原 wire JSON，字段名和退款 enabled 的缺失性必须保持。
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
