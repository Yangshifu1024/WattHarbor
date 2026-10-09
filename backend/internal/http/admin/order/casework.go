package order

import (
	"errors"

	devstore "chargepilot/internal/device"
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

// listFeedback 从 c 读取分页并调用订单所属全局反馈用例，输出归一化原字段或原错误。
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

// listFaultReports 解析 c 原分页后调用设备工单用例，输出原行页或业务错误，无事务。
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

// writeCasePage 将 c 的 items 原驱动投影归一化为公开 total/q 页，精确身份使用字符串，空图片为数组；不查询或写表。
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

// replyFeedback 从 c 绑定原动作并委托完整订单反馈事务，状态和审计由用例提交。
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

// faultFailure 把 err 原工单输入／权限／存储错误映射到 c，不改变报告或权限。
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

// faultHistory 将 c 原ID／分页传给所属当前范围查询，输出原事件页，不访问表。
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

// dispatchFault 绑定 c 原人员及备注并调用完整指派事务，输出原状态，不拥有流转和审计。
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
	httpapi.OK(c, gin.H{"assigned_to": in.AssignedTo, "status": "dispatched"})
}

// resolveFault 绑定 c 原目标状态及说明并调用完整处理事务，输出原状态，不拥有状态机或审计。
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
