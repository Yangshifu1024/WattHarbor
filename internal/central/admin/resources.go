package admin

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ResourceStore contains domain views of the same central database handle.
type ResourceStore struct{ AdminDB, UserDB, BillingDB *gorm.DB }

// ResourceAPI 是除登录鉴权外全部后台业务接口的处理器，聚合了数据句柄、鉴权中间件
// 和调用网关所需的地址与令牌。
type ResourceAPI struct {
	Store                    ResourceStore        // 各模块的数据库句柄
	Auth                     chargehttp.AdminAuth // 鉴权与权限中间件（chargehttp.AdminAuth.Register 负责认证路由）
	GatewayURL, ServiceToken string               // 调用网关用的地址与内部服务令牌
	ExportDir                string               // 导出任务的落地目录
}

// Register 把所有后台业务路由挂到引擎上：先交给各业务域自己的 register 函数，
// 再补站点、设备、订单这三组直接定义在本文件里的接口。
func (a ResourceAPI) Register(r *gin.Engine) {
	a.registerOperations(r)
	a.registerChargingSchemes(r)
	a.registerOnlineCards(r)
	a.registerCoupons(r)
	a.registerActivityRules(r)
	a.registerChargeUsers(r)
	a.registerAuditLogs(r)
	a.registerSplitTemplates(r)
	a.registerCasework(r)
	a.registerInvoices(r)
	a.registerMeterReviews(r)
	a.registerRefunds(r)
	a.registerWalletRisks(r)
	a.registerImports(r)
	a.registerVendors(r)
	a.registerFinanceOps(r)
	a.registerWebhookDelivery(r)
	chargehttp.AdminAccounts{DB: a.Store.AdminDB, Auth: &a.Auth}.RegisterRoles(r)
	chargehttp.AdminAccounts{DB: a.Store.AdminDB, Auth: &a.Auth}.Register(r)
	ExportTask{Store: a.Store, Auth: a.Auth, ExportDir: a.ExportDir}.register(r)
	r.GET("/api/v1/admin/stations", a.Auth.Require("station.read"), a.stations)
	r.GET("/api/v1/admin/stations/:id", a.Auth.Require("station.read"), a.station)
	r.POST("/api/v1/admin/stations", a.Auth.Require("station.create"), a.createStation)
	r.PUT("/api/v1/admin/stations/:id", a.Auth.Require("station.update"), a.updateStation)
	r.PUT("/api/v1/admin/stations/:id/split-template", a.Auth.Require("station.update"), a.Auth.Require("finance.split_template.create"), a.bindStationSplitTemplate)
	r.GET("/api/v1/admin/devices", a.Auth.Require("device.read"), a.devices)
	r.GET("/api/v1/admin/devices/:id", a.Auth.Require("device.read"), a.device)
	r.PUT("/api/v1/admin/devices/:id", a.Auth.Require("device.operate"), a.updateDevice)
	r.PUT("/api/v1/admin/devices/:id/status", a.Auth.Require("device.operate"), a.setDeviceStatus)
	r.GET("/api/v1/admin/orders", a.Auth.Require("order.read"), a.orders)
	r.GET("/api/v1/admin/payment-orders", a.Auth.Require("order.read"), a.paymentOrders)
	r.GET("/api/v1/admin/orders/:id", a.Auth.Require("order.read"), a.order)
	r.GET("/api/v1/admin/orders/:id/timeline", a.Auth.Require("order.read"), a.timeline)
	r.GET("/api/v1/admin/orders/:id/process", a.Auth.Require("order.read"), a.orderProcess)
}

// PageQuery 是全后台统一的分页与筛选入参，各列表接口在此基础上再拼自己的条件。
type PageQuery = chargehttp.AdminPageQuery

// Page 是后台列表接口的统一响应体。
type Page[T any] = chargehttp.AdminPage[T]
