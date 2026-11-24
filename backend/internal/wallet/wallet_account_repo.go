package wallet

import (
	"context"
	"time"
	"wattharbor/internal/domain/money"
	walletdomain "wattharbor/internal/domain/wallet"
	"wattharbor/internal/persist"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 钱包账户金额是已保存的数据，流水仅作为审计凭据。可用额由余额减冻结额计算，下限为零。

// WalletBalance 是钱包余额的可读查询结果。
type WalletBalance struct {
	BalanceCents int64                      `gorm:"column:balance_cents"` // 当前账面余额，分
	FrozenCents  int64                      `gorm:"column:frozen_cents"`  // 当前预占金额，分
	Status       walletdomain.AccountStatus `gorm:"column:status"`        // wallet_account.status：active 可用或 frozen 冻结
}

// AvailableCents 返回余额减冻结额后的可用金额，单位分，下限为零。
func (b WalletBalance) AvailableCents() int64 {
	if b.BalanceCents <= b.FrozenCents {
		return 0
	}
	return b.BalanceCents - b.FrozenCents
}

// UserWalletRepo 是 C 端钱包的读写入口。
type UserWalletRepo struct {
	DB *gorm.DB // central 钱包连接，由进程管理
}

// FindWalletBalance 查询未删除的钱包；未开户返回 gorm.ErrRecordNotFound，不自动创建。
func (s UserWalletRepo) FindWalletBalance(ctx context.Context, userID uint64) (WalletBalance, error) {
	var row WalletBalance
	err := s.DB.WithContext(ctx).Table("wallet_account").
		Where("user_id = ? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// ListWalletTxns 按用户分页读取流水；direction 为空值时查询所有方向，in、out 分别只查收入、支出。
// direction 的词表见 walletdomain.TxnDirection；空值表示不筛选，不是该列的取值。
func (s UserWalletRepo) ListWalletTxns(ctx context.Context, userID uint64, direction walletdomain.TxnDirection, page, pageSize int) (persist.Page[map[string]any], error) {
	out := persist.Page[map[string]any]{Items: []map[string]any{}, Page: page, PageSize: pageSize}
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
// 状态与有效期取自 payment_order：充值申请本身只记"发起过"。
// 能不能付要看支付单有没有过期。
type WalletRechargeRow struct {
	RequestID   string              `json:"request_id" gorm:"column:request_id"`     // 申请 UUID，同内容重放沿用
	AmountCents int64               `json:"amount_cents" gorm:"column:amount_cents"` // 申请金额，分
	Status      money.PaymentStatus `json:"status" gorm:"column:status"`             // 支付 initiated 待付、paid 已到账或退款状态
	ExpiredAt   *time.Time          `json:"expires_at" gorm:"column:expired_at"`     // 支付截止时间
	CreatedAt   time.Time           `json:"created_at" gorm:"column:created_at"`     // 申请创建 UTC 时刻
	CanPay      bool                `json:"can_pay" gorm:"-"`                        // 未支付且未过期才允许继续付款
}

// ListWalletRecharges 按用户分页读取充值申请及支付状态，按创建顺序倒序。
// 可否继续付款由 QueryService 按支付有效期判断。
func (s UserWalletRepo) ListWalletRecharges(ctx context.Context, userID uint64, page, pageSize int) (persist.Page[WalletRechargeRow], error) {
	out := persist.Page[WalletRechargeRow]{Items: []WalletRechargeRow{}, Page: page, PageSize: pageSize}
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
	RequestID   string    `gorm:"column:request_id"`   // 申请 UUID，同内容重放沿用
	AmountCents int64     `gorm:"column:amount_cents"` // 申请金额，分
	Reason      *string   `gorm:"column:reason"`       // 申请依据，可空
	CreatedAt   time.Time `gorm:"column:created_at"`   // 申请创建 UTC 时刻
}

// LockWalletAccount 在调用方事务中锁 userID 未删除的钱包，返回当前行；缺账户为 gorm.ErrRecordNotFound，其他数据库错误要求回滚，调用方负责提交，不自行开户。
func (s UserWalletRepo) LockWalletAccount(tx *gorm.DB, userID uint64) (WalletRow, error) {
	var row WalletRow
	err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// WalletRow 是钱包账户的完整行，充值与退款都要读余额与冻结额。
type WalletRow struct {
	ID           uint64                     `gorm:"column:id"`            // 钱包主键
	UserID       uint64                     `gorm:"column:user_id"`       // 账户或申请所属用户
	BalanceCents int64                      `gorm:"column:balance_cents"` // 当前账面余额，分
	FrozenCents  int64                      `gorm:"column:frozen_cents"`  // 当前预占金额，分
	Status       walletdomain.AccountStatus `gorm:"column:status"`        // wallet_account.status：active 可用或 frozen 冻结
	Version      uint64                     `gorm:"column:version"`       // 钱包版本号，每次资金或状态变化时增加
}

// OpenWalletAccount 在调用方事务中为 userID 开户，初始余额、冻结额为零且状态 active；唯一键冲突时失败，不自行提交。
func (s UserWalletRepo) OpenWalletAccount(tx *gorm.DB, userID uint64) error {
	return tx.Table("wallet_account").Create(map[string]any{
		"user_id": userID, "balance_cents": 0, "frozen_cents": 0, "status": walletdomain.AccountActive,
	}).Error
}

// FindUserOpenID 在调用方事务中读取 userID 未删除身份的 openid；缺用户或空身份返回空字符串，由退款用例拒绝，无用户写入。
func (s UserWalletRepo) FindUserOpenID(tx *gorm.DB, userID uint64) (string, error) {
	var openid string
	err := tx.Table("user").Where("id=? AND deleted_at IS NULL", userID).Pluck("openid", &openid).Error
	return openid, err
}

// LockUser 在调用方事务中锁 userID 未删除的用户，返回记录；缺行返回 gorm.ErrRecordNotFound，不检查 active 状态，不自行提交。
func (s UserWalletRepo) LockUser(tx *gorm.DB, userID uint64) (map[string]any, error) {
	var row map[string]any
	err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// WalletRechargeRequest 是充值申请存档，request_json 供幂等重放比对内容。
type WalletRechargeRequest struct {
	RequestID      string // 申请 UUID，同内容重放沿用
	UserID         uint64 // 账户或申请所属用户
	AmountCents    int64  // 申请金额，分
	PaymentOrderID uint64 // 充值支付主键
	RequestJSON    string // 首次申请内容 JSON，参与重放核对
}

// FindRechargeRequest 在调用方事务中按 requestID 读取申请，返回内容及读取错误；缺行返回 gorm.ErrRecordNotFound，归属和金额重放核对由 RechargeService 负责。
func (s UserWalletRepo) FindRechargeRequest(tx *gorm.DB, requestID string) (WalletRechargeRequest, error) {
	var row WalletRechargeRequest
	err := tx.Table("wallet_recharge_request").Where("request_id=?", requestID).Take(&row).Error
	return row, err
}

// InsertRechargeRequest 在调用方事务中保存 in 的用户、金额、支付编号和请求内容；唯一键冲突时失败，与支付创建共同提交，失败全部回滚。
func (s UserWalletRepo) InsertRechargeRequest(tx *gorm.DB, in WalletRechargeRequest) error {
	return tx.Table("wallet_recharge_request").Create(map[string]any{
		"request_id": in.RequestID, "user_id": in.UserID, "amount_cents": in.AmountCents,
		"payment_order_id": in.PaymentOrderID, "request_json": in.RequestJSON,
	}).Error
}

// WalletRefundRequest 保存余额退款申请，重放须逐字段比对请求内容。
type WalletRefundRequest struct {
	RequestID    string  `gorm:"column:request_id"`    // 申请 UUID，同内容重放沿用
	UserID       uint64  `gorm:"column:user_id"`       // 账户或申请所属用户
	AmountCents  int64   `gorm:"column:amount_cents"`  // 申请金额，分
	Reason       *string `gorm:"column:reason"`        // 申请依据，可空
	ResponseJSON string  `gorm:"column:response_json"` // 首次返回体，重放时原样回给调用方
}

// FindRefundRequest 在调用方事务中按 requestID 读取首次退款申请及回执，返回记录；缺行返回 gorm.ErrRecordNotFound，调用方核对归属、金额及原因。
func (s UserWalletRepo) FindRefundRequest(tx *gorm.DB, requestID string) (WalletRefundRequest, error) {
	var row WalletRefundRequest
	err := tx.Table("wallet_refund_request").Where("request_id=?", requestID).Take(&row).Error
	return row, err
}

// InsertRefundRequest 在调用方事务中登记 requestID、userID、amountCents 分及可空 reason；唯一键冲突时失败，申请、资金预占和最终回执由用例共同提交。
func (s UserWalletRepo) InsertRefundRequest(tx *gorm.DB, requestID string, userID uint64, amountCents int64, reason *string) error {
	return tx.Table("wallet_refund_request").Create(map[string]any{
		"request_id": requestID, "user_id": userID, "amount_cents": amountCents, "reason": reason,
	}).Error
}

// CountRecentRefundRequests 在调用方事务中返回 userID 自 since UTC 时刻起的申请数及读取错误；包含当前事务新申请，频次用例须在拿钱包锁后首次建立快照，不写状态。
func (s UserWalletRepo) CountRecentRefundRequests(tx *gorm.DB, userID uint64, since time.Time) (int64, error) {
	var count int64
	err := tx.Table("wallet_refund_request").
		Where("user_id=? AND created_at>=?", userID, since).Count(&count).Error
	return count, err
}

// InsertRiskFreeze 保存用户、触发规则、处理动作与原因，返回冻结记录 ID；与申请同事务提交。
func (s UserWalletRepo) InsertRiskFreeze(tx *gorm.DB, userID uint64, triggerRule, action, reason string) (uint64, error) {
	if err := tx.Table("risk_freeze_log").Create(map[string]any{
		"user_id": userID, "trigger_rule": triggerRule, "frozen_action": action, "reason": reason,
	}).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// LinkFreezeToRefund 在调用方事务中将 requestID 关联 freezeID；唯一键冲突时失败，链接与申请、冻结共同提交，不修改退款执行状态。
func (s UserWalletRepo) LinkFreezeToRefund(tx *gorm.DB, requestID string, freezeID uint64) error {
	return tx.Table("wallet_risk_freeze_link").Create(map[string]any{
		"request_id": requestID, "freeze_id": freezeID,
	}).Error
}

// FreezeWallet 在调用方事务中将 walletID 钱包置为 frozen 并增加版本号；返回更新错误，不改变余额或已冻结金额，不自行提交。
func (s UserWalletRepo) FreezeWallet(tx *gorm.DB, walletID uint64) error {
	return tx.Table("wallet_account").Where("id=?", walletID).
		Updates(map[string]any{"status": walletdomain.AccountFrozen, "version": gorm.Expr("version+1")}).Error
}

// SetRefundResponse 在调用方事务中为 requestID 保存 responseJSON 首次回执；返回更新错误，与申请、预占及风险共同提交，重放读取回执。
func (s UserWalletRepo) SetRefundResponse(tx *gorm.DB, requestID, responseJSON string) error {
	return tx.Table("wallet_refund_request").Where("request_id=?", requestID).
		Update("response_json", responseJSON).Error
}

// ListWalletRefundRequests 只读取当前用户的退款申请，按创建时间与请求号稳定倒序分页。
func (s UserWalletRepo) ListWalletRefundRequests(ctx context.Context, userID uint64, page, pageSize int) (persist.Page[WalletRefundRequestRow], error) {
	out := persist.Page[WalletRefundRequestRow]{Items: []WalletRefundRequestRow{}, Page: page, PageSize: pageSize}
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
	RefundNo      string             `json:"refund_no" gorm:"column:refund_no"`           // 分笔退款单号，重试沿用
	RefundCents   int64              `json:"refund_cents" gorm:"column:refund_cents"`     // 退款金额，单位分
	Status        money.RefundStatus `json:"status" gorm:"column:status"`                 // 取自 refund_record.status：pending 待退 / processing 处理中 / success 已退 / failed 失败 / rejected 否决
	FailureReason *string            `json:"failure_reason" gorm:"column:failure_reason"` // 渠道终态失败或需复核原因，可空
	CompletedAt   *time.Time         `json:"completed_at" gorm:"column:completed_at"`     // 渠道完成 UTC 时刻，未完成为空
}
