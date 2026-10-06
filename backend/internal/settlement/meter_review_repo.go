package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MeterReview 冻结人工计量核实前后的证据，二签只接受原订单及未变化的证据。
type MeterReview struct {
	ID               uint64          `json:"id"`                 // 核实记录主键
	RequestID        string          `json:"request_id"`         // 提案 UUID，同号同内容重放
	ChargeOrderID    uint64          `json:"charge_order_id"`    // 所属原订单，不接受跨订单核实 ID
	OriginalJSON     json.RawMessage `json:"original"`           // 原计量及价格证据 JSON，二签时复核未变化
	CorrectedJSON    json.RawMessage `json:"corrected"`          // 仅替换计量分段的修正证据 JSON
	Reason           string          `json:"reason"`             // 提案依据，1–500 字
	Status           string          `json:"status"`             // awaiting_second、approved 或 rejected
	FirstReviewerID  uint64          `json:"first_reviewer_id"`  // 首签财务账号 ID
	SecondReviewerID *uint64         `json:"second_reviewer_id"` // 二签财务账号 ID；未决定为空，必须不同于首签
	RejectReason     *string         `json:"reject_reason"`      // 驳回依据，通过或未决定为空
	CreatedAt        time.Time       `json:"created_at"`         // 提案创建 UTC 时刻
	ReviewedAt       *time.Time      `json:"reviewed_at"`        // 二签决定 UTC 时刻，未决定为空
}

// TableName 返回计量核实记录所属表，不执行查询或修改。
func (MeterReview) TableName() string { return "charge_meter_review" }

// ProposeMeter 在 ctx 下为订单 id 登记 actor 的 request UUID、reason 依据及 segments 计量修正。
// 返回原提案或首次提案；异内容、非人工状态、已有有效提案或缺截止证据返回业务错误，存储失败回滚。
// 锁原计费任务后保存完整原始及修正证据；不直接入账，实时权限及同事务审计由外层服务负责。
func (s BillingOrders) ProposeMeter(ctx context.Context, id, actor uint64, request, reason string, segments []charging.MeterSegment) (MeterReview, error) {
	var out MeterReview
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct {
			Status string // 原计费任务状态，仅 manual_review 可新建核实
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
		// 缺失计费截止点证据只能通过有审计的最终金额裁决解决；
		// 补充物理结束分段不能证明较早的计费截止点。
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

// ReviewMeter 在 ctx 下对订单 id 的 reviewID 由 actor 执行 approve 或 reason 驳回。
// 相同二签决定允许重放，状态、原证据、修正范围或审核人冲突返回 ErrBillingConflict，写入失败回滚。
// 锁原任务后推进核实及任务状态；通过仍待后续计费，首签实时权限与审计由外层服务在同事务核对。
func (s BillingOrders) ReviewMeter(ctx context.Context, id, reviewID, actor uint64, approve bool, reason string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct {
			Status string // 原计费任务状态，通过核实后才重新排队
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
			return tx.Table("charge_billing_job").Where("charge_order_id=?", id).Updates(map[string]any{"status": "pending", "next_attempt_at": time.Now().UTC(), "last_error": nil}).Error
		}
		return nil
	})
}

// LockMeterReviewJob 锁定 tx 中 id 的计费任务，串行核实、裁决及其审计；不提交事务。
// 缺任务或读取失败返回错误，调用方不得继续核实。
func LockMeterReviewJob(tx *gorm.DB, id uint64) error {
	var job struct {
		Status string /* 原任务状态 */ // awaiting_second、approved 或 rejected
	}
	return tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&job).Error
}

// MeterProposalExists 在已持任务锁的 tx 中当前读 requestID 是否有原提案。
// 返回存在标志或存储错误，不依赖事务先前的 RR 快照，不写入或提交事务。
func MeterProposalExists(tx *gorm.DB, requestID string) (bool, error) {
	var row MeterReview
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", requestID).Take(&row).Error
	return err == nil, ignoreMissingReview(err)
}

// ManualAmountExists 在已持任务锁的 tx 中当前读订单 id 或 requestID 的金额凭据。
// 返回存在标志或存储错误，不依赖事务先前的 RR 快照，不写入或提交事务。
func ManualAmountExists(tx *gorm.DB, id uint64, requestID string) (bool, error) {
	var row ManualSettlement
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=? OR request_id=?", id, requestID).Take(&row).Error
	return err == nil, ignoreMissingReview(err)
}

// ignoreMissingReview 将存在性读取的缺行 err 转为 nil，保留连接、取消等真实错误。
// 调用方同时使用存在标志；不能把数据库故障当成首次申请并继续写入。
func ignoreMissingReview(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}
