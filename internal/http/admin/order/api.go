package order

import (
	chargingquery "github.com/ChargePilot2026/charge-pilot/internal/charging/query"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	ordercase "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Faults   device.FaultService          // 设备工单当前范围、查询及完整流转事务
	Process  chargingquery.ProcessService // 订单授权身份与具名网关过程的完整读取
	Live     chargingquery.LiveService    // 所属实时估算用例，批量取证和冻结计价读取
	Query    ordercase.QueryService       // 原订单页、详情与事件的所属只读用例
	Feedback ordercase.FeedbackService    // 原订单反馈列表、回复与审计事务
	DB       *gorm.DB                     // cmd注入的支付订单只读连接，生命周期由进程管理
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
