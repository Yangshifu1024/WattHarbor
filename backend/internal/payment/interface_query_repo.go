package payment

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// DevelopmentPaymentRow 是开发支付确认要核对的一笔微信支付单。
type DevelopmentPaymentRow struct {
	ID         uint64    `gorm:"column:id"`          // 支付主键
	OrderNo    string    `gorm:"column:order_no"`    // 商户单号
	BizType    string    `gorm:"column:biz_type"`    // charge 或 wallet_recharge
	TotalCents int64     `gorm:"column:total_cents"` // 应付金额，分
	CreatedAt  time.Time `gorm:"column:created_at"`  // 创建 UTC 时刻，用户模拟确认沿用
	OpenID     string    `gorm:"column:openid"`      // 用户微信身份，仅供内部模拟核对
}

// FindDevelopmentPayment 用 db 按 orderNo、userID 读取微信支付及付款人。
// 返回查询结果，缺关联为 gorm.ErrRecordNotFound；不更新支付，模式装配由 cmd 限制。
func FindDevelopmentPayment(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (DevelopmentPaymentRow, error) {
	var row DevelopmentPaymentRow
	err := db.WithContext(ctx).Table("payment_order p").
		Select("p.id,p.order_no,p.biz_type,p.total_cents,p.created_at,u.openid").
		Joins("JOIN user u ON u.id=p.user_id").
		Where("p.order_no=? AND p.user_id=? AND p.pay_method='wechat'", orderNo, userID).Take(&row).Error
	return row, err
}
