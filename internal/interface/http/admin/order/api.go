package order

import (
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/gin-gonic/gin"
)

// API 是订单与工单接口的处理器。装配来自 admin.Deps，族内不感知自己被哪个
// 进程挂载：admin 与 central 各自注册自己需要的族。
type API struct{ admin.Deps }

// Register 把订单与工单的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerCasework(r)
	r.GET("/api/v1/admin/orders", a.Auth.Require("order.read"), a.orders)
	r.GET("/api/v1/admin/payment-orders", a.Auth.Require("order.read"), a.paymentOrders)
	r.GET("/api/v1/admin/orders/:id", a.Auth.Require("order.read"), a.order)
	r.GET("/api/v1/admin/orders/:id/timeline", a.Auth.Require("order.read"), a.timeline)
	r.GET("/api/v1/admin/orders/:id/process", a.Auth.Require("order.read"), a.orderProcess)
}
