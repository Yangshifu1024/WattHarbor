package order

import (
	"gorm.io/gorm"
	"time"
)

// NewChargeOrderNumber 在调用方事务中为 device/port 按当前北京时间生成充电单号并核对冲突。
// 返回单号或设备、端口、同号、读取错误；仅检查，不插入订单，提交时唯一键仍负责最终仲裁。
func NewChargeOrderNumber(tx *gorm.DB, device string, port uint8) (string, error) {
	no, err := ChargeOrderNumber(time.Now(), device, port)
	if err != nil {
		return "", err
	}
	var count int64
	if err := tx.Model(&ChargeOrderRecord{}).Where("order_no=?", no).Count(&count).Error; err != nil {
		return "", err
	}
	if count != 0 {
		return "", ErrChargeNumberConflict
	}
	return no, nil
}
