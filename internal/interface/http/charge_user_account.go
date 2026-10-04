package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UserAccountAPI 提供钱包、优惠券、发票、公告、附近站点、手机号绑定及用户故障上报接口。
type UserAccountAPI struct {
	WalletQuery    wallet.QueryService       // 余额、充值续付与退款进度查询能力
	Invoices       settlement.InvoiceService // 订单开票申请用例
	Auth           SessionAuthenticator
	UserDB         *gorm.DB
	AdminDB        *gorm.DB
	GatewayURL     string
	ServiceToken   string
	Gateway        serviceclient.Client
	WalletRecharge wallet.RechargeService // 充值申请及提交后预支付业务能力
	WalletRefund   wallet.RefundService   // 钱包退款业务能力，由 cmd 装配
}

// Register 注册用户账户路由；各处理器先验证会话，缺失或无效时返回 401。
func (a UserAccountAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/wallet/balance", a.walletBalance)
	r.GET("/api/v1/user/wallet/txns", a.walletTxns)
	r.GET("/api/v1/user/wallet/recharges", a.walletRecharges)
	r.POST("/api/v1/user/wallet/recharge", a.walletRecharge)
	r.GET("/api/v1/user/wallet/refunds", a.walletRefunds)
	r.POST("/api/v1/user/wallet/refund", a.walletRefund)
	r.GET("/api/v1/user/coupon/my", a.myCoupons)
	r.GET("/api/v1/user/announcement/list", a.announcements)
	r.GET("/api/v1/user/station/nearby", a.nearbyStations)
	r.GET("/api/v1/user/station/:id", a.stationDetail)
	r.POST("/api/v1/user/device/report-fault", a.reportFault)
	r.GET("/api/v1/user/device/fault-reports", a.myFaultReports)
	r.GET("/api/v1/user/device/fault-reports/:id/history", a.myFaultHistory)
	r.GET("/api/v1/user/invoice/my", a.myInvoices)
	r.POST("/api/v1/user/invoice/apply", a.applyInvoice)
	r.POST("/api/v1/user/charge/:order_id/feedback", a.submitFeedback)
}

func (a UserAccountAPI) userID(c *gin.Context) (uint64, bool) {
	id, ok := a.Auth.Authenticate(c)
	if !ok || id == 0 {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return 0, false
	}
	return id, true
}

// ---- 钱包 ----
