package finance

import (
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// API 注入本业务族所需的所属服务、窄查询能力和实时鉴权，不持有数据库连接。
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
