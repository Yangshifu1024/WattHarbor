package http

import (
	"database/sql"
	"encoding/json"
	"errors"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
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
	wallet, err := (database.WalletUserStore{DB: a.UserDB}).FindWalletBalance(c.Request.Context(), userID)
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
	if direction != "" && direction != "in" && direction != "out" {
		httpapi.BadRequest(c, "收支方向无效")
		return
	}
	out, err := (database.WalletUserStore{DB: a.UserDB}).ListWalletTxns(c.Request.Context(), userID, direction, page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "资金流水暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": out.Items, "total": out.Total, "page": page, "page_size": pageSize})
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
	out, err := (database.WalletUserStore{DB: a.UserDB}).ListWalletRecharges(c.Request.Context(), user, page, size)
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	for i := range out.Items {
		out.Items[i].CanPay = out.Items[i].Status == "initiated" && out.Items[i].ExpiredAt != nil && out.Items[i].ExpiredAt.After(time.Now())
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": out.Items, "total": out.Total, "page": page, "page_size": size})
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
	store := database.WalletUserStore{DB: a.UserDB}
	var openID string
	var order orderstore.PaymentOrderRecord
	err := a.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize all wallet requests for one owner, including simultaneous retries.
		openid, err := store.FindUserOpenID(tx, user)
		if err != nil || openid == "" {
			return errWalletIdentity
		}
		openID = openid
		if _, err := store.LockUser(tx, user); err != nil {
			return err
		}
		wallet, err := store.LockWalletAccount(tx, user)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = store.OpenWalletAccount(tx, user); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if wallet.Status != "active" {
			return errWalletFrozen
		}
		prior, err := store.FindRechargeRequest(tx, in.RequestID)
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
		order = orderstore.PaymentOrderRecord{OrderNo: orderNo, BizType: "wallet_recharge", UserID: user, PayMethod: "wechat", TotalCents: in.AmountCents, Status: "initiated", ExpiredAt: sql.NullTime{Time: now.Add(30 * time.Minute), Valid: true}, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		return store.InsertRechargeRequest(tx, database.WalletRechargeRequest{RequestID: in.RequestID, UserID: user, AmountCents: in.AmountCents, PaymentOrderID: order.ID, RequestJSON: mustJSON(in)})
	})
	if errors.Is(err, money.ErrPaymentIntentConflict) || errors.Is(err, errWalletFrozen) || errors.Is(err, errWalletIdentity) {
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
		var stored orderstore.ChargePrepayRecord
		var params orderstore.PrepayParams
		err := a.UserDB.WithContext(ctx).Where("payment_order_id=?", order.ID).Take(&stored).Error
		if err == nil {
			err = json.Unmarshal(stored.ParamsJSON, &params)
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			if a.Prepay == nil {
				httpapi.Write(c, 503, 5003, "支付渠道未配置", nil)
				return
			}
			params, err = a.Prepay.Prepay(ctx, orderstore.PrepayRequest{MerchantOrderNo: order.OrderNo, OpenID: openID, AmountCents: order.TotalCents, ExpiresAt: order.ExpiredAt.Time})
			if err == nil {
				encoded, _ := json.Marshal(params)
				stored = orderstore.ChargePrepayRecord{PaymentOrderID: order.ID, ParamsJSON: encoded, PrepayID: sql.NullString{String: params.PrepayID, Valid: params.PrepayID != ""}}
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
	store := database.WalletUserStore{DB: a.UserDB}
	err := a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		wallet, err := store.LockWalletAccount(tx, user)
		if err != nil {
			return err
		}
		prior, err := store.FindRefundRequest(tx, in.RequestID)
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
		var reason *string
		if in.Reason != "" {
			value := in.Reason
			reason = &value
		}
		if err := store.InsertRefundRequest(tx, in.RequestID, user, in.AmountCents, reason); err != nil {
			return err
		}
		var count int64
		if count, err = store.CountRecentRefundRequests(tx, user, time.Now().UTC().Add(-5*time.Minute)); err != nil {
			return err
		}
		status := "accepted"
		if count >= 3 {
			status = "manual_review"
			freezeID, err := store.InsertRiskFreeze(tx, user, "wallet_refund_frequency", "wallet", "wallet refund frequency")
			if err != nil {
				return err
			}
			if err := store.LinkFreezeToRefund(tx, in.RequestID, freezeID); err != nil {
				return err
			}
			if err := store.FreezeWallet(tx, wallet.ID); err != nil {
				return err
			}
		} else if err := walletrefund.ReserveRefund(tx, walletrefund.WalletRefundReservation{RequestID: in.RequestID, UserID: user, AmountCents: in.AmountCents, Reason: in.Reason}); err != nil {
			return err
		}
		response = map[string]any{"request_id": in.RequestID, "status": status, "refund_cents": in.AmountCents}
		b, _ := json.Marshal(response)
		return store.SetRefundResponse(tx, in.RequestID, string(b))
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
	out, err := (database.WalletUserStore{DB: a.UserDB}).ListWalletRefundRequests(c.Request.Context(), user, page, size)
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	rows := out.Items
	items := []gin.H{}
	store := database.WalletUserStore{DB: a.UserDB}
	for _, row := range rows {
		var parts []database.WalletRefundPartRow
		var err error
		if parts, err = store.ListWalletRefundParts(c.Request.Context(), a.UserDB, row.RequestID); err != nil {
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
		_, reviewErr := store.RiskReviewForRefund(c.Request.Context(), a.UserDB, row.RequestID)
		if reviewErr != nil && !errors.Is(reviewErr, gorm.ErrRecordNotFound) {
			resourceWriteFailure(c, reviewErr)
			return
		}
		var linked int64
		if linked, err = store.CountRefundFreezeLinks(c.Request.Context(), a.UserDB, row.RequestID); err != nil {
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
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": items, "total": out.Total, "page": page, "page_size": size})
}

// ---- 优惠券 ----
