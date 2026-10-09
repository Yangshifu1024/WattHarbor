package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	charging "chargepilot/internal/domain/charging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MeterReview 保存修正前后的价格和计量；第二次审核须确认原数据未变且只修正分段。
type MeterReview struct {
	ID               uint64          `json:"id"`                 // 核实记录主键
	RequestID        string          `json:"request_id"`         // 提案 UUID，同号同内容重放
	ChargeOrderID    uint64          `json:"charge_order_id"`    // 所属同一订单，不接受跨订单核实 ID
	OriginalJSON     json.RawMessage `json:"original"`           // 原价格和设备计量 JSON，第二次审核时核对未变
	CorrectedJSON    json.RawMessage `json:"corrected"`          // 只替换分段后的价格和计量 JSON
	Reason           string          `json:"reason"`             // 提案依据，1–500 字
	Status           string          `json:"status"`             // awaiting_second、approved 或 rejected
	FirstReviewerID  uint64          `json:"first_reviewer_id"`  // 首次提交的财务账号 ID
	SecondReviewerID *uint64         `json:"second_reviewer_id"` // 第二次审核的财务账号 ID；未决定为空，须与首次提交人不同
	RejectReason     *string         `json:"reject_reason"`      // 驳回依据，通过或未决定为空
	CreatedAt        time.Time       `json:"created_at"`         // 提案创建 UTC 时刻
	ReviewedAt       *time.Time      `json:"reviewed_at"`        // 第二次审核的 UTC 时刻，未决定为空
}

// TableName 返回计量核实记录所属表，不执行查询或修改。
func (MeterReview) TableName() string { return "charge_meter_review" }

// ProposeMeter 锁定计费任务后保存设备计量和修正提案，相同请求复用首次结果。
// 请求内容不一致、任务非待核实、已有待审核或已批准提案、缺截止读数时拒绝；外层服务授权并审计。
func (s BillingOrders) ProposeMeter(ctx context.Context, id, actor uint64, request, reason string, segments []charging.MeterSegment) (MeterReview, error) {
	var out MeterReview
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct {
			Status string // 计费任务状态，仅 manual_review 可新建核实
		}
		if err := tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&job).Error; err != nil {
			return err
		}
		var prior MeterReview
		found := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", request).Find(&prior)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected > 0 {
			var source Source
			if json.Unmarshal(prior.CorrectedJSON, &source) != nil || prior.ChargeOrderID != id || prior.FirstReviewerID != actor || prior.Reason != reason || !charging.SameSegments(source.Meter.Segments, segments) {
				return ErrBillingConflict
			}
			out = prior
			return nil
		}
		if job.Status != "manual_review" {
			return ErrBillingConflict
		}
		var active []MeterReview
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=? AND status IN ('awaiting_second','approved')", id).Find(&active).Error; err != nil {
			return err
		}
		if len(active) > 0 {
			return ErrBillingConflict
		}
		source, err := readOriginalBillingSource(tx, id)
		if err != nil {
			return err
		}
		original, _ := json.Marshal(source)
		source.Meter.Segments = segments
		// 缺少截止时刻的读数时，补充结束分段也无法算出截止用量；须另行登记人工最终金额。
		if source.Meter.ReviewRequired {
			return charging.ErrMeterReview
		}
		if _, err := charging.PriceActual(source.Rule, source.Meter); err != nil {
			return err
		}
		corrected, _ := json.Marshal(source)
		out = MeterReview{RequestID: request, ChargeOrderID: id, OriginalJSON: original, CorrectedJSON: corrected, Reason: reason, Status: "awaiting_second", FirstReviewerID: actor}
		return tx.Create(&out).Error
	})
	return out, err
}

// ReviewMeter 锁定任务后保存第二次计量审核结果，相同决定可重试。
// 订单、原价格和计量、修正内容或审核人不符时返回 ErrBillingConflict；批准后重新排队计费。
// 外层服务通过 LookupReviewer 查询首位审核人的当前权限；审核记录和审计在业务事务内保存。
func (s BillingOrders) ReviewMeter(ctx context.Context, id, reviewID, actor uint64, approve bool, reason string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct {
			Status string // 计费任务状态，通过核实后才重新排队
		}
		if err := tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&job).Error; err != nil {
			return err
		}
		var review MeterReview
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND charge_order_id=?", reviewID, id).Take(&review).Error; err != nil {
			return err
		}
		target := "rejected"
		if approve {
			target = "approved"
		}
		if review.Status == target && review.SecondReviewerID != nil && *review.SecondReviewerID == actor {
			if !approve && (review.RejectReason == nil || *review.RejectReason != reason) {
				return ErrBillingConflict
			}
			return nil
		}
		if job.Status != "manual_review" || review.Status != "awaiting_second" || review.FirstReviewerID == actor {
			return ErrBillingConflict
		}
		if approve {
			current, err := readOriginalBillingSource(tx, id)
			if err != nil {
				return err
			}
			var original, corrected Source
			if json.Unmarshal(review.OriginalJSON, &original) != nil || json.Unmarshal(review.CorrectedJSON, &corrected) != nil {
				return ErrBillingConflict
			}
			a, _ := json.Marshal(current)
			b, _ := json.Marshal(original)
			if string(a) != string(b) {
				return ErrBillingConflict
			}
			expected := current
			expected.Meter.Segments = corrected.Meter.Segments
			a, _ = json.Marshal(expected)
			b, _ = json.Marshal(corrected)
			if string(a) != string(b) {
				return ErrBillingConflict
			}
			if _, err := charging.PriceActual(corrected.Rule, corrected.Meter); err != nil {
				return err
			}
		}
		changes := map[string]any{"status": target, "second_reviewer_id": actor, "reviewed_at": time.Now().UTC()}
		if !approve {
			changes["reject_reason"] = reason
		}
		if err := tx.Model(&MeterReview{}).Where("id=?", reviewID).Updates(changes).Error; err != nil {
			return err
		}
		if approve {
			return tx.Table("charge_billing_job").Where("charge_order_id=?", id).Updates(map[string]any{"status": "pending", "generation": gorm.Expr("generation+1"), "next_attempt_at": time.Now().UTC(), "last_error": nil}).Error
		}
		return nil
	})
}

// LockMeterReviewJob 锁定计费任务，使计量核实、人工金额和审计按顺序处理；缺任务返回错误。
func LockMeterReviewJob(tx *gorm.DB, id uint64) error {
	var job struct {
		Status string /* 计费任务状态：pending、manual_review 或 done */
	}
	return tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&job).Error
}

// MeterProposalExists 在任务锁内用当前读检查提案，避免旧 RR 快照遗漏已提交记录。
func MeterProposalExists(tx *gorm.DB, requestID string) (bool, error) {
	var row MeterReview
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", requestID).Take(&row).Error
	return err == nil, ignoreMissingReview(err)
}

// ManualAmountExists 在任务锁内加锁查询人工最终金额，避免旧 RR 快照遗漏已提交记录。
func ManualAmountExists(tx *gorm.DB, id uint64, requestID string) (bool, error) {
	var row ManualSettlement
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=? OR request_id=?", id, requestID).Take(&row).Error
	return err == nil, ignoreMissingReview(err)
}

// ignoreMissingReview 仅将记录缺失转为 nil；连接和取消错误不能当作首次申请。
func ignoreMissingReview(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}
