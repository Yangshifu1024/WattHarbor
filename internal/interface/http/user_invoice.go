package http

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
)

// 开票申请与我的发票。
func (a UserAccountAPI) myInvoices(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	out, err := settlement.ListUserInvoiceRequests(c.Request.Context(), a.UserDB, userID, page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "发票记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": out.Items, "total": out.Total, "page": page, "page_size": pageSize})
}

// applyInvoice 为客户自己某一笔已结算的订单申请发票。
// 金额取自订单，绝不取自请求。
func (a UserAccountAPI) applyInvoice(c *gin.Context) {
	userID, ok := a.userID(c)
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
	// 库里枚举存的是 normal / vat_special；小程序发过来的也是这两个名字。
	if !oneOfStatus(in.InvoiceType, "normal vat_special") {
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
		resourceWriteFailure(c, err)
	default:
		httpapi.OK(c, out)
	}

}

// ---- 共享工具 ----

func readPaging(c *gin.Context) (int, int, bool) {
	return readPagingDefault(c, 20)
}
