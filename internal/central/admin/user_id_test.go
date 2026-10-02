package admin

import (
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"testing"
)

func TestPublicUserIDsRemainExactDecimalStrings(t *testing.T) {
	id := uint64(893327963750400123)
	for _, tc := range []struct {
		value any
		key   string
	}{
		{database.ChargeUserRow{ID: id, InviterID: &id}, "id"},
		{database.ChargeUserRow{ID: id, InviterID: &id}, "inviter_id"},
		{OrderView{UserID: id}, "user_id"},
		{orderstore.PaymentOrderView{UserID: id}, "user_id"},
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
