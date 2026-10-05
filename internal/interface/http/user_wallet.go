package http

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	walletpkg "github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/gin-gonic/gin"
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
	out, err := a.WalletQuery.Balance(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "钱包暂时无法读取", nil)
		return
	}
	httpapi.OK(c, out)
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
	out, err := a.WalletQuery.Transactions(c.Request.Context(), userID, direction, page, pageSize)
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
	out, err := a.WalletQuery.Recharges(c.Request.Context(), user, page, size, time.Now())
	if err != nil {
		resourceWriteFailure(c, err)
		return
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
	out, err := a.WalletRecharge.Create(c.Request.Context(), user, walletpkg.RechargeInput{RequestID: in.RequestID, AmountCents: in.AmountCents})
	switch {
	case errors.Is(err, walletpkg.ErrRechargeConflict), errors.Is(err, walletpkg.ErrFrozen), errors.Is(err, walletpkg.ErrIdentity):
		httpapi.Write(c, 409, 2009, "请求号冲突或钱包已冻结", nil)
	case errors.Is(err, walletpkg.ErrPrepayNotConfigured):
		httpapi.Write(c, 503, 5003, "支付渠道未配置", nil)
	case errors.Is(err, walletpkg.ErrPrepayUnavailable):
		httpapi.Write(c, 503, 5003, "支付渠道暂不可用，请重试同一充值单", nil)
	case err != nil:
		resourceWriteFailure(c, err)
	default:
		httpapi.OK(c, out)
	}
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
	response, err := a.WalletRefund.Request(c.Request.Context(), user, walletpkg.RefundInput{RequestID: in.RequestID, AmountCents: in.AmountCents, Reason: in.Reason})
	switch {
	case errors.Is(err, walletpkg.ErrInsufficientBalance):
		httpapi.Write(c, 409, 2009, "可用余额不足", nil)
	case errors.Is(err, walletpkg.ErrWalletRefundConflict):
		httpapi.Write(c, 409, 2009, "申请冲突或原充值可退额度不足", nil)
	case errors.Is(err, walletpkg.ErrFrozen):
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
	out, err := a.WalletQuery.Refunds(c.Request.Context(), user, page, size)
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": out.Items, "total": out.Total, "page": page, "page_size": size})
}

// ---- 优惠券 ----
