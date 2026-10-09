package user

import (
	"encoding/json"
	"testing"

	"chargepilot/internal/identity"
	"chargepilot/internal/order"
	"chargepilot/internal/payment"
)

func TestPublicUserIDsRemainExactDecimalStrings(t *testing.T) {
	id := uint64(893327963750400123)
	for _, tc := range []struct {
		value any
		key   string
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
