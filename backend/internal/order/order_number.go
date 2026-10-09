package order

import (
	"errors"
	"fmt"
	"time"
	"unicode"
)

// ErrChargeNumberConflict 表示设备、端口无效或充电单号冲突，定义在订单模块以避免反向依赖支付。
var ErrChargeNumberConflict = errors.New("charge order number conflicts with device identity, port or an existing order")

// ChargeOrderNumber 按请求时间、设备编号与端口号（1–99）生成充电业务单号。
// 格式为 C+北京时间14位时间+设备号+两位端口；输入无效或超过64字节时报冲突。
// 支付、设备命令与回执使用同一编号，实际起充时间另行记录。
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

// pricingTime 将 at 请求时刻转为北京时间14位文本。
func pricingTime(at time.Time) string {
	return at.In(time.FixedZone("CST", 8*3600)).Format("20060102150405")
}
