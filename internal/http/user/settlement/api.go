package settlement

import (
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// InvoiceAPI 将本业务族的用户请求交给所属用例，字段由 cmd 装配。
type InvoiceAPI struct {
	Invoices settlement.InvoiceService     // 读取所属发票并执行订单开票申请。
	Auth     authhttp.SessionAuthenticator // 验证当前会话与用户状态；失败已写响应，处理器立即返回。
}

// Register 向 r 注册本业务族的原用户路由；只装配处理器，不执行查询或写入。
func (a InvoiceAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/invoice/my", a.myInvoices)
	r.POST("/api/v1/user/invoice/apply", a.applyInvoice)
}
