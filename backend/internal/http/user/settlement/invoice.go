package settlement

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	userhttp "wattharbor/internal/http/user"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/settlement"
)

// myInvoices 分页返回当前用户的发票申请。
func (a InvoiceAPI) myInvoices(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, pageSize, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	out, err := a.Invoices.List(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "发票记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": out.Items, "total": out.Total, "page": page, "page_size": pageSize})
}

// applyInvoice 校验开票参数，提交当前用户订单的发票申请；金额由订单确定。
func (a InvoiceAPI) applyInvoice(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in settlement.InvoiceInput
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "发票申请无效")
		return
	}
	if uuid.Validate(in.RequestID) != nil || strings.TrimSpace(in.OrderNo) == "" {
		httpapi.BadRequest(c, "请提供 UUID 请求号与订单号")
		return
	}
	// 发票类型沿用 normal / vat_special；空值沿用校验助手的放行规则。
	if !userhttp.AllowedStatus(in.InvoiceType, "normal vat_special") {
		httpapi.BadRequest(c, "发票类型无效")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || len([]rune(title)) > 128 {
		httpapi.BadRequest(c, "发票抬头无效")
		return
	}
	if len([]rune(in.TaxNo)) > 32 {
		httpapi.BadRequest(c, "税号过长")
		return
	}
	if len(in.Email) > 128 || (in.Email != "" && !strings.Contains(in.Email, "@")) {
		httpapi.BadRequest(c, "邮箱格式无效")
		return
	}
	out, err := a.Invoices.Create(c.Request.Context(), userID, in)
	switch {
	case errors.Is(err, settlement.ErrInvoiceUnbilled):
		httpapi.Write(c, 409, 2009, "订单尚未计费，暂不能申请发票", nil)
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.Write(c, 404, 1004, "订单不存在", nil)
	case errors.Is(err, settlement.ErrInvoiceOrderLookup):
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
	case errors.Is(err, settlement.ErrInvoiceUnfinished):
		httpapi.Write(c, 409, 2009, "订单尚未完成，暂不能申请发票", nil)
	case errors.Is(err, settlement.ErrInvoiceZero):
		httpapi.Write(c, 409, 2009, "订单金额为零，暂不能申请发票", nil)
	case errors.Is(err, settlement.ErrInvoiceDuplicate):
		httpapi.Write(c, 409, 2009, "该订单已申请过发票", nil)
	case err != nil:
		userhttp.WriteResourceFailure(c, err)
	default:
		httpapi.OK(c, out)
	}

}
