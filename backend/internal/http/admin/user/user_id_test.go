package user

import (
	"encoding/json"
	"testing"

	"chargepilot/internal/identity"
	"chargepilot/internal/order"
	"chargepilot/internal/payment"
)

// TestPublicUserIDsRemainExactDecimalStrings 验证用户及订单读模型把超出JavaScript安全整数范围的用户号输出为精确十进制字符串。
func TestPublicUserIDsRemainExactDecimalStrings(t *testing.T) {
	id := uint64(893327963750400123)
	for _, tc := range []struct {
		// value 是携带同一精确用户号的公开读模型。
		value any
		// key 是JSON中应保持十进制字符串的身份字段。
		key string
	}{
		{identity.ChargeUserRow{ID: id, InviterID: &id}, "id"},
		{identity.ChargeUserRow{ID: id, InviterID: &id}, "inviter_id"},
		{order.OrderView{UserID: id}, "user_id"},
		{payment.PaymentOrderView{UserID: id}, "user_id"},
	} {
		raw, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields[tc.key] != "893327963750400123" {
			t.Fatalf("%s = %#v", tc.key, fields[tc.key])
		}
	}
}
