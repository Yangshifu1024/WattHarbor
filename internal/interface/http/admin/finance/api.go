package finance

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	DB       *gorm.DB                        // 原资金列表只读投影连接，由进程管理
	Risks    wallet.RiskService              // 钱包审核与解冻用例，由 cmd 注入
	Refunds  settlement.RefundAdminService   // 人工退款事务，由 cmd 装配
	Invoices settlement.InvoiceReviewService // 发票审核事务，由 cmd 装配
	Auth     chargehttp.AdminAuth            // 实时后台权限及会话适配
}

// Register 把资金与财务的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerRefunds(r)
	a.registerInvoices(r)
	a.registerWalletRisks(r)
}
