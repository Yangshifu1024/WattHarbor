package ops

import (
	"reflect"
	"testing"

	"wattharbor/internal/domain/operations"
	"wattharbor/internal/http/admin"
)

func TestRetiredPermissionsPreserveFeedbackWalletAndDeviceAlerts(t *testing.T) {
	codes := []string{
		"customer_service.read", "feedback.read", "customer_service.create", "feedback.reply", "customer_service.update", "wallet.read", "customer_service.delete",
		"ota.read", "ota.package.create", "ota.package.delete", "ota.schedule.create", "ota.schedule.trigger", "settings.ota.update",
		"alert.rule.create", "alert.rule.update", "alert.rule.delete", "alert.subscription.create", "alert.read", "alert.ack", "alert.risk_config.update",
		"membership.create", "pricing.template.create",
	}
	want := []string{"feedback.read", "feedback.reply", "wallet.read", "alert.read", "alert.ack"}
	if got := operations.WithoutRetiredPermissions(codes); !reflect.DeepEqual(got, want) {
		t.Fatalf("visible permissions = %v, want %v", got, want)
	}
}

// TestNormalizeRowsTurnsTinyintFlagsIntoBooleans 验证数据库 tinyint(1) 在响应中转换为布尔值。
// 设备能力字段必须输出 true/false，保持前端严格布尔判断与后端能力校验一致。
func TestNormalizeRowsTurnsTinyintFlagsIntoBooleans(t *testing.T) {
	rows := []map[string]any{{
		"device_id":               "demo_DC589-0001",
		"reports_energy":          int64(1),
		"reports_segmented_power": int64(0),
		"enabled":                 int64(1),
		"station_id":              int64(14),
		"status":                  "active",
	}}
	admin.NormalizeRows(rows)

	row := rows[0]
	for _, name := range []string{"reports_energy", "enabled"} {
		if v, ok := row[name].(bool); !ok || !v {
			t.Errorf("%s = %#v，期望 true", name, row[name])
		}
	}
	if v, ok := row["reports_segmented_power"].(bool); !ok || v {
		t.Errorf("reports_segmented_power = %#v，期望 false", row["reports_segmented_power"])
	}
	// 布尔归一化不应改变数字和字符串列的类型。
	if _, ok := row["station_id"].(int64); !ok {
		t.Errorf("station_id 被改成了 %#v，不该动", row["station_id"])
	}
	if row["status"] != "active" {
		t.Errorf("status = %#v，不该动", row["status"])
	}
}
