package main

import (
	"context"
	"net/http"
	"time"

	"chargepilot/internal/card"
	chargingquery "chargepilot/internal/charging/query"
	"chargepilot/internal/device"
	exportpkg "chargepilot/internal/export"
	adminaccounthttp "chargepilot/internal/http/admin/account"
	admindashboard "chargepilot/internal/http/admin/dashboard"
	admindevice "chargepilot/internal/http/admin/device"
	adminexport "chargepilot/internal/http/admin/export"
	adminfinance "chargepilot/internal/http/admin/finance"
	adminmarketing "chargepilot/internal/http/admin/marketing"
	adminops "chargepilot/internal/http/admin/ops"
	adminorder "chargepilot/internal/http/admin/order"
	adminpricing "chargepilot/internal/http/admin/pricing"
	adminstation "chargepilot/internal/http/admin/station"
	adminuser "chargepilot/internal/http/admin/user"
	adminvendor "chargepilot/internal/http/admin/vendor"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/identity"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/marketing"
	"chargepilot/internal/ops"
	ordercase "chargepilot/internal/order"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/pricing"
	"chargepilot/internal/settlement"
	"chargepilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// newRouter 注册当前进程路由、指标与健康端点；cfg 提供导出目录等配置，r 提供已打开存储，authAPI 提供后台身份能力，gateway 提供网关查询与控制。
// 返回未监听的 Gin 引擎，无错误返回；设置全局 Gin 发布模式，仅装配不执行业务请求，健康处理器用请求 context 的两秒子期限检查存储。
func newRouter(cfg config.Admin, r *resources, authAPI authhttp.AdminAuth, gateway *gatewayclient.Client) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
	gin.SetMode(gin.ReleaseMode)
	// 指标端点无需运营会话，只暴露路由模板、状态码与耗时。
	metrics := httpapi.NewMetrics("admin", httpapi.DatabasePool{Schema: "central_db", DB: r.DB})
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if db.PingContext(check) != nil || cache.Ping(check).Err() != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "storage unavailable", nil)
			return
		}
		httpapi.OK(c, gin.H{"status": "admin_storage_ready"})
	})

	authAPI.Register(router)

	// 计量复核、发票审核与人工退款签署共用同一实时权限语义：闭包在审核事务内
	// 按账号 ID 读取当前角色并断言所需权限位，不落缓存，权限回收即时生效；
	// 三者仅权限位与结果类型不同（ReviewerStatus／SignerStatus）。
	lookupMeterReviewer := func(ctx context.Context, id uint64) (settlement.ReviewerStatus, error) {
		p, err := authAPI.Sessions.Accounts.Profile(ctx, id)
		return settlement.ReviewerStatus{Role: p.Role, Permitted: p.HasPermission("billing.meter.review")}, err
	}
	lookupInvoiceReviewer := func(ctx context.Context, id uint64) (settlement.ReviewerStatus, error) {
		p, err := authAPI.Sessions.Accounts.Profile(ctx, id)
		return settlement.ReviewerStatus{Role: p.Role, Permitted: p.HasPermission("invoice.review")}, err
	}
	lookupRefundSigner := func(ctx context.Context, id uint64) (settlement.SignerStatus, error) {
		p, err := authAPI.Sessions.Accounts.Profile(ctx, id)
		return settlement.SignerStatus{Role: p.Role, Permitted: p.HasPermission("order.refund.review")}, err
	}

	// 后台业务按族注册：每个族是一个独立包，进程只挂自己提供的那些。scopes 为
	// 无状态数据范围查询，全族共用同一实例；各字面量统一以 Auth 收尾。
	scopes := ops.ScopeQueryService{DB: orm}
	adminops.API{
		Accounts:      ops.AccountService{DB: orm},
		Alerts:        device.AlertService{DB: orm},
		Announcements: ops.AnnouncementService{DB: orm},
		Audits:        ops.AuditQueryService{DB: orm},
		Auth:          authAPI,
	}.Register(router)

	adminpricing.API{
		Queries:      pricing.QueryService{DB: orm},
		Scopes:       scopes,
		Schemes:      pricing.SchemeService{DB: orm},
		Publications: pricing.PublicationService{DB: orm},
		Auth:         authAPI,
	}.Register(router)

	adminstation.API{
		Stations: device.StationService{DB: orm},
		Scopes:   scopes,
		Auth:     authAPI,
	}.Register(router)

	admindevice.API{
		Management: device.ManagementService{DB: orm, Summaries: gateway},
		Imports: device.ImportService{
			DB:            orm,
			Gateway:       gateway,
			Compatibility: pricing.Repo{DB: orm},
			LoadScope:     scopes.Load,
		},
		Meters: settlement.MeterReviewService{
			Orders:         settlement.BillingOrders{DB: orm},
			LookupReviewer: lookupMeterReviewer,
		},
		Scopes: scopes,
		Auth:   authAPI,
	}.Register(router)

	adminorder.API{
		Faults: device.FaultService{DB: orm},
		Process: chargingquery.ProcessService{
			Orders:  ordercase.QueryService{DB: orm},
			Gateway: gateway,
		},
		Live: chargingquery.LiveService{
			Gateway: gateway,
			Orders:  ordercase.SnapshotRepo{DB: orm},
			Cards:   card.PurchaseQueryRepo{DB: orm},
		},
		Query:    ordercase.QueryService{DB: orm},
		Feedback: ordercase.FeedbackService{DB: orm},
		Payments: payment.OrderQueryService{DB: orm},
		Auth:     authAPI,
	}.Register(router)

	adminfinance.API{
		Refunds: settlement.RefundAdminService{
			DB:         orm,
			ReviewRepo: settlement.RefundReviewRepo{DB: orm, LookupSigner: lookupRefundSigner},
		},
		Invoices: settlement.InvoiceReviewService{
			DB:         orm,
			ReviewRepo: settlement.InvoiceReviewRepo{DB: orm, LookupReviewer: lookupInvoiceReviewer},
		},
		Risks: wallet.RiskService{
			DB:     orm,
			Refund: wallet.RefundService{DB: orm, Sources: payment.RechargeSources{}, Recorder: settlement.RefundRecorder{}},
		},
		Auth: authAPI,
	}.Register(router)

	adminmarketing.API{
		Coupons:    marketing.CouponService{DB: orm},
		Activities: marketing.ActivityService{DB: orm},
		Auth:       authAPI,
	}.Register(router)

	adminuser.API{
		Users: identity.UserQueryService{
			DB:        orm,
			Recharges: wallet.RechargeHistoryService{Payments: payment.RechargeHistoryRepo{DB: orm}},
		},
		Cards: card.CardAdminService{DB: orm},
		Auth:  authAPI,
	}.Register(router)

	adminvendor.API{
		Vendors: device.VendorService{DB: orm, Gateway: gateway},
		Scopes:  scopes,
		Auth:    authAPI,
	}.Register(router)

	admindashboard.Dashboard{Queries: ops.DashboardQueryService{DB: orm}}.Register(router, authAPI)
	// 账号与角色管理共用同一装配：RegisterRoles 挂角色与权限字典路由，Register
	// 挂账号管理路由（创建路径仍由运营族注册）；Auth 取指针以满足该包的会话写入口。
	accounts := adminaccounthttp.AdminAccounts{Accounts: ops.AccountService{DB: orm}, Roles: ops.RoleService{DB: orm}, Auth: &authAPI}
	accounts.RegisterRoles(router)
	accounts.Register(router)
	adminexport.ExportTask{Service: exportpkg.ExportService{DB: orm, ExportDir: cfg.ExportDir}, Auth: authAPI}.Register(router)

	return router
}
