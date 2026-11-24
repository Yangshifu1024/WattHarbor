// Package wallet 提供当前用户的钱包余额、流水、充值和退款接口。
package wallet

import (
	"github.com/gin-gonic/gin"
	authhttp "wattharbor/internal/http/auth"
	"wattharbor/internal/wallet"
)

// API 将本业务族的用户请求交给用例，字段由 cmd 装配。
type API struct {
	WalletQuery    wallet.QueryService           // 读取当前用户余额、流水、充值和退款状态。
	WalletRecharge wallet.RechargeService        // 执行充值申请及提交后的渠道预支付。
	WalletRefund   wallet.RefundService          // 执行钱包退款条件校验及申请事务。
	Auth           authhttp.SessionAuthenticator // 验证当前会话与用户状态；失败已写响应，处理器立即返回。
}

// Register 向 r 注册本业务族的用户路由；只装配处理器，不执行查询或写入。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/user/wallet/balance", a.walletBalance)
	r.GET("/api/v1/user/wallet/txns", a.walletTxns)
	r.GET("/api/v1/user/wallet/recharges", a.walletRecharges)
	r.POST("/api/v1/user/wallet/recharge", a.walletRecharge)
	r.GET("/api/v1/user/wallet/refunds", a.walletRefunds)
	r.POST("/api/v1/user/wallet/refund", a.walletRefund)
}
