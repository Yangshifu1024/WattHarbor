package card

import (
	"context"

	"gorm.io/gorm"
)

// CardAvailableCents 返回卡对应钱包的可用余额。
//

// 调用方据此报 valid=false，不区分两者。
func CardAvailableCents(ctx context.Context, db *gorm.DB, cardNo string) (int64, error) {
	var row struct {
		BalanceCents int64 `gorm:"column:balance_cents"`
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
