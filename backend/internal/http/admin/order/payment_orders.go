package order

import (
	"errors"
	"time"
	"unicode/utf8"

	adminhttp "chargepilot/internal/http/admin"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// paymentOrders 按用途、渠道、状态和时间范围分页查询支付记录。
func (a API) paymentOrders(c *gin.Context) {
	page, ok := adminhttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	q := payment.PaymentOrderQuery{PageQuery: page, OrderNo: c.Query("order_no"), BizType: c.Query("biz_type"),
		PayMethod: c.Query("pay_method"), PaymentStatus: c.Query("payment_status")}
	if utf8.RuneCountInString(q.OrderNo) > 64 || !adminhttp.AdminOneOf(q.BizType, "charge wallet_recharge") ||
		!adminhttp.AdminOneOf(q.PayMethod, "wechat balance") || !adminhttp.AdminOneOf(q.PaymentStatus, "pending paid refunded partial_refunded") {
		httpapi.BadRequest(c, "支付单号、用途、支付方式或支付状态无效")
		return
	}
	var err error
	q.From, q.To, err = paymentTimeRange(c.Query("created_from"), c.Query("created_to"))
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	out, err := a.Payments.List(c.Request.Context(), q)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// paymentTimeRange 解析支付时间上下界；空值表示不限制该侧，拒绝格式错误或起止倒置。
// 转换为 UTC 后，年份须在 MySQL 允许的 1000–9999 范围内。
func paymentTimeRange(fromRaw, toRaw string) (*time.Time, *time.Time, error) {
	var from, to *time.Time
	for _, bound := range []struct {
		raw    string      // 单侧查询字符串，空值表示无边界
		target **time.Time // 对应解析结果的写入位置，成功才赋值
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
