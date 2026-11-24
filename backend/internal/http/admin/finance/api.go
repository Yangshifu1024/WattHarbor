// Package finance 提供退款、发票和钱包风控审核接口。
package finance

import (
	"github.com/gin-gonic/gin"
	authhttp "wattharbor/internal/http/auth"
	"wattharbor/internal/settlement"
	"wattharbor/internal/wallet"
)

// API 提供退款、发票和钱包风控接口，依赖相应业务服务和后台鉴权。
type API struct {
	Risks    wallet.RiskService              // 钱包审核与解冻用例，由 cmd 注入
	Refunds  settlement.RefundAdminService   // 人工退款事务，由 cmd 装配
	Invoices settlement.InvoiceReviewService // 发票审核事务，由 cmd 装配
	Auth     authhttp.AdminAuth              // 实时后台权限及会话适配
}

// Register 把资金与财务的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerRefunds(r)
	a.registerInvoices(r)
	a.registerWalletRisks(r)
}
