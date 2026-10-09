package settlement

import (
	"fmt"
	"time"
	"unicode"
)

// CalculationNumber 为当前订单生成 B 前缀结算号，旧订单生成稳定的 FEE 编号。
// 历史数字、CH 和测试编号沿用首次规则。
func CalculationNumber(orderNo string, chargeOrderID uint64) string {
	if prefixedChargeOrderNumber(orderNo) {
		return "B" + orderNo[1:]
	}
	return fmt.Sprintf("FEE%020d", chargeOrderID)
}

// prefixedChargeOrderNumber 检查 C 前缀、时间戳、设备号和两位端口号组成的订单编号。
func prefixedChargeOrderNumber(no string) bool {
	// 编号由 C、14 位有效时间、设备号和非零两位端口号组成。
	if len(no) < 18 || len(no) > 64 || no[0] != 'C' {
		return false
	}
	if _, err := time.Parse("20060102150405", no[1:15]); err != nil {
		return false
	}
	port := no[len(no)-2:]
	if port[0] < '0' || port[0] > '9' || port[1] < '0' || port[1] > '9' || port == "00" {
		return false
	}
	for _, r := range no[15 : len(no)-2] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
