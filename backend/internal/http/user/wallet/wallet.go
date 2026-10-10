package wallet

import (
	"errors"
	"strconv"
	"strings"
	"time"

	walletdomain "chargepilot/internal/domain/wallet"
	userhttp "chargepilot/internal/http/user"
	"chargepilot/internal/platform/httpapi"
	walletpkg "chargepilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// txnDirectionAllowed 返回 value 是否属于 walletdomain.TxnDirection 的合法取值串。
// 只识别已知取值，不解析、不改写；空值由调用方先行处理并视为不筛选。
func txnDirectionAllowed(value string) bool {
	for _, v := range strings.Fields(walletdomain.AllTxnDirections) {
		if value == v {
			return true
		}
	}
	return false
}

// walletBalance 返回当前用户的钱包余额。
func (a API) walletBalance(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
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

// walletTxns 分页返回当前用户的流水；direction 可选 in 或 out。
func (a API) walletTxns(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, pageSize, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	direction := strings.TrimSpace(c.Query("direction"))
	// 空方向表示不筛选；其余值必须在 walletdomain.AllTxnDirections 词表内。
	if direction != "" && !txnDirectionAllowed(direction) {
		httpapi.BadRequest(c, "收支方向无效")
		return
	}
	out, err := a.WalletQuery.Transactions(c.Request.Context(), userID, walletdomain.TxnDirection(direction), page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "资金流水暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": out.Items, "total": out.Total, "page": page, "page_size": pageSize})
}

// walletRecharges 返回当前用户的充值记录及是否可继续支付。
func (a API) walletRecharges(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, size, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	out, err := a.WalletQuery.Recharges(c.Request.Context(), user, page, size, time.Now())
	if err != nil {
		userhttp.WriteResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": out.Items, "total": out.Total, "page": page, "page_size": size})
}

// walletRecharge 校验请求编号和金额，创建充值单并返回支付参数。
// 渠道暂不可用时，用户须重试同一充值单。
func (a API) walletRecharge(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		// RequestID 当前用户请求编号，必须为 UUID。
		RequestID string `json:"request_id"`
		// AmountCents 请求金额，单位为分；限额在对应校验助手执行。
		AmountCents int64 `json:"amount_cents"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "请求体无效")
		return
	}
	// 字段错误逐项保留，调用方据此定位请求中的无效输入。
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
		userhttp.WriteResourceFailure(c, err)
	default:
		httpapi.OK(c, out)
	}
}

// walletRefund 校验请求编号、金额和原因，提交当前用户的钱包退款申请。
func (a API) walletRefund(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		// RequestID 当前用户请求编号，必须为 UUID。
		RequestID string `json:"request_id"`
		// AmountCents 请求金额，单位为分；限额在对应校验助手执行。
		AmountCents int64 `json:"amount_cents"`
		// Reason 退款申请原因，校验长度后再去除首尾空白。
		Reason string `json:"reason"`
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
		userhttp.WriteResourceFailure(c, err)
	default:
		httpapi.OK(c, response)
	}
}

// walletRefunds 分页返回当前用户的退款和审批状态。
func (a API) walletRefunds(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, size, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	out, err := a.WalletQuery.Refunds(c.Request.Context(), user, page, size)
	if err != nil {
		userhttp.WriteResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": out.Items, "total": out.Total, "page": page, "page_size": size})
}
