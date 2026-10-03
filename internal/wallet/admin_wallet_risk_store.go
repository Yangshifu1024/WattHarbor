package wallet

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对钱包退款风控的读写。判断"谁能审、能不能解冻"是 operations/money
// 的事，这里只提供审核与解冻所需的数据读写。
//
// 链路：wallet_refund_request 是一次退款申请；wallet_risk_freeze_link 把它与
// 触发它的 risk_freeze_log 冻结记录关联起来；wallet_risk_review 与
// wallet_risk_release 分别是两段人工结论，各自存首次回执供幂等重放。

// WalletRiskRequest 是审核与解冻共同依赖的退款申请最小投影。
type WalletRiskRequest struct {
	RequestID   string  // 请求号，业务主键，审核、解冻、幂等都按它定位
	UserID      uint64  // 申请退款的用户 ID，冻结与退款记录都挂在该用户上
	AmountCents int64   // 申请退款金额，单位分
	Reason      *string // 申请原因，可空
}

// WalletRiskWallet 是 wallet_account 一行的最小投影，用于定位钱包并恢复其状态。
type WalletRiskWallet struct {
	ID     uint64
	UserID uint64
	Status string // 钱包状态，如 active / frozen
}

// WalletRiskFreeze 是关联到本次申请的冻结记录。
type WalletRiskFreeze struct {
	ID          uint64 // 冻结记录主键，解冻时按它更新 risk_freeze_log
	UserID      uint64 // 被冻结用户，必须与风控请求的申请人对得上
	Status      string // 冻结状态，解冻要求 frozen
	TriggerRule string // 触发规则，只有 wallet_refund_frequency 走人工审核
}

// WalletRiskPriorReview 是一段已存在的审核或解冻结论。ResponseJSON 是首次返回体，
// 幂等重放时原样回给调用方。
type WalletRiskPriorReview struct {
	ActorID      uint64 // 首次操作人 ID
	Approved     bool   // 审核结论，解冻结论无此字段
	Comment      string // 依据
	ResponseJSON string // 首次返回体
	Exists       bool   // 区分"没有记录"与"读到了但出错"
}

// WalletRiskStore 是风控审核与解冻的读写入口。
type WalletRiskStore struct{ DB *gorm.DB }

// walletRiskSelect 是列表接口要带的全部列，含审核与解冻的结论快照。
const walletRiskSelect = "r.request_id, r.user_id, r.amount_cents, r.reason, r.created_at, " +
	"v.response_json AS review_json, v.created_at AS review_created_at, " +
	"x.response_json AS release_json, x.created_at AS release_created_at, " +
	"f.status AS freeze_status, f.trigger_rule, l.freeze_id"

// ListWalletRisks 分页返回关联了冻结记录的退款申请。reviewed 只取已有审核结论的，
// 其余取尚未审核的。返回通用行集合，字段的可见性与可操作标记由处理器按权限组装。
func (s WalletRiskStore) ListWalletRisks(ctx context.Context, status string, page, pageSize int) ([]map[string]any, int64, error) {
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

// LockRefundRequest 在行锁下取退款申请，审核与解冻都从这里起步，
// 避免并发的两段人工结论交叉写入。
func (s WalletRiskStore) LockRefundRequest(tx *gorm.DB, requestID string) (WalletRiskRequest, error) {
	var row WalletRiskRequest
	err := tx.Table("wallet_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("request_id=?", requestID).Take(&row).Error
	return row, err
}

// FindRiskReview 取已有的审核结论。取不到时 Exists 为 false，调用方据此区分
// "首次审核"与"读取出错"。
func (s WalletRiskStore) FindRiskReview(tx *gorm.DB, requestID string) (WalletRiskPriorReview, error) {
	var row struct {
		ActorID      uint64
		Approved     bool
		Comment      string
		ResponseJSON string
	}
	err := tx.Table("wallet_risk_review").Where("request_id=?", requestID).Take(&row).Error
	if err != nil {
		return WalletRiskPriorReview{}, err
	}
	return WalletRiskPriorReview{ActorID: row.ActorID, Approved: row.Approved, Comment: row.Comment, ResponseJSON: row.ResponseJSON, Exists: true}, nil
}

// CountFrequencyFreezes 数出这次申请关联的、属于该用户且由退款频次规则触发的冻结记录。
// 人工审核只认可恰好一条：零条说明没有待审的冻结，多条说明关联关系有歧义。
func (s WalletRiskStore) CountFrequencyFreezes(tx *gorm.DB, requestID string, userID uint64) (int64, error) {
	var count int64
	err := tx.Table("wallet_risk_freeze_link l").
		Joins("JOIN risk_freeze_log f ON f.id=l.freeze_id").
		Where("l.request_id=? AND f.user_id=? AND f.trigger_rule='wallet_refund_frequency'", requestID, userID).
		Count(&count).Error
	return count, err
}

// InsertRiskReview 落一条审核结论与首次回执。
func (s WalletRiskStore) InsertRiskReview(tx *gorm.DB, requestID string, actorID uint64, approved bool, comment, responseJSON string) error {
	return tx.Table("wallet_risk_review").Create(map[string]any{
		"request_id": requestID, "actor_id": actorID, "approved": approved,
		"comment": comment, "response_json": responseJSON,
	}).Error
}

// CountRiskReviews 数出这次申请的审核记录数，解冻前要求恰好一次。
func (s WalletRiskStore) CountRiskReviews(tx *gorm.DB, requestID string) (int64, error) {
	var count int64
	err := tx.Table("wallet_risk_review").Where("request_id=?", requestID).Count(&count).Error
	return count, err
}

// FindRiskRelease 取已有的解冻结论，取不到时 Exists 为 false。
func (s WalletRiskStore) FindRiskRelease(tx *gorm.DB, requestID string) (WalletRiskPriorReview, error) {
	var row struct {
		ActorID      uint64
		Comment      string
		ResponseJSON string
	}
	err := tx.Table("wallet_risk_release").Where("request_id=?", requestID).Take(&row).Error
	if err != nil {
		return WalletRiskPriorReview{}, err
	}
	return WalletRiskPriorReview{ActorID: row.ActorID, Comment: row.Comment, ResponseJSON: row.ResponseJSON, Exists: true}, nil
}

// LockWallet 在行锁下取钱包，用于核对归属并恢复状态。
func (s WalletRiskStore) LockWallet(tx *gorm.DB, userID uint64) (WalletRiskWallet, error) {
	var row WalletRiskWallet
	err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row, err
}

// LockFreeze 在行锁下取这次申请关联的冻结记录，并核对归属与触发规则。
func (s WalletRiskStore) LockFreeze(tx *gorm.DB, requestID string) (WalletRiskFreeze, error) {
	var row WalletRiskFreeze
	err := tx.Table("risk_freeze_log f").Select("f.id, f.user_id, f.status, f.trigger_rule").
		Joins("JOIN wallet_risk_freeze_link l ON l.freeze_id=f.id").
		Where("l.request_id=?", requestID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
	return row, err
}

// Unfreeze 把一条冻结记录置为已解冻并记下解冻时刻。
func (s WalletRiskStore) Unfreeze(tx *gorm.DB, freezeID uint64, at time.Time) error {
	return tx.Table("risk_freeze_log").Where("id=?", freezeID).
		Updates(map[string]any{"status": "unfrozen", "unfreeze_at": at}).Error
}

// CountActiveFreezes 数出该用户仍然生效的冻结数，为零才允许恢复钱包为可用。
func (s WalletRiskStore) CountActiveFreezes(tx *gorm.DB, userID uint64) (int64, error) {
	var count int64
	err := tx.Table("risk_freeze_log").Where("user_id=? AND status='frozen'", userID).Count(&count).Error
	return count, err
}

// UserStatus 取用户状态，用户非 active 时钱包不恢复。
func (s WalletRiskStore) UserStatus(tx *gorm.DB, userID uint64) (string, error) {
	var row struct{ Status string }
	err := tx.Table("user").Where("id=? AND deleted_at IS NULL", userID).Take(&row).Error
	return row.Status, err
}

// ActivateWallet 把钱包恢复为可用并推进乐观锁版本。
func (s WalletRiskStore) ActivateWallet(tx *gorm.DB, walletID uint64) error {
	return tx.Table("wallet_account").Where("id=?", walletID).
		Updates(map[string]any{"status": "active", "version": gorm.Expr("version+1")}).Error
}

// InsertRiskRelease 落一条解冻结论与首次回执。
func (s WalletRiskStore) InsertRiskRelease(tx *gorm.DB, requestID string, actorID uint64, comment, responseJSON string) error {
	return tx.Table("wallet_risk_release").Create(map[string]any{
		"request_id": requestID, "actor_id": actorID, "comment": comment, "response_json": responseJSON,
	}).Error
}
