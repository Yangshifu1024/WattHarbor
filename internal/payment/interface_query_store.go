package payment

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// FindDevelopmentPayment 按商户单号与用户取一笔微信支付单。

// DevelopmentPaymentRow 是开发支付确认要核对的一笔微信支付单。
type DevelopmentPaymentRow struct {
	ID         uint64    `gorm:"column:id"`
	OrderNo    string    `gorm:"column:order_no"`
	BizType    string    `gorm:"column:biz_type"`
	TotalCents int64     `gorm:"column:total_cents"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	OpenID     string    `gorm:"column:openid"`
}

// 同时带出 openid：模拟支付要按它模拟用户身份。
func FindDevelopmentPayment(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (DevelopmentPaymentRow, error) {
	var row DevelopmentPaymentRow
	err := db.WithContext(ctx).Table("payment_order p").
		Select("p.id,p.order_no,p.biz_type,p.total_cents,p.created_at,u.openid").
		Joins("JOIN user u ON u.id=p.user_id").
		Where("p.order_no=? AND p.user_id=? AND p.pay_method='wechat'", orderNo, userID).Take(&row).Error
	return row, err
}
