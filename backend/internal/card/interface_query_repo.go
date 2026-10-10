package card

import (
	identitydomain "chargepilot/internal/domain/identity"
	walletdomain "chargepilot/internal/domain/wallet"
	"context"

	"gorm.io/gorm"
)

// CardAvailableCents 用一次联表查询读取卡对应钱包的可用余额，单位分，下限为零。
// 卡、用户或钱包无效时返回 gorm.ErrRecordNotFound，公开接口返回 valid=false，不泄露绑定用户。
func CardAvailableCents(ctx context.Context, db *gorm.DB, cardNo string) (int64, error) {
	var row struct {
		BalanceCents int64 `gorm:"column:balance_cents"` // 余额减冻结额的金额（分）
	}
	err := db.WithContext(ctx).Table("online_card c").
		Select("w.balance_cents-w.frozen_cents AS balance_cents").
		// 三个 status 分别属于三张表的三个词表，均以绑定参数传入，避免同串不同义混淆：
		// u.status → identitydomain.UserStatus、w.status → walletdomain.AccountStatus、
		// c.status → 本包 CardStatus。三列取值串部分相同但互不可赋值。
		Joins("JOIN user u ON u.id=c.user_id AND u.status=? AND u.deleted_at IS NULL", identitydomain.UserActive).
		Joins("JOIN wallet_account w ON w.user_id=c.user_id AND w.status=? AND w.deleted_at IS NULL", walletdomain.AccountActive).
		Where("c.card_no=? AND c.status=?", cardNo, CardActive).Take(&row).Error
	if err != nil {
		return 0, err
	}
	if row.BalanceCents < 0 {
		return 0, nil
	}
	return row.BalanceCents, nil
}
