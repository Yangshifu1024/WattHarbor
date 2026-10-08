package settlement

import (
	"context"
	"errors"
	"fmt"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"

	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BillRepo 根据计费回执生成用户账单，金额以已确认的结算结果为准。
type BillRepo struct {
	DB *gorm.DB // central 账单连接，由进程管理
}

// Bill 是用户账单的已确认费用展示，不用当前遥测重算金额。
type Bill struct {
	ID             uint64     `json:"id,string" gorm:"column:id"`                      // 账单主键
	BillNo         string     `json:"bill_no" gorm:"column:bill_no"`                   // 原订单派生账单号，重试沿用
	ChargeOrderID  uint64     `json:"charge_order_id" gorm:"column:charge_order_id"`   // 所属原充电订单，一单一份账单
	PaymentOrderID *uint64    `json:"payment_order_id" gorm:"column:payment_order_id"` // 原支付主键，无关联时为空
	UserID         uint64     `json:"user_id,string" gorm:"column:user_id"`            // 原账单所属用户，公开为十进制字符串
	DeviceID       string     `json:"device_id" gorm:"column:device_id"`               // 原设备编号
	PortNo         uint8      `json:"port_no" gorm:"column:port_no"`                   // 原设备端口，从 1 开始
	ElectricCents  int64      `json:"electric_cents" gorm:"column:electric_cents"`     // 回执确定电费，分
	ServiceCents   int64      `json:"service_cents" gorm:"column:service_cents"`       // 回执确定服务费，分
	TotalCents     int64      `json:"total_cents" gorm:"column:total_cents"`           // 回执确定总费，分
	PrepaidCents   int64      `json:"prepaid_cents" gorm:"column:prepaid_cents"`       // 原支付已确认实付，分
	RefundCents    int64      `json:"refund_cents" gorm:"column:refund_cents"`         // 原支付成功退回累计，分
	ShortfallCents int64      `json:"shortfall_cents" gorm:"column:shortfall_cents"`   // 确定资金缺口，分
	ChargedKWh     *string    `json:"charged_kwh" gorm:"column:charged_kwh"`           // 已确认累计电量，kWh 十进制文本，缺计量为空
	ChargedSeconds *int64     `json:"charged_seconds" gorm:"column:charged_seconds"`   // 已确认充电时长，秒，缺计量为空
	Status         string     `json:"status" gorm:"column:status"`                     // issued 已出账或 settled 已结清
	IssuedAt       time.Time  `json:"issued_at" gorm:"column:issued_at"`               // 账单归属时刻，优先原充电开始时刻
	SettledAt      *time.Time `json:"settled_at" gorm:"column:settled_at"`             // 结清 UTC 时刻，未结清为空
	Read           bool       `json:"read" gorm:"-"`                                   // 本用户是否已有已读凭据
}

// Issue 为已结算订单生成账单；charge_order_id 唯一键保证重复调用不新增账单。
// ctx 取消数据库操作，chargeOrderID 为原订单；返回账单、可出账标志和错误。
// false、nil 表示尚无可出账的正费用回执；true 也可能复用旧账单，不表示首次创建。
// 账单及订单事件同事务，失败可按原订单重试；error 非 nil 时账单结果不可使用。
func (s BillRepo) Issue(ctx context.Context, chargeOrderID uint64) (Bill, bool, error) {
	if s.DB == nil || chargeOrderID == 0 {
		return Bill{}, false, errors.New("bill store is not configured")
	}
	var order struct {
		ID             uint64     `gorm:"column:id"`               // 原充电订单主键
		OrderNo        string     `gorm:"column:order_no"`         // 原充电订单号
		UserID         uint64     `gorm:"column:user_id"`          // 原订单所属用户
		DeviceID       string     `gorm:"column:device_id"`        // 原设备编号
		PortNo         uint8      `gorm:"column:port_no"`          // 原设备端口，从 1 开始
		PaymentOrderID *int64     `gorm:"column:payment_order_id"` // 原支付主键，无关联时为空
		ChargedKWh     *string    `gorm:"column:charged_kwh"`      // 已确认累计电量，kWh 十进制文本，缺计量为空
		ChargedSeconds *int64     `gorm:"column:charged_seconds"`  // 已确认充电时长，秒，缺计量为空
		StartedAt      *time.Time `gorm:"column:started_at"`       // 原充电开始 UTC 时刻，尚未启动为空
		CreatedMonth   time.Time  `gorm:"column:created_month"`    // 原记录 UTC 月首分区键
	}
	err := s.DB.WithContext(ctx).Table("charge_order").
		Select("id, order_no, user_id, device_id, port_no, payment_order_id, charged_kwh, charged_seconds, started_at, created_month").
		Where("id = ? AND deleted_at IS NULL", chargeOrderID).Take(&order).Error
	if err != nil {
		return Bill{}, false, err
	}
	var receipt ChargeFeeRecord
	if err := s.DB.WithContext(ctx).Where("charge_order_id = ?", order.ID).Take(&receipt).Error; err != nil {
		// 计费未完成时尚无账单。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Bill{}, false, nil
		}
		return Bill{}, false, err
	}
	electric, service, total, ok := receipt.Fees()
	if !ok || total <= 0 {
		return Bill{}, false, nil
	}

	var prepaid, refunded int64
	if order.PaymentOrderID != nil {
		var payment struct {
			PaidCents     int64 `gorm:"column:paid_cents"`     // 原支付已确认实付，分
			RefundedCents int64 `gorm:"column:refunded_cents"` // 原支付成功退回累计，分
		}
		if err := s.DB.WithContext(ctx).Table("payment_order").
			Select("paid_cents, refunded_cents").Where("id = ?", *order.PaymentOrderID).Take(&payment).Error; err == nil {
			prepaid, refunded = payment.PaidCents, payment.RefundedCents
		}
	}
	shortfall := receipt.ShortfallCents
	if shortfall <= 0 && total > prepaid {
		shortfall = total - prepaid
	}

	billNo := "BILL" + fmt.Sprintf("%020d", order.ID)
	issuedAt := time.Now().UTC()
	if order.StartedAt != nil {
		// 账单日期使用充电发生时间，不使用计费执行时间。
		issuedAt = order.StartedAt.Add(0)
	}
	month := time.Date(issuedAt.Year(), issuedAt.Month(), 1, 0, 0, 0, 0, time.UTC)
	row := map[string]any{
		"bill_no": billNo, "charge_order_id": order.ID, "payment_order_id": order.PaymentOrderID,
		"user_id": order.UserID, "device_id": order.DeviceID, "port_no": order.PortNo,
		"electric_cents": electric, "service_cents": service, "total_cents": total,
		"prepaid_cents": prepaid, "refund_cents": refunded, "shortfall_cents": shortfall,
		"charged_kwh": order.ChargedKWh, "charged_seconds": order.ChargedSeconds,
		"status": "issued", "issued_at": issuedAt, "created_month": month,
	}
	var billID uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 通过订单唯一键复用已有账单，保证插入幂等。
		existing := int64(0)
		if err := tx.Table("charge_bill").
			Where("charge_order_id = ? AND created_month = ?", order.ID, month).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			if err := tx.Table("charge_bill").
				Where("charge_order_id = ? AND created_month = ?", order.ID, month).
				Pluck("id", &billID).Error; err != nil {
				return err
			}
			return nil
		}
		if err := tx.Table("charge_bill").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&billID).Error; err != nil {
			return err
		}
		// 将账单事件追加到订单时间线，供用户查询结算记录。
		eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("bill-issued:"+billNo)).String()
		return orderstore.AppendEventInTx(tx, orderstore.ChargeEventLogRecord{
			ChargeOrderID: order.ID, EventID: eventID, Event: "bill_issued", Actor: "billing",
			Detail:     fmt.Sprintf("bill=%s total=%d prepaid=%d shortfall=%d", billNo, total, prepaid, shortfall),
			OccurredAt: time.Now().UTC(),
		})
	})
	if err != nil {
		return Bill{}, false, err
	}
	bill, err := s.Load(ctx, chargeOrderID)
	return bill, true, err
}

// Missing 返回最多 limit 笔满足现有正费用出账条件但没有账单的订单 ID。
// ctx 取消查询，limit 必须为 1–50；返回空集合、nil 表示无待恢复账单，错误表示查询失败。
// 查询仅读取已入账费用回执及订单事实；包括 backlog，不能以 fee_delivery 状态替代入账。
func (s BillRepo) Missing(ctx context.Context, limit int) ([]uint64, error) {
	if s.DB == nil || limit < 1 || limit > 50 {
		return nil, errors.New("invalid bill recovery query")
	}
	ids := []uint64{}
	err := s.DB.WithContext(ctx).Table("charge_fee_receipt f").
		Joins("JOIN charge_order c ON c.id=f.charge_order_id AND c.deleted_at IS NULL").
		Where("NOT EXISTS (SELECT 1 FROM charge_bill b WHERE b.charge_order_id=c.id)").
		Where("CAST(JSON_UNQUOTE(JSON_EXTRACT(f.result_json,'$.total_cents')) AS SIGNED)>0").
		Order("c.id").Limit(limit).Pluck("c.id", &ids).Error
	return ids, err
}

// Load 在 ctx 下按 chargeOrderID 读取账单，返回原账单或 gorm.ErrRecordNotFound／存储错误；不写入，也不核对用户，调用方负责授权。
func (s BillRepo) Load(ctx context.Context, chargeOrderID uint64) (Bill, error) {
	bill := Bill{}
	err := s.DB.WithContext(ctx).Table("charge_bill").
		Select("id, bill_no, charge_order_id, payment_order_id, user_id, device_id, port_no, electric_cents, service_cents, total_cents, prepaid_cents, refund_cents, shortfall_cents, charged_kwh, charged_seconds, status, issued_at, settled_at").
		Where("charge_order_id = ?", chargeOrderID).Take(&bill).Error
	return bill, err
}

// LoadByID 在 ctx 下按 userID 和 billID 读取所属账单，返回原账单或缺行／存储错误；不能读取他人账单，不修改已读状态。
func (s BillRepo) LoadByID(ctx context.Context, userID, billID uint64) (Bill, error) {
	bill := Bill{}
	err := s.DB.WithContext(ctx).Table("charge_bill").
		Select("id, bill_no, charge_order_id, payment_order_id, user_id, device_id, port_no, electric_cents, service_cents, total_cents, prepaid_cents, refund_cents, shortfall_cents, charged_kwh, charged_seconds, status, issued_at, settled_at").
		Where("id = ? AND user_id = ?", billID, userID).Take(&bill).Error
	return bill, err
}

// Settle 在 ctx 下将 chargeOrderID 的 issued 账单标为 settled 并记录结清时刻；返回更新错误。调用方须先确认无未结金额，本方法不核算资金或请求渠道。
func (s BillRepo) Settle(ctx context.Context, chargeOrderID uint64) error {
	return s.DB.WithContext(ctx).Table("charge_bill").
		Where("charge_order_id = ? AND status = 'issued'", chargeOrderID).
		Updates(map[string]any{"status": "settled", "settled_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
}

// ListBills 在 ctx 下按 userID、page、pageSize 返回稳定倒序账单及总数，批量补查本页已读凭据；存储错误返回 error，不修改账单。已读补查失败沿用 false 展示，不影响原金额。
func (s BillRepo) ListBills(ctx context.Context, userID uint64, page, pageSize int) ([]Bill, int64, error) {
	base := s.DB.WithContext(ctx).Table("charge_bill").Where("user_id = ?", userID)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []Bill{}
	if err := base.
		Select("id, bill_no, charge_order_id, payment_order_id, user_id, device_id, port_no, electric_cents, service_cents, total_cents, prepaid_cents, refund_cents, shortfall_cents, charged_kwh, charged_seconds, status, issued_at, settled_at, created_month").
		Order("issued_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	if len(rows) > 0 {
		ids := make([]uint64, 0, len(rows))
		months := make([]time.Time, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
			months = append(months, row.IssuedAt.UTC())
		}
		var reads []struct {
			BillID uint64 `gorm:"column:bill_id"` // 已读凭据对应账单主键
		}
		// 账单已读记录使用账单 ID 和相同的分区月份共同寻址。
		if err := s.DB.WithContext(ctx).Table("charge_bill_read").
			Select("bill_id").Where("user_id = ? AND bill_id IN ?", userID, ids).Find(&reads).Error; err == nil {
			seen := map[uint64]bool{}
			for _, read := range reads {
				seen[read.BillID] = true
			}
			for i := range rows {
				rows[i].Read = seen[rows[i].ID]
			}
		}
		_ = months
	}
	return rows, total, nil
}

// MarkRead 在 ctx 下为 userID 的 billID 保存原分区已读凭据；无所属账单或存储错误返回 error，已有凭据返回 nil，无资金或账单状态变更。
func (s BillRepo) MarkRead(ctx context.Context, userID, billID uint64) error {
	var bill struct {
		CreatedMonth time.Time `gorm:"column:created_month"` // 原记录 UTC 月首分区键
	}
	if err := s.DB.WithContext(ctx).Table("charge_bill").
		Select("created_month").Where("id = ? AND user_id = ?", billID, userID).Take(&bill).Error; err != nil {
		return err
	}
	var existing int64
	if err := s.DB.WithContext(ctx).Table("charge_bill_read").
		Where("bill_id = ? AND created_month = ?", billID, bill.CreatedMonth).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	return s.DB.WithContext(ctx).Table("charge_bill_read").Create(map[string]any{
		"bill_id": billID, "created_month": bill.CreatedMonth, "user_id": userID,
	}).Error
}
