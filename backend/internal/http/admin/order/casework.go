package order

import (
	devstore "chargepilot/internal/device"
	"errors"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	ordercase "chargepilot/internal/order"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// registerCasework 注册反馈和设备报修路由，分别校验读取与操作权限。
// 反馈支持回复、关闭；报修支持指派、处理历史和 open → dispatched → fixed → closed 状态流转。
func (a API) registerCasework(r *gin.Engine) {
	r.GET("/api/v1/admin/feedback", a.Auth.Require("feedback.read"), a.listFeedback)
	r.POST("/api/v1/admin/feedback/:id/reply", a.Auth.Require("feedback.reply"), a.replyFeedback)
	r.GET("/api/v1/admin/device-fault-reports", a.Auth.Require("fault.read"), a.listFaultReports)
	r.GET("/api/v1/admin/device-fault-reports/:id/history", a.Auth.Require("fault.read"), a.faultHistory)
	r.POST("/api/v1/admin/device-fault-reports/:id/dispatch", a.Auth.Require("fault.dispatch"), a.dispatchFault)
	r.POST("/api/v1/admin/device-fault-reports/:id/resolve", a.Auth.Require("fault.resolve"), a.resolveFault)
}

// listFeedback 分页查询用户反馈。
func (a API) listFeedback(c *gin.Context) {
	q, ok := admin.ReadAdminPage(c, "pending processed closed")
	if !ok {
		return
	}
	out, err := a.Feedback.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), q)
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	writeCasePage(c, out.Items, out.Total, q)
}

// listFaultReports 分页查询设备报修工单。
func (a API) listFaultReports(c *gin.Context) {
	q, ok := admin.ReadAdminPage(c, "open dispatched fixed closed")
	if !ok {
		return
	}
	out, err := a.Faults.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), q)
	if err != nil {
		faultFailure(c, err)
		return
	}
	writeCasePage(c, out.Items, out.Total, q)
}

// writeCasePage 将查询结果整理为分页响应；大整数编号使用字符串，空图片列表返回 []。
func writeCasePage(c *gin.Context, items []map[string]any, total int64, q persist.PageQuery) {
	admin.NormalizeRows(items)
	httpapi.DecimalizeRowIDsAll(items, "id", "user_id", "order_id", "assigned_to")
	for _, row := range items {
		row["images"] = row["images_json"]
		if row["images"] == nil {
			row["images"] = []string{}
		}
		delete(row, "images_json")
	}
	httpapi.OK(c, persist.Page[map[string]any]{Items: items, Page: q.Page, PageSize: q.PageSize, Total: total})
}

// replyFeedback 提交反馈回复或关闭操作，由订单服务保存状态和审计记录。
func (a API) replyFeedback(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	var in ordercase.FeedbackReplyInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	err := a.Feedback.Reply(c.Request.Context(), admin.AdminAuditContext(c), id, in)
	var input *ordercase.FeedbackInputError
	if errors.As(err, &input) {
		httpapi.BadRequest(c, input.Message)
		return
	}
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"saved": true})
}

// faultFailure 将工单参数、权限和存储错误转换为接口响应。
func faultFailure(c *gin.Context, err error) {
	var input *devstore.FaultInputError
	if errors.As(err, &input) {
		httpapi.BadRequest(c, input.Message)
		return
	}
	if errors.Is(err, operations.ErrForbidden) {
		httpapi.Write(c, 403, 1003, "无权执行此操作", nil)
		return
	}
	admin.AdminResourceFailure(c, err)
}

// faultHistory 分页查询当前管理员范围内的报修处理历史。
func (a API) faultHistory(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	q, ok := admin.ReadAdminPage(c, "")
	if !ok {
		return
	}
	out, err := a.Faults.History(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), id, q)
	if err != nil {
		faultFailure(c, err)
		return
	}
	admin.NormalizeRows(out.Items)
	httpapi.DecimalizeRowIDsAll(out.Items, "event_id", "actor_id", "assigned_to")
	httpapi.OK(c, out)
}

// dispatchFault 将报修工单指派给指定人员，返回工单状态。
func (a API) dispatchFault(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	var in devstore.FaultDispatch
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if err := a.Faults.Dispatch(c.Request.Context(), admin.AdminAuditContext(c), id, in); err != nil {
		faultFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"assigned_to": in.AssignedTo, "status": devstore.FaultDispatched})
}

// resolveFault 更新报修工单状态和处理说明，返回工单状态。
func (a API) resolveFault(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	var in devstore.FaultResolution
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if err := a.Faults.Resolve(c.Request.Context(), admin.AdminAuditContext(c), id, in); err != nil {
		faultFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"status": in.Status})
}
