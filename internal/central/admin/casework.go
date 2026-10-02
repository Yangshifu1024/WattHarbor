package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/devices"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	paystore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"time"
	"unicode/utf8"
)

// registerCasework 注册反馈和设备报修路由，分别校验读取与操作权限。
// 反馈支持回复、关闭；报修支持指派、处理历史和 open → dispatched → fixed → closed 状态流转。
func (a ResourceAPI) registerCasework(r *gin.Engine) {
	r.GET("/api/v1/admin/feedback", a.Auth.Require("feedback.read"), func(c *gin.Context) { a.caseList(c, "feedback", "pending processed closed") })
	r.POST("/api/v1/admin/feedback/:id/reply", a.Auth.Require("feedback.reply"), a.replyFeedback)
	r.GET("/api/v1/admin/device-fault-reports", a.Auth.Require("fault.read"), func(c *gin.Context) { a.caseList(c, "device_fault_report", "open dispatched fixed closed") })
	r.GET("/api/v1/admin/device-fault-reports/:id/history", a.Auth.Require("fault.read"), a.faultHistory)
	r.POST("/api/v1/admin/device-fault-reports/:id/dispatch", a.Auth.Require("fault.dispatch"), a.dispatchFault)
	r.POST("/api/v1/admin/device-fault-reports/:id/resolve", a.Auth.Require("fault.resolve"), a.resolveFault)
}

// caseList 复用反馈与报修列表查询，表名和合法状态由调用方指定。
// 响应将 images_json 转为 images 数组，无图片时返回空数组。
func (a ResourceAPI) caseList(c *gin.Context, kind, statuses string) {
	q, ok := chargehttp.ReadAdminPage(c, statuses)
	if !ok {
		return
	}
	spec, known := paystore.CaseListSpecOf(kind)
	if !known {
		chargehttp.AdminResourceFailure(c, gorm.ErrRecordNotFound)
		return
	}
	items, total, err := (paystore.CaseStore{DB: a.Store.UserDB}).ListCases(c.Request.Context(), spec, q.Status, q.Page, q.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	out := Page[map[string]any]{Items: items, Page: q.Page, PageSize: q.PageSize, Total: total}
	normalizeRows(items)
	httpapi.DecimalizeRowIDsAll(items, "id", "user_id", "order_id", "assigned_to")
	for _, row := range items {
		row["images"] = row["images_json"]
		if row["images"] == nil {
			row["images"] = []string{}
		}
		delete(row, "images_json")
	}
	httpapi.OK(c, out)
}

// replyFeedback 处理用户反馈：action 为 reply 时写入回复并把状态从 pending 推到 processed，
// 为 close 时直接置为 closed。对已关闭的工单重复关闭是幂等的，重复回复或对已关闭再回复则判冲突。
func (a ResourceAPI) replyFeedback(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		Action       string `json:"action"`        // reply 回复 / close 关闭，二选一
		ReplyContent string `json:"reply_content"` // 回复正文，action 为 reply 时必填，1–2000 字
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.Action != "close" && (in.Action != "reply" || !operations.ValidText(in.ReplyContent, 2000)) {
		httpapi.BadRequest(c, "请填写 1–2000 字回复或关闭反馈")
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	var auditPending []chargehttp.AuditEntry
	store := paystore.CaseStore{DB: a.Store.UserDB}
	err := chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		row, err := store.LockCaseStatus(tx, "feedback", id)
		if err != nil {
			return err
		}
		if in.Action == "close" && row.Status == "closed" {
			return nil
		}
		if row.Status == "closed" || (in.Action == "reply" && row.Status != "pending") {
			return errConflict
		}
		v := map[string]any{"status": "closed"}
		if in.Action == "reply" {
			v = map[string]any{"status": "processed", "reply_content": in.ReplyContent, "replied_by": p.ID, "replied_at": time.Now().UTC()}
		}
		if err := store.UpdateCase(tx, "feedback", id, v); err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: in.Action, Target: "feedback", ID: id, Before: gin.H{"status": row.Status}, After: v, RequestID: ""}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"saved": true})
}

// faultHistory 分页读取报修单的流转事件、操作人、备注及可见性。
// 先校验工单存在，不存在时返回错误，避免与无历史记录混淆。
func (a ResourceAPI) faultHistory(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	q, ok := chargehttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	store := paystore.CaseStore{DB: a.Store.UserDB}
	exists, err := store.FaultExists(c.Request.Context(), a.Store.UserDB, id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if !exists {
		chargehttp.AdminResourceFailure(c, gorm.ErrRecordNotFound)
		return
	}
	items, total, err := store.ListFaultEvents(c.Request.Context(), a.Store.UserDB, id, q.Page, q.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	normalizeRows(items)
	httpapi.DecimalizeRowIDsAll(items, "event_id", "actor_id", "assigned_to")
	httpapi.OK(c, Page[map[string]any]{Items: items, Page: q.Page, PageSize: q.PageSize, Total: total})
}

// dispatchFault 校验处理人存在且持有 fault.resolve 权限，将报修置为 dispatched。
// 已有处理人时记录 reassigned，否则记录 dispatched；处理历史对用户可见，同人重复指派幂等。
func (a ResourceAPI) dispatchFault(c *gin.Context) {
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
	if in.AssignedTo == 0 || (in.Note != nil && utf8.RuneCountInString(*in.Note) > devices.MaxFaultNoteLength) {
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
	store := paystore.CaseStore{DB: a.Store.UserDB}
	err = chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		row, err := store.LockFault(tx, id)
		if err != nil {
			return err
		}
		if row.Status != "open" && row.Status != "dispatched" {
			return errConflict
		}
		if row.AssignedTo != nil && *row.AssignedTo == in.AssignedTo {
			return nil
		}
		event := "dispatched"
		if row.AssignedTo != nil {
			event = "reassigned"
		}
		if err := store.UpdateCase(tx, "device_fault_report", id, map[string]any{"status": "dispatched", "assigned_to": in.AssignedTo}); err != nil {
			return err
		}
		if err := store.AppendFaultEvent(tx, paystore.FaultEventInput{
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
func (a ResourceAPI) resolveFault(c *gin.Context) {
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
	if (in.Status != devices.FaultFixed && in.Status != devices.FaultClosed) ||
		(in.Note != nil && utf8.RuneCountInString(*in.Note) > devices.MaxFaultNoteLength) ||
		(devices.FaultNoteRequired(in.Status) && (in.Note == nil || !operations.ValidText(*in.Note, devices.MaxFaultNoteLength))) {
		httpapi.BadRequest(c, "状态无效或缺少修复说明")
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	var auditPending []chargehttp.AuditEntry
	store := paystore.CaseStore{DB: a.Store.UserDB}
	err := chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		row, err := store.LockFault(tx, id)
		if err != nil {
			return err
		}
		if row.AssignedTo == nil || *row.AssignedTo != p.ID {
			return errConflict
		}
		// 同状态重放视为幂等，不写事件也不报错；其余只允许 open→dispatched→fixed→closed。
		if err := devices.CanTransitionFault(row.Status, in.Status); err != nil {
			return errConflict
		}
		if row.Status == in.Status {
			return nil
		}
		if err := store.UpdateCase(tx, "device_fault_report", id, map[string]any{"status": in.Status, "resolved_at": time.Now().UTC()}); err != nil {
			return err
		}
		if err := store.AppendFaultEvent(tx, paystore.FaultEventInput{
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
