// Package order 提供后台订单、支付记录、反馈和报修工单接口。
package order

import (
	chargingquery "chargepilot/internal/charging/query"
	"chargepilot/internal/device"
	authhttp "chargepilot/internal/http/auth"
	ordercase "chargepilot/internal/order"
	"chargepilot/internal/payment"
	"github.com/gin-gonic/gin"
)

// API 提供订单查询和工单接口，依赖订单、计量、报修服务和后台鉴权。
type API struct {
	Faults   device.FaultService          // 报修范围查询、指派和状态处理
	Process  chargingquery.ProcessService // 订单授权身份与专用网关过程的完整读取
	Live     chargingquery.LiveService    // 实时费用估算，读取计量采样和订单价格快照
	Query    ordercase.QueryService       // 订单页、详情与事件的只读用例
	Feedback ordercase.FeedbackService    // 订单反馈列表、回复与审计事务
	Payments payment.OrderQueryService    // 支付订单分页与关联信息，由 cmd 注入。
	Auth     authhttp.AdminAuth           // 当前请求的后台会话、版本及路由权限校验
}

// Register 把订单与工单的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerCasework(r)
	r.GET("/api/v1/admin/orders", a.Auth.Require("order.read"), a.orders)
	r.GET("/api/v1/admin/payment-orders", a.Auth.Require("order.read"), a.paymentOrders)
	r.GET("/api/v1/admin/orders/:id", a.Auth.Require("order.read"), a.order)
	r.GET("/api/v1/admin/orders/:id/timeline", a.Auth.Require("order.read"), a.timeline)
	r.GET("/api/v1/admin/orders/:id/process", a.Auth.Require("order.read"), a.orderProcess)
}
