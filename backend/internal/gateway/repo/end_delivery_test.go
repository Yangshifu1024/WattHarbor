package repo

import "testing"

func TestFrozenEndIdentityKeepsExactIntegerIDs(t *testing.T) {
	stored := `{"charge_order_id":9007199254740992,"port_id":9007199254740992,"meter":{"charged_wh":1000,"segments":[{"energy_wh":1000}]}}`
	if sameEndIdentity(stored, `{"charge_order_id":9007199254740993,"port_id":9007199254740992,"meter":{"charged_wh":1000}}`) {
		t.Fatal("different charge IDs became the same float value")
	}
	if sameEndIdentity(stored, `{"charge_order_id":9007199254740992,"port_id":9007199254740993,"meter":{"charged_wh":1000}}`) {
		t.Fatal("different port IDs became the same float value")
	}
	if !sameEndIdentity(stored, `{"charge_order_id":9007199254740992,"port_id":9007199254740992,"meter":{"charged_wh":1000}}`) {
		t.Fatal("replay without candidate segments lost the original identity")
	}
	if sameEndIdentity(stored, stored+`{}`) {
		t.Fatal("trailing JSON accepted as a frozen identity")
	}
}
