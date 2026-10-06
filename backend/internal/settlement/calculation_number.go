package settlement

import (
	"fmt"
	"time"
	"unicode"
)

// CalculationNumber 根据 orderNo 生成 B 前缀原结算身份；旧订单号按 chargeOrderID 返回稳定 FEE 编号。
// 无数据库或渠道调用，无错误返回；历史数字、CH 及测试编号继续沿用原编号规则。
func CalculationNumber(orderNo string, chargeOrderID uint64) string {
	if prefixedChargeOrderNumber(orderNo) {
		return "B" + orderNo[1:]
	}
	return fmt.Sprintf("FEE%020d", chargeOrderID)
}

// prefixedChargeOrderNumber 判断 no 是否为当前 C 时间戳＋设备＋两位端口编号，返回有效标志，无 IO。
func prefixedChargeOrderNumber(no string) bool {
	// 身份包含 C、14 位有效时间、非空设备编号以及非零的两位端口号。
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
