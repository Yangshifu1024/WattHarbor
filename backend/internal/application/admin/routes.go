package admin

import (
	"context"
	"fmt"
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
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/pricing"
	"chargepilot/internal/settlement"
	"chargepilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// newRouter 注册本进程的业务路由、指标和健康检查，不启动监听。
// 健康检查使用请求 context，最多等待两秒。
func newRouter(cfg config.Admin, r *resources, authAPI authhttp.AdminAuth, gateway *gatewayclient.Client) (*gin.Engine, error) {
	db, orm, cache := r.DB, r.ORM, r.Cache
	gin.SetMode(gin.ReleaseMode)
	// /metrics 无需登录，包含请求、进程和数据库连接池统计。
	metrics := httpapi.NewMetrics("admin", httpapi.DatabasePool{Schema: "central_db", DB: r.DB})
	router := httpapi.NewRouter(metrics)
	if err := router.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, fmt.Errorf("admin trusted proxies: %w", err)
	}
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
	adminops.DiagnosticsAPI{
		DB: orm, Auth: authAPI, Transport: serviceclient.Client{HTTP: r.InternalHTTP}, Token: cfg.ServiceToken,
		GatewayURL: cfg.GatewayInternalURL, WorkerURL: cfg.WorkerInternalURL,
	}.Register(router)

	// 审核时直接查询首位审核人的当前角色和权限，不从缓存读取。
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

	// 按业务模块注册后台路由，共用数据范围查询服务。
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
	// 账号和角色管理共用鉴权服务；分别注册角色、权限字典和账号路由。
	// 账号创建路由仍由运营模块注册。
	accounts := adminaccounthttp.AdminAccounts{Accounts: ops.AccountService{DB: orm}, Roles: ops.RoleService{DB: orm}, Auth: &authAPI}
	accounts.RegisterRoles(router)
	accounts.Register(router)
	adminexport.ExportTask{Service: exportpkg.ExportService{DB: orm, ExportDir: cfg.ExportDir}, Auth: authAPI}.Register(router)

	return router, nil
}
