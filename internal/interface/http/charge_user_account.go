package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UserAccountAPI 适配所属账户能力及公开站点／公告查询；手机号由PhoneAPI单独注册。
type UserAccountAPI struct {
	Stations       device.UserStationService    // 所属公开站点和运行态组合
	Announcements  ops.AnnouncementQueryService // 所属公开公告读取
	Faults         device.FaultService          // 原用户工单提交、所属查询及可见历史
	CouponQuery    marketing.CouponQueryService // 所属持券状态、筛选及分页
	Feedback       order.FeedbackService        // 所属订单评价准入与提交事务
	WalletQuery    wallet.QueryService          // 余额、充值续付与退款进度查询能力
	Invoices       settlement.InvoiceService    // 订单开票申请用例
	Auth           authhttp.SessionAuthenticator
	UserDB         *gorm.DB
	WalletRecharge wallet.RechargeService // 充值申请及提交后预支付业务能力
	WalletRefund   wallet.RefundService   // 钱包退款业务能力，由 cmd 装配
}

// Register 注册原用户路由；站点／公告保持公开，账户处理器验证会话，失效返回401。
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
