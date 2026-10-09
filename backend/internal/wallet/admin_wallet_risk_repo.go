package wallet

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 风控 repo 提供申请、冻结、审核及解冻的已保存的数据；角色、权限及状态决策由 RiskService 负责。

// WalletRiskRequest 是审核与解冻共同依赖的退款申请最小查询结果。
type WalletRiskRequest struct {
	RequestID   string  // 请求号，业务主键，审核、解冻、幂等都按它定位
	UserID      uint64  // 申请退款的用户 ID，冻结与退款记录都挂在该用户上
	AmountCents int64   // 申请退款金额，单位分
	Reason      *string // 申请原因，可空
}

// WalletRiskWallet 是 wallet_account 一行的最小查询结果，用于定位钱包并恢复其状态。
type WalletRiskWallet struct {
	ID     uint64 // 钱包主键
	UserID uint64 // 钱包所属用户
	Status string // 钱包状态，如 active / frozen
}

// WalletRiskFreeze 是关联到本次申请的冻结记录。
type WalletRiskFreeze struct {
	ID          uint64 // 冻结记录主键，解冻时按它更新 risk_freeze_log
	UserID      uint64 // 被冻结用户，必须与风控请求的申请人对得上
	Status      string // 冻结状态，解冻要求 frozen
	TriggerRule string // 触发规则，只有 wallet_refund_frequency 走人工审核
}

// WalletRiskPriorReview 是一段已存在的审核或解冻结论。ResponseJSON 是首次返回体。
// 幂等重放时原样回给调用方。
type WalletRiskPriorReview struct {
	ActorID      uint64 // 首次操作人 ID
	Approved     bool   // 审核结论，解冻结论无此字段
	Comment      string // 依据
	ResponseJSON string // 首次返回体
	Exists       bool   // 区分"没有记录"与"读到了但出错"
}

// RiskRepo 是风控审核与解冻的读写入口。
type RiskRepo struct {
	DB *gorm.DB // central 风控连接，由进程管理
}

// walletRiskSelect 是列表接口要带的全部列，含审核与解冻的结论快照。
const walletRiskSelect = "r.request_id, r.user_id, r.amount_cents, r.reason, r.created_at, " +
	"v.response_json AS review_json, v.created_at AS review_created_at, " +
	"x.response_json AS release_json, x.created_at AS release_created_at, " +
	"f.status AS freeze_status, f.trigger_rule, l.freeze_id"

// ListWalletRisks 分页读取关联冻结的退款申请；reviewed 查已审核，其余查未审核。
// 审核、解冻按钮是否可用由用例按权限与当前状态判断。
func (s RiskRepo) ListWalletRisks(ctx context.Context, status string, page, pageSize int) ([]map[string]any, int64, error) {
	query := s.DB.WithContext(ctx).Table("wallet_refund_request r").
		Joins("LEFT JOIN wallet_risk_review v ON v.request_id=r.request_id").
		Joins("LEFT JOIN wallet_risk_freeze_link l ON l.request_id=r.request_id").
		Joins("LEFT JOIN risk_freeze_log f ON f.id=l.freeze_id").
		Joins("LEFT JOIN wallet_risk_release x ON x.request_id=r.request_id").
		Where("l.freeze_id IS NOT NULL")
	if status == "reviewed" {
		query = query.Where("v.request_id IS NOT NULL")
	} else {
		query = query.Where("v.request_id IS NULL")
	}
	total := int64(0)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []map[string]any{}
	if err := query.Select(walletRiskSelect).Order("r.created_at DESC, r.request_id").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// LockRefundRequest 按 requestID 锁定退款申请并返回用户、金额；缺行返回 gorm.ErrRecordNotFound。
// 审核、预占和审计须在 tx 内一并提交。
func (s RiskRepo) LockRefundRequest(tx *gorm.DB, requestID string) (WalletRiskRequest, error) {
	var row WalletRiskRequest
	err := tx.Table("wallet_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("request_id=?", requestID).Take(&row).Error
	return row, err
}

// FindRiskReview 读取 requestID 的审核记录；有记录时 Exists=true，缺行返回 gorm.ErrRecordNotFound。
// 其他读取错误不能当作尚未审核。
func (s RiskRepo) FindRiskReview(tx *gorm.DB, requestID string) (WalletRiskPriorReview, error) {
	var row struct {
		ActorID      uint64 // 审核或解冻账号 ID
		Approved     bool   // 审核是否通过，解冻记录无此值
		Comment      string // 动作依据，重放须相同
		ResponseJSON string // 首次回执，重放保持内容
	}
	err := tx.Table("wallet_risk_review").Where("request_id=?", requestID).Take(&row).Error
	if err != nil {
		return WalletRiskPriorReview{}, err
	}
	return WalletRiskPriorReview{ActorID: row.ActorID, Approved: row.Approved, Comment: row.Comment, ResponseJSON: row.ResponseJSON, Exists: true}, nil
}

// CountFrequencyFreezes 在调用方事务中返回 requestID 关联且属于 userID 的 wallet_refund_frequency 冻结数及读取错误；用例要求恰好一条，不在此做批准或解冻。
func (s RiskRepo) CountFrequencyFreezes(tx *gorm.DB, requestID string, userID uint64) (int64, error) {
	var count int64
	err := tx.Table("wallet_risk_freeze_link l").
		Joins("JOIN risk_freeze_log f ON f.id=l.freeze_id").
		Where("l.request_id=? AND f.user_id=? AND f.trigger_rule='wallet_refund_frequency'", requestID, userID).
		Count(&count).Error
	return count, err
}

// InsertRiskReview 保存首次审核人、结论、依据与回执；与资金预占及审计同事务提交。
func (s RiskRepo) InsertRiskReview(tx *gorm.DB, requestID string, actorID uint64, approved bool, comment, responseJSON string) error {
	return tx.Table("wallet_risk_review").Create(map[string]any{
		"request_id": requestID, "actor_id": actorID, "approved": approved,
		"comment": comment, "response_json": responseJSON,
	}).Error
}

// CountRiskReviews 统计申请的审核记录；解冻用例要求恰好一条。
func (s RiskRepo) CountRiskReviews(tx *gorm.DB, requestID string) (int64, error) {
	var count int64
	err := tx.Table("wallet_risk_review").Where("request_id=?", requestID).Count(&count).Error
	return count, err
}

// FindRiskRelease 读取 requestID 的首次解冻记录；有记录时 Exists=true，缺行返回 gorm.ErrRecordNotFound。
// 其他读取错误不能触发再次解冻。
func (s RiskRepo) FindRiskRelease(tx *gorm.DB, requestID string) (WalletRiskPriorReview, error) {
	var row struct {
		ActorID      uint64 // 审核或解冻账号 ID
		Comment      string // 动作依据，重放须相同
		ResponseJSON string // 首次回执，重放保持内容
	}
	err := tx.Table("wallet_risk_release").Where("request_id=?", requestID).Take(&row).Error
	if err != nil {
		return WalletRiskPriorReview{}, err
	}
	return WalletRiskPriorReview{ActorID: row.ActorID, Comment: row.Comment, ResponseJSON: row.ResponseJSON, Exists: true}, nil
}

// LockWallet 锁定未删除钱包并读取用户与状态；没有账户时返回 gorm.ErrRecordNotFound。
// 可否解冻由用例判断。
func (s RiskRepo) LockWallet(tx *gorm.DB, userID uint64) (WalletRiskWallet, error) {
	var row WalletRiskWallet
	err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// LockFreeze 锁定申请关联的冻结记录，读取用户、状态与触发规则，供用例核对归属与冻结条件。
func (s RiskRepo) LockFreeze(tx *gorm.DB, requestID string) (WalletRiskFreeze, error) {
	var row WalletRiskFreeze
	err := tx.Table("risk_freeze_log f").Select("f.id, f.user_id, f.status, f.trigger_rule").
		Joins("JOIN wallet_risk_freeze_link l ON l.freeze_id=f.id").
		Where("l.request_id=?", requestID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
	return row, err
}

// Unfreeze 在调用方事务中将 freezeID 置为 unfrozen 并记 at UTC 解冻时刻；返回更新错误，与钱包状态、首次回执及审计一并提交，不释放渠道退款预占。
func (s RiskRepo) Unfreeze(tx *gorm.DB, freezeID uint64, at time.Time) error {
	return tx.Table("risk_freeze_log").Where("id=?", freezeID).
		Updates(map[string]any{"status": "unfrozen", "unfreeze_at": at}).Error
}

// CountActiveFreezes 在已锁钱包的事务中当前读 userID 仍为 frozen 的记录数及读取错误。
// 零值才允许继续恢复钱包资格核对；逐行锁定读取避免旧 RR 快照遗漏新冻结。
func (s RiskRepo) CountActiveFreezes(tx *gorm.DB, userID uint64) (int64, error) {
	var rows []struct {
		ID uint64 // 当前仍生效的冻结记录 ID
	}
	err := tx.Table("risk_freeze_log").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND status='frozen'", userID).Find(&rows).Error
	return int64(len(rows)), err
}

// UserStatus 读取未删除用户的状态；用户未启用时不能恢复钱包使用。
func (s RiskRepo) UserStatus(tx *gorm.DB, userID uint64) (string, error) {
	var row struct {
		Status string // 用户当前状态，只有 active 才可恢复钱包
	}
	err := tx.Table("user").Where("id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row.Status, err
}

// ActivateWallet 在调用方事务中将 walletID 置为 active 并增加一次版本号；调用方已核对用户及剩余冻结，不改变资金金额（分）。
func (s RiskRepo) ActivateWallet(tx *gorm.DB, walletID uint64) error {
	return tx.Table("wallet_account").Where("id=?", walletID).
		Updates(map[string]any{"status": "active", "version": gorm.Expr("version+1")}).Error
}

// InsertRiskRelease 保存首次解冻人、依据与回执，与冻结、钱包状态及审计同事务提交。
func (s RiskRepo) InsertRiskRelease(tx *gorm.DB, requestID string, actorID uint64, comment, responseJSON string) error {
	return tx.Table("wallet_risk_release").Create(map[string]any{
		"request_id": requestID, "actor_id": actorID, "comment": comment, "response_json": responseJSON,
	}).Error
}
