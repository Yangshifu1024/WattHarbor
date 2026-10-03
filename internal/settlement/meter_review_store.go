package settlement

import (
	"context"
	"encoding/json"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MeterReview struct {
	ID               uint64          `json:"id"`
	RequestID        string          `json:"request_id"`
	ChargeOrderID    uint64          `json:"charge_order_id"`
	OriginalJSON     json.RawMessage `json:"original"`
	CorrectedJSON    json.RawMessage `json:"corrected"`
	Reason           string          `json:"reason"`
	Status           string          `json:"status"`
	FirstReviewerID  uint64          `json:"first_reviewer_id"`
	SecondReviewerID *uint64         `json:"second_reviewer_id"`
	RejectReason     *string         `json:"reject_reason"`
	CreatedAt        time.Time       `json:"created_at"`
	ReviewedAt       *time.Time      `json:"reviewed_at"`
}

func (MeterReview) TableName() string { return "charge_meter_review" }

func (s BillingOrders) ProposeMeter(ctx context.Context, id, actor uint64, request, reason string, segments []charging.MeterSegment) (MeterReview, error) {
	var out MeterReview
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct{ Status string }
		if err := tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&job).Error; err != nil {
			return err
		}
		var prior MeterReview
		found := tx.Where("request_id=?", request).Find(&prior)
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
		var count int64
		if err := tx.Model(&MeterReview{}).Where("charge_order_id=? AND status IN ('awaiting_second','approved')", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrBillingConflict
		}
		source, err := readOriginalBillingSource(tx, id)
		if err != nil {
			return err
		}
		original, _ := json.Marshal(source)
		source.Meter.Segments = segments
		// Missing cutoff evidence must be resolved by an audited final amount;
		// adding physical end segments alone does not prove that boundary.
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
func (s BillingOrders) ReviewMeter(ctx context.Context, id, reviewID, actor uint64, approve bool, reason string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct{ Status string }
		if err := tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&job).Error; err != nil {
			return err
		}
		var review MeterReview
		if err := tx.Where("id=? AND charge_order_id=?", reviewID, id).Take(&review).Error; err != nil {
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
