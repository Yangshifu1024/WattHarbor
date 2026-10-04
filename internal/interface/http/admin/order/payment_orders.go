package order

import (
	"errors"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"time"
	"unicode/utf8"
)

func (a API) paymentOrders(c *gin.Context) {
	page, ok := chargehttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	q := payment.PaymentOrderQuery{PageQuery: page, OrderNo: c.Query("order_no"), BizType: c.Query("biz_type"),
		PayMethod: c.Query("pay_method"), PaymentStatus: c.Query("payment_status")}
	if utf8.RuneCountInString(q.OrderNo) > 64 || !chargehttp.AdminOneOf(q.BizType, "charge wallet_recharge") ||
		!chargehttp.AdminOneOf(q.PayMethod, "wechat balance") || !chargehttp.AdminOneOf(q.PaymentStatus, "pending paid refunded partial_refunded") {
		httpapi.BadRequest(c, "支付单号、用途、支付方式或支付状态无效")
		return
	}
	var err error
	q.From, q.To, err = paymentTimeRange(c.Query("created_from"), c.Query("created_to"))
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	out, err := payment.PaymentOrders(c.Request.Context(), a.DB, q)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

func paymentTimeRange(fromRaw, toRaw string) (*time.Time, *time.Time, error) {
	var from, to *time.Time
	for _, bound := range []struct {
		raw    string
		target **time.Time
	}{{fromRaw, &from}, {toRaw, &to}} {
		raw, target := bound.raw, bound.target
		if raw == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || value.UTC().Year() < 1000 || value.UTC().Year() > 9999 {
			return nil, nil, errors.New("时间格式须为 ISO 8601，且在有效日期范围内")
		}
		*target = &value
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, errors.New("开始时间不能晚于结束时间")
	}
	return from, to, nil
}
