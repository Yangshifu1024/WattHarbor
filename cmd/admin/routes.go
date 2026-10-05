package main

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	chargingquery "github.com/ChargePilot2026/charge-pilot/internal/charging/query"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	ordercase "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"net/http"
	"time"

	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	admindashboard "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/dashboard"
	admindevice "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/device"
	adminexport "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/export"
	adminfinance "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/finance"
	adminmarketing "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/marketing"
	adminops "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/ops"
	adminorder "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/order"
	adminpricing "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/pricing"
	adminstation "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/station"
	adminuser "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/user"
	adminvendor "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/vendor"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

func newRouter(cfg config.Admin, r *resources, authAPI identityhttp.AdminAuth, gateway *gatewayclient.Client) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
	gin.SetMode(gin.ReleaseMode)
	// 指标端点无需运营会话，只暴露路由模板、状态码与耗时。
	metrics := httpapi.NewMetrics("admin")
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
	// 后台业务按族注册：每个族是一个独立包，进程只挂自己提供的那些。
	adminops.API{DB: orm, Auth: authAPI}.Register(router)
	adminpricing.API{Queries: pricing.QueryService{DB: orm}, DB: orm, Auth: authAPI, Schemes: pricing.SchemeService{DB: orm}, Publications: pricing.PublicationService{DB: orm}}.Register(router)
	adminstation.API{Stations: device.StationService{DB: orm}, DB: orm, Auth: authAPI}.Register(router)
	admindevice.API{Management: device.ManagementService{DB: orm, Summaries: gateway}, Imports: device.ImportService{DB: orm, Gateway: gateway, Compatibility: pricing.Repo{DB: orm}, LoadScope: func(ctx context.Context, p operations.Profile) (operations.DataScope, error) {
		return ops.LoadDataScope(ctx, orm, p)
	}}, Meters: settlement.MeterReviewService{Orders: settlement.BillingOrders{DB: orm}, LookupReviewer: func(ctx context.Context, id uint64) (settlement.ReviewerStatus, error) {
		p, err := authAPI.Store.Profile(ctx, id)
		return settlement.ReviewerStatus{Role: p.Role, Permitted: p.HasPermission("billing.meter.review")}, err
	}}, DB: orm, Auth: authAPI}.Register(router)
	adminorder.API{Faults: device.FaultService{DB: orm}, Process: chargingquery.ProcessService{Orders: ordercase.QueryService{DB: orm}, Gateway: gateway}, Live: chargingquery.LiveService{Gateway: gateway, Orders: ordercase.SnapshotRepo{DB: orm}, Cards: card.PurchaseQueryRepo{DB: orm}}, Query: ordercase.QueryService{DB: orm}, Feedback: ordercase.FeedbackService{DB: orm}, DB: orm, Auth: authAPI}.Register(router)
	adminfinance.API{Refunds: settlement.RefundAdminService{DB: orm, ReviewRepo: settlement.RefundReviewRepo{DB: orm, LookupSigner: func(ctx context.Context, id uint64) (settlement.SignerStatus, error) {
		p, err := authAPI.Store.Profile(ctx, id)
		return settlement.SignerStatus{Role: p.Role, Permitted: p.HasPermission("order.refund.review")}, err
	}}}, Invoices: settlement.InvoiceReviewService{DB: orm, ReviewRepo: settlement.InvoiceReviewRepo{DB: orm, LookupReviewer: func(ctx context.Context, id uint64) (settlement.ReviewerStatus, error) {
		p, err := authAPI.Store.Profile(ctx, id)
		return settlement.ReviewerStatus{Role: p.Role, Permitted: p.HasPermission("invoice.review")}, err
	}}}, Risks: wallet.RiskService{DB: orm, Refund: wallet.RefundService{DB: orm, Sources: payment.RechargeSources{}, Recorder: settlement.RefundRecorder{}}}, DB: orm, Auth: authAPI}.Register(router)
	adminmarketing.API{Coupons: marketing.CouponService{DB: orm}, Activities: marketing.ActivityService{DB: orm}, Auth: authAPI}.Register(router)
	adminuser.API{Users: identity.UserQueryService{DB: orm, Recharges: wallet.RechargeHistoryService{Payments: payment.RechargeHistoryRepo{DB: orm}}}, Auth: authAPI, Cards: card.CardAdminService{DB: orm}}.Register(router)
	adminvendor.API{Vendors: device.VendorService{DB: orm, Gateway: gateway}, DB: orm, Auth: authAPI}.Register(router)
	admindashboard.Dashboard{DB: orm}.Register(router, authAPI)
	chargehttp.AdminAccounts{DB: orm, Auth: &authAPI}.RegisterRoles(router)
	chargehttp.AdminAccounts{DB: orm, Auth: &authAPI}.Register(router)
	adminexport.ExportTask{DB: orm, Auth: authAPI, ExportDir: cfg.ExportDir}.Register(router)

	return router
}
