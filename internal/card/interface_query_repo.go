package card

import (
	"context"

	"gorm.io/gorm"
)

// CardAvailableCents 返回卡对应钱包的可用余额。
// ctx 控制取消，db 为原连接或事务，cardNo 为原十进制身份；返回分值或缺失／存储错误。
// 卡、用户或钱包无效均返回 gorm.ErrRecordNotFound，消费者据此返回 valid=false，不泄露绑定身份。
// 单条只读联表投影保证同次读取的有效性和余额，负值下限零，不加锁、扣款或提交。
func CardAvailableCents(ctx context.Context, db *gorm.DB, cardNo string) (int64, error) {
	var row struct {
		BalanceCents int64 `gorm:"column:balance_cents"` // 原余额减冻结额的分值
	}
	err := db.WithContext(ctx).Table("online_card c").
		Select("w.balance_cents-w.frozen_cents AS balance_cents").
		Joins("JOIN user u ON u.id=c.user_id AND u.status='active' AND u.deleted_at IS NULL").
		Joins("JOIN wallet_account w ON w.user_id=c.user_id AND w.status='active' AND w.deleted_at IS NULL").
		Where("c.card_no=? AND c.status='active'", cardNo).Take(&row).Error
	if err != nil {
		return 0, err
	}
	if row.BalanceCents < 0 {
		return 0, nil
	}
	return row.BalanceCents, nil
}
