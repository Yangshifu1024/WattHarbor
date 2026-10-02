package database

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// C 端钱包的读写。支付单、退款预留与台账分账在 payment_* / wallet_refund_store 里，
// 这里承接用户侧可见的钱包余额、流水、充值单与退款申请。
//
// 余额永远不是"算出来"的：wallet_account 上的 balance_cents 与 frozen_cents
// 是唯一事实，流水 wallet_txn 是它的审计凭证。可用额 = 余额 - 冻结额，
// 不允许出现负数——冻结额大于余额说明数据已经不一致，宁可显示 0 也不要
// 让前端算出一个负数去下单。

// WalletBalance 是钱包余额的可读投影。
type WalletBalance struct {
	BalanceCents int64  `gorm:"column:balance_cents"` // 账户总余额，单位分
	FrozenCents  int64  `gorm:"column:frozen_cents"`  // 已冻结金额，单位分
	Status       string `gorm:"column:status"`        // active 可用 / frozen 因风控冻结
}

// AvailableCents 返回可用余额，不为负。
func (b WalletBalance) AvailableCents() int64 {
	if b.BalanceCents <= b.FrozenCents {
		return 0
	}
	return b.BalanceCents - b.FrozenCents
}

// WalletUserStore 是 C 端钱包的读写入口。
type WalletUserStore struct{ DB *gorm.DB }

// FindWalletBalance 取钱包余额。账户不存在时返回 gorm.ErrRecordNotFound，
// 调用方据此显示全零的新钱包。
func (s WalletUserStore) FindWalletBalance(ctx context.Context, userID uint64) (WalletBalance, error) {
	var row WalletBalance
	err := s.DB.WithContext(ctx).Table("wallet_account").
		Where("user_id = ? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// ListWalletTxns 分页返回资金流水，direction 为空表示不限收支方向。
func (s WalletUserStore) ListWalletTxns(ctx context.Context, userID uint64, direction string, page, pageSize int) (Page[map[string]any], error) {
	out := Page[map[string]any]{Items: []map[string]any{}, Page: page, PageSize: pageSize}
	query := s.DB.WithContext(ctx).Table("wallet_txn").Where("user_id = ?", userID)
	if direction != "" {
		query = query.Where("direction = ?", direction)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}

// WalletRechargeRow 是充值申请的一行，联表带出支付单的状态与有效期。
// 状态与有效期取自 payment_order：充值申请本身只记"发起过"，
// 能不能付要看支付单有没有过期。
type WalletRechargeRow struct {
	RequestID   string     `json:"request_id" gorm:"column:request_id"`
	AmountCents int64      `json:"amount_cents" gorm:"column:amount_cents"` // 申请金额，单位分
	Status      string     `json:"status" gorm:"column:status"`             // initiated 待支付 / paid 已到账
	ExpiredAt   *time.Time `json:"expires_at" gorm:"column:expired_at"`     // 支付截止时间
	CreatedAt   time.Time  `json:"created_at" gorm:"column:created_at"`
	CanPay      bool       `json:"can_pay" gorm:"-"` // 未支付且未过期才允许继续付款
}

// ListWalletRecharges 分页返回用户的充值申请，按申请时间倒序。
func (s WalletUserStore) ListWalletRecharges(ctx context.Context, userID uint64, page, pageSize int) (Page[WalletRechargeRow], error) {
	out := Page[WalletRechargeRow]{Items: []WalletRechargeRow{}, Page: page, PageSize: pageSize}
	query := s.DB.WithContext(ctx).Table("wallet_recharge_request r").
		Joins("JOIN payment_order p ON p.id=r.payment_order_id").Where("r.user_id=?", userID)
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select("r.request_id,r.amount_cents,r.created_at,p.status,p.expired_at").
		Order("r.created_at DESC,r.request_id").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}

// WalletRefundRequestRow 是用户可见的余额退款申请。
type WalletRefundRequestRow struct {
	RequestID   string    `gorm:"column:request_id"`
	AmountCents int64     `gorm:"column:amount_cents"`
	Reason      *string   `gorm:"column:reason"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// LockWalletAccount 在行锁下取钱包。取不到返回 gorm.ErrRecordNotFound，
// 调用方据此走开户分支。
func (s WalletUserStore) LockWalletAccount(tx *gorm.DB, userID uint64) (WalletRow, error) {
	var row WalletRow
	err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// WalletRow 是钱包账户的完整行，充值与退款都要读余额与冻结额。
type WalletRow struct {
	ID           uint64 `gorm:"column:id"`
	UserID       uint64 `gorm:"column:user_id"`
	BalanceCents int64  `gorm:"column:balance_cents"`
	FrozenCents  int64  `gorm:"column:frozen_cents"`
	Status       string `gorm:"column:status"`
	Version      uint64 `gorm:"column:version"`
}

// OpenWalletAccount 为新用户开户，初始余额与冻结额均为 0。
func (s WalletUserStore) OpenWalletAccount(tx *gorm.DB, userID uint64) error {
	return tx.Table("wallet_account").Create(map[string]any{
		"user_id": userID, "balance_cents": 0, "frozen_cents": 0, "status": "active",
	}).Error
}

// FindUserOpenID 取用户的 openid。退款要按原渠道拆回支付渠道，
// 没有 openid 就无法确定原路退回的账号，按无效处理。
func (s WalletUserStore) FindUserOpenID(tx *gorm.DB, userID uint64) (string, error) {
	var openid string
	err := tx.Table("user").Where("id=? AND deleted_at IS NULL", userID).Pluck("openid", &openid).Error
	return openid, err
}

// LockUser 在行锁下取用户。退款冻结要确认用户仍存在且未软删除。
func (s WalletUserStore) LockUser(tx *gorm.DB, userID uint64) (map[string]any, error) {
	var row map[string]any
	err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// WalletRechargeRequest 是充值申请存档，request_json 供幂等重放比对内容。
type WalletRechargeRequest struct {
	RequestID      string
	UserID         uint64
	AmountCents    int64
	PaymentOrderID uint64
	RequestJSON    string
}

// FindRechargeRequest 按请求号取充值申请，取不到返回 gorm.ErrRecordNotFound。
func (s WalletUserStore) FindRechargeRequest(tx *gorm.DB, requestID string) (WalletRechargeRequest, error) {
	var row WalletRechargeRequest
	err := tx.Table("wallet_recharge_request").Where("request_id=?", requestID).Take(&row).Error
	return row, err
}

// InsertRechargeRequest 落一条充值申请。
func (s WalletUserStore) InsertRechargeRequest(tx *gorm.DB, in WalletRechargeRequest) error {
	return tx.Table("wallet_recharge_request").Create(map[string]any{
		"request_id": in.RequestID, "user_id": in.UserID, "amount_cents": in.AmountCents,
		"payment_order_id": in.PaymentOrderID, "request_json": in.RequestJSON,
	}).Error
}

// WalletRefundRequest 是余额退款申请的一行。幂等重放要逐字段比对，
// 所以用具名字段而不是通用 map。
type WalletRefundRequest struct {
	RequestID    string  `gorm:"column:request_id"`
	UserID       uint64  `gorm:"column:user_id"`
	AmountCents  int64   `gorm:"column:amount_cents"`  // 申请退款金额，单位分
	Reason       *string `gorm:"column:reason"`        // 申请理由，可空
	ResponseJSON string  `gorm:"column:response_json"` // 首次返回体，重放时原样回给调用方
}

// FindRefundRequest 按请求号取余额退款申请，取不到返回 gorm.ErrRecordNotFound。
func (s WalletUserStore) FindRefundRequest(tx *gorm.DB, requestID string) (WalletRefundRequest, error) {
	var row WalletRefundRequest
	err := tx.Table("wallet_refund_request").Where("request_id=?", requestID).Take(&row).Error
	return row, err
}

// InsertRefundRequest 落一条余额退款申请。
func (s WalletUserStore) InsertRefundRequest(tx *gorm.DB, requestID string, userID uint64, amountCents int64, reason *string) error {
	return tx.Table("wallet_refund_request").Create(map[string]any{
		"request_id": requestID, "user_id": userID, "amount_cents": amountCents, "reason": reason,
	}).Error
}

// _unused 保留给后续按 reason 检索的查询。

// CountRecentRefundRequests 数出用户在 since 之后提交过的退款申请。
// 频次超限会触发风控冻结，这是判定的事实依据。
func (s WalletUserStore) CountRecentRefundRequests(tx *gorm.DB, userID uint64, since time.Time) (int64, error) {
	var count int64
	err := tx.Table("wallet_refund_request").
		Where("user_id=? AND created_at>=?", userID, since).Count(&count).Error
	return count, err
}

// InsertRiskFreeze 落一条风控冻结记录并返回其主键。
func (s WalletUserStore) InsertRiskFreeze(tx *gorm.DB, userID uint64, triggerRule, action, reason string) (uint64, error) {
	if err := tx.Table("risk_freeze_log").Create(map[string]any{
		"user_id": userID, "trigger_rule": triggerRule, "frozen_action": action, "reason": reason,
	}).Error; err != nil {
		return 0, err
	}
	return LastInsertID(tx)
}

// LinkFreezeToRefund 把冻结记录关联到本次退款申请，后续审核与解冻都按这条链接找冻结。
func (s WalletUserStore) LinkFreezeToRefund(tx *gorm.DB, requestID string, freezeID uint64) error {
	return tx.Table("wallet_risk_freeze_link").Create(map[string]any{
		"request_id": requestID, "freeze_id": freezeID,
	}).Error
}

// FreezeWallet 把钱包置为冻结并推进乐观锁版本。
func (s WalletUserStore) FreezeWallet(tx *gorm.DB, walletID uint64) error {
	return tx.Table("wallet_account").Where("id=?", walletID).
		Updates(map[string]any{"status": "frozen", "version": gorm.Expr("version+1")}).Error
}

// SetRefundResponse 记录退款申请的对外回执。
func (s WalletUserStore) SetRefundResponse(tx *gorm.DB, requestID, responseJSON string) error {
	return tx.Table("wallet_refund_request").Where("request_id=?", requestID).
		Update("response_json", responseJSON).Error
}

// ListWalletRefundRequests 分页返回用户的余额退款申请。
func (s WalletUserStore) ListWalletRefundRequests(ctx context.Context, userID uint64, page, pageSize int) (Page[WalletRefundRequestRow], error) {
	out := Page[WalletRefundRequestRow]{Items: []WalletRefundRequestRow{}, Page: page, PageSize: pageSize}
	query := s.DB.WithContext(ctx).Table("wallet_refund_request").Where("user_id=?", userID)
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select("request_id,amount_cents,reason,created_at").
		Order("created_at DESC,request_id").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}

// WalletRefundPartRow 是退款申请拆出的每一笔原路退款。
type WalletRefundPartRow struct {
	RefundNo      string     `json:"refund_no" gorm:"column:refund_no"`
	RefundCents   int64      `json:"refund_cents" gorm:"column:refund_cents"` // 退款金额，单位分
	Status        string     `json:"status" gorm:"column:status"`
	FailureReason *string    `json:"failure_reason" gorm:"column:failure_reason"`
	CompletedAt   *time.Time `json:"completed_at" gorm:"column:completed_at"`
}

// ListWalletRefundParts 返回一笔余额退款拆出的各笔原路退款及其结果。
// 拆几笔取决于当初用了几个支付渠道，因此不能假定只有一笔。
func (s WalletUserStore) ListWalletRefundParts(ctx context.Context, db *gorm.DB, requestID string) ([]WalletRefundPartRow, error) {
	rows := []WalletRefundPartRow{}
	err := db.WithContext(ctx).Table("wallet_refund_part p").
		Select("r.refund_no,r.refund_cents,r.status,r.failure_reason,r.completed_at").
		Joins("JOIN refund_record r ON r.id=p.refund_record_id").
		Where("p.request_id=?", requestID).Order("r.id").Find(&rows).Error
	return rows, err
}

// RiskReviewForRefund 取退款的风控审核结论，取不到返回 gorm.ErrRecordNotFound。
func (s WalletUserStore) RiskReviewForRefund(ctx context.Context, db *gorm.DB, requestID string) (map[string]any, error) {
	var row map[string]any
	err := db.WithContext(ctx).Table("wallet_risk_review").Where("request_id=?", requestID).Take(&row).Error
	return row, err
}

// CountRefundFreezeLinks 数出退款申请关联的风控冻结记录。
func (s WalletUserStore) CountRefundFreezeLinks(ctx context.Context, db *gorm.DB, requestID string) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Table("wallet_risk_freeze_link").
		Where("request_id=?", requestID).Count(&count).Error
	return count, err
}
