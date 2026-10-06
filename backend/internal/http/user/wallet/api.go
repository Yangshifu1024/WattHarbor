package wallet

import (
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// API 将本业务族的用户请求交给所属用例，字段由 cmd 装配。
type API struct {
	WalletQuery    wallet.QueryService           // 读取当前用户余额、流水、充值和退款状态。
	WalletRecharge wallet.RechargeService        // 执行充值申请及提交后的渠道预支付。
	WalletRefund   wallet.RefundService          // 执行钱包退款准入及申请事务。
	Auth           authhttp.SessionAuthenticator // 验证当前会话与用户状态；失败已写响应，处理器立即返回。
}

// Register 向 r 注册本业务族的原用户路由；只装配处理器，不执行查询或写入。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/user/wallet/balance", a.walletBalance)
	r.GET("/api/v1/user/wallet/txns", a.walletTxns)
	r.GET("/api/v1/user/wallet/recharges", a.walletRecharges)
	r.POST("/api/v1/user/wallet/recharge", a.walletRecharge)
	r.GET("/api/v1/user/wallet/refunds", a.walletRefunds)
	r.POST("/api/v1/user/wallet/refund", a.walletRefund)
}
