package settlement

import (
	orderstore "chargepilot/internal/order"
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// ManualSettlement 保存无法补齐计量证据时的人工金额，仍不得超过购买套餐金额。
type ManualSettlement struct {
	ChargeOrderID uint64 `json:"charge_order_id"` // 同一订单 ID，一单仅允许一份最终金额
	RequestID     string `json:"request_id"`      // 裁决 UUID，同号不能换内容
	ActorID       uint64 `json:"actor_id"`        // 实时财务操作人，由服务覆盖客户端值
	ElectricCents int64  `json:"electric_cents"`  // 电费，分，非负且不超过 1000000
	ServiceCents  int64  `json:"service_cents"`   // 服务费，分，非负且不超过 1000000
	Reason        string `json:"reason"`          // 最终金额依据，1–1000 字
}

// TableName 返回人工最终金额所属表，不执行查询或修改。
func (ManualSettlement) TableName() string { return "charge_manual_settlement" }

// ResolveAmount 锁定计费任务后登记人工金额，核对首次请求和套餐上限。
// 金额、订单事件、重新排队同事务；调用方负责实时权限和审计，不在此执行资金入账。
func (s BillingOrders) ResolveAmount(ctx context.Context, in ManualSettlement) error {
	if in.ChargeOrderID == 0 || in.ActorID == 0 || uuid.Validate(in.RequestID) != nil || strings.TrimSpace(in.Reason) == "" || len([]rune(in.Reason)) > 1000 || in.ElectricCents < 0 || in.ServiceCents < 0 || in.ElectricCents > 1000000 || in.ServiceCents > 1000000 {
		return ErrBillingConflict
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct {
			Status string // 计费任务状态，仅 manual_review 可新建裁决
		}
		if err := tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", in.ChargeOrderID).Take(&job).Error; err != nil {
			return err
		}
		var previous ManualSettlement
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=? OR request_id=?", in.ChargeOrderID, in.RequestID).Take(&previous).Error
		if err == nil {
			if previous != in {
				return ErrBillingConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if job.Status != "manual_review" {
			return ErrBillingConflict
		}
		source, err := readOriginalBillingSource(tx, in.ChargeOrderID)
		if err != nil {
			return err
		}
		if source.Offer == nil || in.ElectricCents+in.ServiceCents > source.Offer.PriceCents {
			return ErrBillingConflict
		}
		if err := tx.Create(&in).Error; err != nil {
			return err
		}
		if err := orderstore.AppendEventInTx(tx, orderstore.ChargeEventLogRecord{ChargeOrderID: in.ChargeOrderID, EventID: in.RequestID, Event: "manual_settlement", Actor: "admin", Detail: in.Reason, OccurredAt: time.Now().UTC()}); err != nil {
			return err
		}
		return tx.Table("charge_billing_job").Where("charge_order_id=?", in.ChargeOrderID).Updates(map[string]any{"status": "pending", "last_error": nil, "next_attempt_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
	})
}
