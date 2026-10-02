package http

import (
	"database/sql"
	"encoding/json"
	"errors"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	walletrefund "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
	"time"
)

// 钱包：余额、流水、充值与余额退款。
func (a UserAccountAPI) walletBalance(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var wallet struct {
		BalanceCents int64  `gorm:"column:balance_cents"`
		FrozenCents  int64  `gorm:"column:frozen_cents"`
		Status       string `gorm:"column:status"`
	}
	err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_account").
		Where("user_id = ? AND deleted_at IS NULL", userID).Take(&wallet).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.OK(c, gin.H{"balance_cents": 0, "frozen_cents": 0, "available_cents": 0, "status": "active"})
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "钱包暂时无法读取", nil)
		return
	}
	available := max(wallet.BalanceCents-wallet.FrozenCents, 0)
	httpapi.OK(c, gin.H{
		"balance_cents": wallet.BalanceCents, "frozen_cents": wallet.FrozenCents,
		"available_cents": available, "status": wallet.Status,
	})
}

func (a UserAccountAPI) walletTxns(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	direction := strings.TrimSpace(c.Query("direction"))
	query := a.UserDB.WithContext(c.Request.Context()).Table("wallet_txn").Where("user_id = ?", userID)
	if direction != "" {
		if direction != "in" && direction != "out" {
			httpapi.BadRequest(c, "收支方向无效")
			return
		}
		query = query.Where("direction = ?", direction)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "资金流水暂时无法读取", nil)
		return
	}
	rows := []map[string]any{}
	if err := query.Select("txn_no, direction, amount_cents, balance_after_cents, biz_type, biz_ref, note, created_at, created_month").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "资金流水暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// Wallet requests use owner-scoped UUIDs. The payment and frozen funds are
// recorded atomically; channel calls can be retried against the same order.
func (a UserAccountAPI) walletRecharges(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	page, size, ok := readPaging(c)
	if !ok {
		return
	}
	q := a.UserDB.WithContext(c.Request.Context()).Table("wallet_recharge_request r").Where("r.user_id=?", user)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	rows := []struct {
		RequestID   string     `json:"request_id"`
		AmountCents int64      `json:"amount_cents"`
		Status      string     `json:"status"`
		ExpiredAt   *time.Time `json:"expires_at"`
		CreatedAt   time.Time  `json:"created_at"`
		CanPay      bool       `json:"can_pay" gorm:"-"`
	}{}
	if err := q.Select("r.request_id,r.amount_cents,r.created_at,p.status,p.expired_at").Joins("JOIN payment_order p ON p.id=r.payment_order_id").Order("r.created_at DESC,r.request_id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	for i := range rows {
		rows[i].CanPay = rows[i].Status == "initiated" && rows[i].ExpiredAt != nil && rows[i].ExpiredAt.After(time.Now())
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": rows, "total": total, "page": page, "page_size": size})
}
func (a UserAccountAPI) walletRecharge(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		AmountCents int64  `json:"amount_cents"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "请求体无效")
		return
	}
	// 逐项列出违规字段，前端可据此定位到具体输入框而不是只得到一句笼统提示。
	if errs := rechargeFieldErrors(in.RequestID, in.AmountCents); len(errs) > 0 {
		httpapi.BadRequest(c, "充值参数不合法", errs...)
		return
	}
	ctx := c.Request.Context()
	var openid string
	if err := a.UserDB.WithContext(ctx).Table("user").Where("id=? AND deleted_at IS NULL", user).Pluck("openid", &openid).Error; err != nil || openid == "" {
		httpapi.Write(c, 409, 2009, "账号身份不可用", nil)
		return
	}
	var order money.PaymentOrderRecord
	err := a.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize all wallet requests for one owner, including simultaneous retries.
		var owner struct{ ID uint64 }
		if err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", user).Take(&owner).Error; err != nil {
			return err
		}
		var wallet struct {
			ID     uint64
			Status string
		}
		err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", user).Take(&wallet).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Table("wallet_account").Create(map[string]any{"user_id": user, "balance_cents": 0, "frozen_cents": 0, "status": "active"}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if wallet.Status != "active" {
			return errWalletFrozen
		}
		var prior struct {
			UserID         uint64
			AmountCents    int64
			PaymentOrderID uint64
		}
		err = tx.Table("wallet_recharge_request").Where("request_id=?", in.RequestID).Take(&prior).Error
		if err == nil {
			if prior.UserID != user || prior.AmountCents != in.AmountCents {
				return money.ErrPaymentIntentConflict
			}
			return tx.Where("id=?", prior.PaymentOrderID).Take(&order).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		orderNo, err := orderstore.NewOrderNumber(tx)
		if err != nil {
			return err
		}
		order = money.PaymentOrderRecord{OrderNo: orderNo, BizType: "wallet_recharge", UserID: user, PayMethod: "wechat", TotalCents: in.AmountCents, Status: "initiated", ExpiredAt: sql.NullTime{Time: now.Add(30 * time.Minute), Valid: true}, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		return tx.Table("wallet_recharge_request").Create(map[string]any{"request_id": in.RequestID, "user_id": user, "amount_cents": in.AmountCents, "payment_order_id": order.ID, "request_json": mustJSON(in)}).Error
	})
	if errors.Is(err, money.ErrPaymentIntentConflict) || errors.Is(err, errWalletFrozen) {
		httpapi.Write(c, 409, 2009, "请求号冲突或钱包已冻结", nil)
		return
	}
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	canPay := order.Status == "initiated" && order.ExpiredAt.Valid && order.ExpiredAt.Time.After(time.Now())
	response := gin.H{"request_id": in.RequestID, "payment_order_id": order.ID, "merchant_order_no": order.OrderNo, "amount_cents": order.TotalCents, "status": order.Status, "can_pay": canPay}
	if canPay {
		var stored money.ChargePrepayRecord
		var params orderstore.PrepayParams
		err := a.UserDB.WithContext(ctx).Where("payment_order_id=?", order.ID).Take(&stored).Error
		if err == nil {
			err = json.Unmarshal(stored.ParamsJSON, &params)
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			if a.Prepay == nil {
				httpapi.Write(c, 503, 5003, "支付渠道未配置", nil)
				return
			}
			params, err = a.Prepay.Prepay(ctx, orderstore.PrepayRequest{MerchantOrderNo: order.OrderNo, OpenID: openid, AmountCents: order.TotalCents, ExpiresAt: order.ExpiredAt.Time})
			if err == nil {
				encoded, _ := json.Marshal(params)
				stored = money.ChargePrepayRecord{PaymentOrderID: order.ID, ParamsJSON: encoded, PrepayID: sql.NullString{String: params.PrepayID, Valid: params.PrepayID != ""}}
				err = a.UserDB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&stored).Error
				if err == nil {
					err = a.UserDB.WithContext(ctx).Where("payment_order_id=?", order.ID).Take(&stored).Error
				}
				if err == nil {
					err = json.Unmarshal(stored.ParamsJSON, &params)
				}
			}
		}
		if err != nil {
			httpapi.Write(c, 503, 5003, "支付渠道暂不可用，请重试同一充值单", nil)
			return
		}
		response["payment_params"] = params
	}
	httpapi.OK(c, response)
}

func (a UserAccountAPI) walletRefund(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		AmountCents int64  `json:"amount_cents"`
		Reason      string `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "请求体无效")
		return
	}
	if errs := walletRefundFieldErrors(in.RequestID, in.AmountCents, in.Reason); len(errs) > 0 {
		httpapi.BadRequest(c, "退款参数不合法", errs...)
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	var response map[string]any
	err := a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var wallet struct {
			ID                        uint64
			BalanceCents, FrozenCents int64
			Status                    string
		}
		if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", user).Take(&wallet).Error; err != nil {
			return err
		}
		var prior struct {
			UserID       uint64
			AmountCents  int64
			Reason       *string
			ResponseJSON string
		}
		err := tx.Table("wallet_refund_request").Where("request_id=?", in.RequestID).Take(&prior).Error
		if err == nil {
			reason := ""
			if prior.Reason != nil {
				reason = *prior.Reason
			}
			if prior.UserID != user || prior.AmountCents != in.AmountCents || reason != in.Reason {
				return orderstore.ErrRefundConflict
			}
			return json.Unmarshal([]byte(prior.ResponseJSON), &response)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if wallet.Status != "active" {
			return errWalletFrozen
		}
		if wallet.BalanceCents-wallet.FrozenCents < in.AmountCents {
			return ErrInsufficientBalance
		}
		if err := tx.Table("wallet_refund_request").Create(map[string]any{"request_id": in.RequestID, "user_id": user, "amount_cents": in.AmountCents, "reason": in.Reason}).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Table("wallet_refund_request").Where("user_id=? AND created_at>=?", user, time.Now().UTC().Add(-5*time.Minute)).Count(&count).Error; err != nil {
			return err
		}
		status := "accepted"
		if count >= 3 {
			status = "manual_review"
			if err := tx.Table("risk_freeze_log").Create(map[string]any{"user_id": user, "trigger_rule": "wallet_refund_frequency", "frozen_action": "wallet"}).Error; err != nil {
				return err
			}
			var id uint64
			if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
				return err
			}
			if err := tx.Table("wallet_risk_freeze_link").Create(map[string]any{"request_id": in.RequestID, "freeze_id": id}).Error; err != nil {
				return err
			}
			if err := tx.Table("wallet_account").Where("id=?", wallet.ID).Updates(map[string]any{"status": "frozen", "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		} else if err := walletrefund.ReserveRefund(tx, walletrefund.WalletRefundReservation{RequestID: in.RequestID, UserID: user, AmountCents: in.AmountCents, Reason: in.Reason}); err != nil {
			return err
		}
		response = map[string]any{"request_id": in.RequestID, "status": status, "refund_cents": in.AmountCents}
		b, _ := json.Marshal(response)
		return tx.Table("wallet_refund_request").Where("request_id=?", in.RequestID).Update("response_json", string(b)).Error
	})
	switch {
	case errors.Is(err, ErrInsufficientBalance):
		httpapi.Write(c, 409, 2009, "可用余额不足", nil)
	case errors.Is(err, orderstore.ErrRefundConflict), errors.Is(err, orderstore.ErrWalletRefundConflict):
		httpapi.Write(c, 409, 2009, "申请冲突或原充值可退额度不足", nil)
	case errors.Is(err, errWalletFrozen):
		httpapi.Write(c, 409, 2009, "钱包已冻结，请等待审核", nil)
	case err != nil:
		resourceWriteFailure(c, err)
	default:
		httpapi.OK(c, response)
	}
}

func (a UserAccountAPI) walletRefunds(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	page, size, ok := readPaging(c)
	if !ok {
		return
	}
	q := a.UserDB.WithContext(c.Request.Context()).Table("wallet_refund_request").Where("user_id=?", user)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	rows := []struct {
		RequestID   string
		AmountCents int64
		Reason      *string
		CreatedAt   time.Time
	}{}
	if err := q.Order("created_at DESC,request_id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		parts := []struct {
			RefundNo      string     `json:"refund_no"`
			RefundCents   int64      `json:"refund_cents"`
			Status        string     `json:"status"`
			FailureReason *string    `json:"failure_reason"`
			CompletedAt   *time.Time `json:"completed_at"`
		}{}
		if err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_refund_part p").Select("r.refund_no,r.refund_cents,r.status,r.failure_reason,r.completed_at").Joins("JOIN refund_record r ON r.id=p.refund_record_id").Where("p.request_id=?", row.RequestID).Order("r.id").Find(&parts).Error; err != nil {
			resourceWriteFailure(c, err)
			return
		}
		status := "pending"
		var refunded int64
		for _, part := range parts {
			switch part.Status {
			case "success":
				refunded += part.RefundCents
			case "failed", "needs_review":
				status = "needs_review"
			case "processing":
				if status != "needs_review" {
					status = "processing"
				}
			}
		}
		if refunded == row.AmountCents {
			status = "success"
		}
		var review struct {
			Approved bool   `json:"approved"`
			Comment  string `json:"comment"`
			ActorID  uint64 `json:"actor_id"`
		}
		reviewErr := a.UserDB.WithContext(c.Request.Context()).Table("wallet_risk_review").Where("request_id=?", row.RequestID).Take(&review).Error
		if reviewErr != nil && !errors.Is(reviewErr, gorm.ErrRecordNotFound) {
			resourceWriteFailure(c, reviewErr)
			return
		}
		var linked int64
		if err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_risk_freeze_link").Where("request_id=?", row.RequestID).Count(&linked).Error; err != nil {
			resourceWriteFailure(c, err)
			return
		}
		if linked > 0 && errors.Is(reviewErr, gorm.ErrRecordNotFound) {
			status = "manual_review"
		} else if reviewErr == nil && !review.Approved {
			status = "rejected"
		}
		item := gin.H{"request_id": row.RequestID, "amount_cents": row.AmountCents, "refunded_cents": refunded, "status": status, "reason": row.Reason, "created_at": row.CreatedAt, "refund_orders": parts}
		if reviewErr == nil {
			item["review"] = review
		}
		items = append(items, item)
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": items, "total": total, "page": page, "page_size": size})
}

// ---- 优惠券 ----
