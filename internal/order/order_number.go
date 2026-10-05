package order

import (
	"errors"
	"fmt"
	"time"
	"unicode"
)

// ErrChargeNumberConflict 表示充电单号与设备标识、端口或既有单号冲突。
// 独立于 payment 包的哨兵：order 作为 payment 的上游被依赖方，不得反向 import payment。
var ErrChargeNumberConflict = errors.New("charge order number conflicts with device identity, port or an existing order")

// ChargeOrderNumber 为 at 原启动请求时刻、device 原设备身份与 port(1–99)生成既有业务号。
// 返回 C+北京时间14位时刻+设备号+两位端口，非法输入或超64字节返回冲突；无 IO。
// 编号早于设备确认，支付、命令及回执沿用原号；真实启动时间单独记录，不重新编号。
func ChargeOrderNumber(at time.Time, device string, port uint8) (string, error) {
	if at.IsZero() || device == "" || port == 0 || port > 99 {
		return "", ErrChargeNumberConflict
	}
	for _, r := range device {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return "", ErrChargeNumberConflict
		}
	}
	stamp := pricingTime(at)
	if len(stamp) != 14 {
		return "", ErrChargeNumberConflict
	}
	no := fmt.Sprintf("C%s%s%02d", stamp, device, port)
	if len(no) > 64 {
		return "", ErrChargeNumberConflict
	}
	return no, nil
}

// pricingTime 将 at 请求时刻转为北京时间14位文本，无 IO 或错误。
func pricingTime(at time.Time) string {
	return at.In(time.FixedZone("CST", 8*3600)).Format("20060102150405")
}
