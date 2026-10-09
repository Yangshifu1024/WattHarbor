package wallet

import (
	"chargepilot/internal/platform/dbutil"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

// RechargeReceipt 是支付回调所需的充值申请查询结果，由 wallet 持有请求表。
type RechargeReceipt struct {
	RequestID   string `gorm:"column:request_id"`   // 客户端幂等请求号
	UserID      uint64 `gorm:"column:user_id"`      // 申请人用户 ID
	AmountCents int64  `gorm:"column:amount_cents"` // 申请金额，单位分
	OpenID      string `gorm:"-"`                   // 所属用户的固定微信身份，用于核对渠道付款人
}

// LockRechargeForPayment 在调用方事务中锁定 paymentID 的充值申请。
// 支付回调必须先锁支付行再调用；无申请返回 gorm.ErrRecordNotFound，其他错误表示读取失败。
// 返回结果用于核对付款人和金额，不直接修改支付状态；调用方负责提交或回滚。
func LockRechargeForPayment(tx *gorm.DB, paymentID uint64) (RechargeReceipt, error) {
	var row RechargeReceipt
	err := tx.Table("wallet_recharge_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("payment_order_id=?", paymentID).Take(&row).Error
	if err != nil {
		return row, err
	}
	// openid 是登录身份的固定标识，不由用户修改。这里只读，不反向取得用户行锁。
	// 充值申请的创建保持用户→钱包锁序，回调保持支付→申请→钱包锁序。
	row.OpenID, err = (UserWalletRepo{}).FindUserOpenID(tx, row.UserID)
	return row, err
}

// CreditRechargeInTx 将已确认金额（分）计入钱包，并保存申请关联流水。
// 调用前须锁支付与充值申请，digest 是至少 32 字符的验签通知摘要。
// 余额、版本与流水同事务，失败全部回滚；钱包冻结不拒收到账资金。
// 重复通知由支付回调提前识别，不能再次入账。
func CreditRechargeInTx(tx *gorm.DB, userID uint64, requestID, digest string, amountCents int64) error {
	if amountCents <= 0 || len(digest) < 32 {
		return errors.New("invalid recharge credit")
	}
	var row WalletRow
	if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", userID).Take(&row).Error; err != nil {
		return err
	}
	const maxInt64 = int64(1<<63 - 1)
	if row.BalanceCents > maxInt64-amountCents {
		return errors.New("recharge balance overflow")
	}
	balance := row.BalanceCents + amountCents
	changed := tx.Table("wallet_account").Where("id=? AND version=?", row.ID, row.Version).Updates(map[string]any{"balance_cents": balance, "version": row.Version + 1})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return errors.New("recharge wallet version conflict")
	}
	return tx.Table("wallet_txn").Create(map[string]any{"txn_no": "RC" + strings.ToUpper(digest[:32]), "user_id": userID, "wallet_account_id": row.ID, "direction": "in", "amount_cents": amountCents, "balance_after_cents": balance, "biz_type": "recharge", "biz_ref": requestID, "note": "wechat recharge settled", "created_month": dbutil.MonthStart()}).Error
}
