package order

import (
	"errors"
	devstore "github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	ordercase "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"time"
	"unicode/utf8"
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
	q, ok := chargehttp.ReadAdminPage(c, "pending processed closed")
	if !ok {
		return
	}
	out, err := a.Feedback.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), q)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	writeCasePage(c, out.Items, out.Total, q)
}

// listFaultReports 从 c 读取原分页并调用设备所属 repo；故障业务编排在 D-02 继续迁移。
func (a API) listFaultReports(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "open dispatched fixed closed")
	if !ok {
		return
	}
	rows, total, err := (devstore.FaultRepo{DB: a.DB}).ListReports(c.Request.Context(), q.Status, q.Page, q.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	writeCasePage(c, rows, total, q)
}

// writeCasePage 将 c 的 items 原驱动投影归一化为公开 total/q 页，精确身份使用字符串，空图片为数组；不查询或写表。
func writeCasePage(c *gin.Context, items []map[string]any, total int64, q chargehttp.AdminPageQuery) {
	admin.NormalizeRows(items)
	httpapi.DecimalizeRowIDsAll(items, "id", "user_id", "order_id", "assigned_to")
	for _, row := range items {
		row["images"] = row["images_json"]
		if row["images"] == nil {
			row["images"] = []string{}
		}
		delete(row, "images_json")
	}
	httpapi.OK(c, admin.Page[map[string]any]{Items: items, Page: q.Page, PageSize: q.PageSize, Total: total})
}

// replyFeedback 从 c 绑定原动作并委托完整订单反馈事务，状态和审计由用例提交。
func (a API) replyFeedback(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in ordercase.FeedbackReplyInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	err := a.Feedback.Reply(c.Request.Context(), chargehttp.AdminAuditContext(c), id, in)
	var input *ordercase.FeedbackInputError
	if errors.As(err, &input) {
		httpapi.BadRequest(c, input.Message)
		return
	}
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"saved": true})
}

// faultHistory 分页读取报修单的流转事件、操作人、备注及可见性。
// 先校验工单存在，不存在时返回错误，避免与无历史记录混淆。
func (a API) faultHistory(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	q, ok := chargehttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	store := devstore.FaultRepo{DB: a.DB}
	exists, err := store.FaultExists(c.Request.Context(), id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if !exists {
		chargehttp.AdminResourceFailure(c, gorm.ErrRecordNotFound)
		return
	}
	items, total, err := store.ListFaultEvents(c.Request.Context(), id, q.Page, q.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	admin.NormalizeRows(items)
	httpapi.DecimalizeRowIDsAll(items, "event_id", "actor_id", "assigned_to")
	httpapi.OK(c, admin.Page[map[string]any]{Items: items, Page: q.Page, PageSize: q.PageSize, Total: total})
}

// dispatchFault 校验处理人存在且持有 fault.resolve 权限，将报修置为 dispatched。
// 已有处理人时记录 reassigned，否则记录 dispatched；处理历史对用户可见，同人重复指派幂等。
func (a API) dispatchFault(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		AssignedTo uint64  `json:"assigned_to"` // 处理人管理员 ID，必填且非 0
		Note       *string `json:"note"`        // 备注，可空；非空时最多 2000 字
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.AssignedTo == 0 || (in.Note != nil && utf8.RuneCountInString(*in.Note) > devstore.MaxFaultNoteLength) {
		httpapi.BadRequest(c, "请指定有效人员，备注最多 2000 字")
		return
	}
	assignee, err := a.Auth.Store.Profile(c.Request.Context(), in.AssignedTo)
	if err != nil || !assignee.HasPermission("fault.resolve") {
		httpapi.BadRequest(c, "指派账号不存在、未启用或无故障处理权限")
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	var auditPending []chargehttp.AuditEntry
	store := devstore.FaultRepo{DB: a.DB}
	err = chargehttp.AuditedTransaction(c, a.DB, &auditPending, func(tx *gorm.DB) error {
		row, err := store.LockFault(tx, id)
		if err != nil {
			return err
		}
		if row.Status != "open" && row.Status != "dispatched" {
			return admin.ErrConflict
		}
		if row.AssignedTo != nil && *row.AssignedTo == in.AssignedTo {
			return nil
		}
		event := "dispatched"
		if row.AssignedTo != nil {
			event = "reassigned"
		}
		if err := store.UpdateReport(tx, id, map[string]any{"status": "dispatched", "assigned_to": in.AssignedTo}); err != nil {
			return err
		}
		if err := store.AppendFaultEvent(tx, devstore.FaultEventInput{
			ReportID: id, ActorID: p.ID, EventType: event, FromStatus: row.Status, ToStatus: "dispatched",
			AssignedTo: in.AssignedTo, Note: in.Note, UserVisible: true,
		}); err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: event, Target: "device_fault_report", ID: id, Before: row, After: in, RequestID: ""}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"assigned_to": in.AssignedTo, "status": "dispatched"})
}

// resolveFault 由当前处理人把报修单标记为 fixed 或 closed。
// 只有被指派的本人能操作；fixed 必须从 dispatched 来、closed 必须从 fixed 来，保证修复和关闭两步都留痕。
// 标记 fixed 时必须写修复说明。重复提交同一目标状态是幂等的。
func (a API) resolveFault(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		Status string  `json:"status"` // 目标状态：fixed 已修复 或 closed 已关闭
		Note   *string `json:"note"`   // 备注；status 为 fixed 时必填，1–2000 字
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if (in.Status != devstore.FaultFixed && in.Status != devstore.FaultClosed) ||
		(in.Note != nil && utf8.RuneCountInString(*in.Note) > devstore.MaxFaultNoteLength) ||
		(devstore.FaultNoteRequired(in.Status) && (in.Note == nil || !operations.ValidText(*in.Note, devstore.MaxFaultNoteLength))) {
		httpapi.BadRequest(c, "状态无效或缺少修复说明")
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	var auditPending []chargehttp.AuditEntry
	store := devstore.FaultRepo{DB: a.DB}
	err := chargehttp.AuditedTransaction(c, a.DB, &auditPending, func(tx *gorm.DB) error {
		row, err := store.LockFault(tx, id)
		if err != nil {
			return err
		}
		if row.AssignedTo == nil || *row.AssignedTo != p.ID {
			return admin.ErrConflict
		}
		// 同状态重放视为幂等，不写事件也不报错；其余只允许 open→dispatched→fixed→closed。
		if err := devstore.CanTransitionFault(row.Status, in.Status); err != nil {
			return admin.ErrConflict
		}
		if row.Status == in.Status {
			return nil
		}
		if err := store.UpdateReport(tx, id, map[string]any{"status": in.Status, "resolved_at": time.Now().UTC()}); err != nil {
			return err
		}
		if err := store.AppendFaultEvent(tx, devstore.FaultEventInput{
			ReportID: id, ActorID: p.ID, EventType: in.Status, FromStatus: row.Status, ToStatus: in.Status,
			AssignedTo: p.ID, Note: in.Note, UserVisible: true,
		}); err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: in.Status, Target: "device_fault_report", ID: id, Before: row, After: in, RequestID: ""}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"status": in.Status})
}
