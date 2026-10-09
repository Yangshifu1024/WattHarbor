package wallet

import (
	"errors"
	"strconv"
	"strings"
	"time"

	userhttp "chargepilot/internal/http/user"
	"chargepilot/internal/platform/httpapi"
	walletpkg "chargepilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// walletBalance 在 c 的请求取消范围验证身份并读取所属余额，写余额回执或 503；不更改资金。
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

// walletTxns 从 c 验证身份并解析分页和收支方向，返回所属流水页；参数错误为 400、查询失败为 503，无资金写入。
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

// walletRecharges 从 c 验证身份、解析分页，并以当前时间读取所属充值及续付状态；写分页回执或 404／503，不创建充值。
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

// walletRecharge 从 c 验证身份并绑定 UUID 请求号和分值，校验后调用所属充值用例。
// 成功写充值回执；参数错误为 400、冲突或冻结为 409、渠道及存储故障为 503；资金事务和预支付由用例执行。
func (a API) walletRecharge(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		// RequestID 当前用户请求身份，必须为 UUID。
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

// walletRefund 从 c 验证身份并绑定 UUID 请求号、分值和原因，校验后调用钱包退款用例。
// 成功写申请回执；参数错误为 400、余额不足或额度冲突及冻结为 409，其他错误保持 404／503；退款与冻结写入归所属用例。
func (a API) walletRefund(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		// RequestID 当前用户请求身份，必须为 UUID。
		RequestID string `json:"request_id"`
		// AmountCents 请求金额，单位为分；限额在对应校验助手执行。
		AmountCents int64 `json:"amount_cents"`
		// Reason 退款申请原因，校验原长度后再去除首尾空白。
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

// walletRefunds 从 c 验证身份并解析分页，读取所属退款及审批状态；写用户编号和分页回执或 404／503，不写资金。
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
