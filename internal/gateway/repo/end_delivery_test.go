package repo

import "testing"

// TestFrozenEndIdentityKeepsExactIntegerIDs 接收 t，验证超出浮点精确范围的订单/端口身份仍区分，忽略 segments 的重放保留身份且尾随 JSON 被拒绝；仅验证纯比较，不访问数据库。
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
