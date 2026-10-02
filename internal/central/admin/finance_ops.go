package admin

import (
	"context"
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	paystore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
	"time"
)

// financeStore 返回财务查询的读写入口，持有结算库与 central_db 两条连接。
func (a ResourceAPI) financeStore() paystore.AdminFinanceStore {
	return paystore.AdminFinanceStore{AdminDB: a.Store.AdminDB, BillingDB: a.Store.BillingDB}
}

// Withdraws 分页返回提现单，支持按状态与关键字（单号或参与方编码）过滤，按 ID 倒序。
func (s ResourceStore) Withdraws(ctx context.Context, page PageQuery) (Page[paystore.WithdrawRow], error) {
	return (paystore.AdminFinanceStore{BillingDB: s.BillingDB}).ListWithdraws(ctx, page)
}

// createWithdraw 新建提现申请：余额重算、参与方与模板有效性校验归属 settlement 家族，
// 相同请求号重放直接返回原单据，不重复占用余额。本 handler 只做输入校验与审计编排。
func (a ResourceAPI) createWithdraw(c *gin.Context) {
	var in struct {
		RequestID   string  `json:"request_id"`   // 客户端请求号,必须是 UUID,用于幂等
		PartyID     uint64  `json:"party_id"`     // 分账参与方 ID,必须存在且其分账模板仍为 active
		AmountCents int64   `json:"amount_cents"` // 申请金额,单位分,必须大于 0 且不超过可提现余额
		Note        *string `json:"note"`         // 备注(可空),最长 255 字符
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.RequestID == "" || !operations.ValidRequestID(in.RequestID) || in.PartyID == 0 || in.AmountCents <= 0 || (in.Note != nil && utf8Count(*in.Note) > 255) {
		httpapi.BadRequest(c, "请填写 UUID 请求号、有效参与方、正数金额和备注")
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	var no string
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		no, err = (paystore.WithdrawStore{}).Create(c.Request.Context(), tx, paystore.WithdrawInput{
			RequestID: in.RequestID, PartyID: in.PartyID, AmountCents: in.AmountCents, Note: in.Note,
		})
		if err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, profile, "create", "withdraw", in.PartyID, nil,
			gin.H{"withdraw_no": no, "party_id": in.PartyID, "amount_cents": in.AmountCents}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no, "status": "pending"})
}

// decideWithdraw 按 approve 值审核提现，驳回须填写理由；状态机与余额校验归属 settlement 家族。
func (a ResourceAPI) decideWithdraw(c *gin.Context) {
	no := strings.TrimSpace(c.Param("withdraw_no"))
	var in struct {
		Approve *bool   `json:"approve"` // 是否通过(可空);为 true 表示通过,为 false 时必须同时给 reason
		Reason  *string `json:"reason"`  // 审核意见(可空),驳回时必填且不能是空白,最长 255 字符
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if no == "" || len(no) > 64 || (in.Approve == nil && in.Reason == nil) {
		httpapi.BadRequest(c, "请提供提现单号和审核决定")
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		action, rowID, err := (paystore.WithdrawStore{}).Decide(c.Request.Context(), tx, no, profile.ID, paystore.WithdrawDecision{Approve: in.Approve, Reason: in.Reason})
		if err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, profile, action, "withdraw", rowID, nil, gin.H{"withdraw_no": no}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no})
}

// payWithdraw 登记打款结果：状态推进与余额校验归属 settlement 家族；
// 已打款的单据重复调用只回报状态，不会重复出款。
func (a ResourceAPI) payWithdraw(c *gin.Context) {
	no := strings.TrimSpace(c.Param("withdraw_no"))
	var in struct {
		Note *string `json:"note"` // 打款备注(可空),最长 255 字符
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if no == "" || len(no) > 64 {
		httpapi.BadRequest(c, "提现单号无效")
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	alreadyPaid := false
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		rowID, paid, err := (paystore.WithdrawStore{}).Pay(c.Request.Context(), tx, no, in.Note)
		if err != nil {
			return err
		}
		if paid {
			alreadyPaid = true
			return nil
		}
		return chargehttp.AdminResourceAudit(tx, profile, "pay", "withdraw", rowID, nil, gin.H{"withdraw_no": no}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no, "status": "paid", "already_paid": alreadyPaid})
}

// runReconcile 触发一次对账：校验对账类型、日期和上报条数，把渠道数据按参照号聚合，
// 再与内部台账逐笔比对，差异原样落库交给运营判断，不做自动抹平。
func (a ResourceAPI) runReconcile(c *gin.Context) {
	var in struct {
		ReconcileType string            `json:"reconcile_type"`  // 对账类型:wechat_refund / wechat_pay / split / withdraw
		Date          string            `json:"date"`            // 对账日期,格式 YYYY-MM-DD
		Channel       []ReconcileAmount `json:"channel_amounts"` // 渠道上报明细,1-2000 条,按参照号累加后与内部比对
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !chargehttp.AdminOneOf(in.ReconcileType, "wechat_refund wechat_pay split withdraw") {
		httpapi.BadRequest(c, "对账类型无效")
		return
	}
	date, err := time.Parse("2006-01-02", in.Date)
	if err != nil {
		httpapi.BadRequest(c, "对账日期须为 YYYY-MM-DD")
		return
	}
	if len(in.Channel) == 0 || len(in.Channel) > 2000 {
		httpapi.BadRequest(c, "请提供 1–2000 条渠道对账数据")
		return
	}
	channel := map[string]int64{}
	for _, row := range in.Channel {
		if !operations.ValidText(row.Ref, 64) {
			httpapi.BadRequest(c, "渠道流水号无效")
			return
		}
		channel[row.Ref] += row.AmountCents
	}
	created, err := a.reconcileType(c, in.ReconcileType, date, channel)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, created)
}

// ReconcileAmount 是渠道上报的一行：一个参照号对应一个金额（单位分）。
type ReconcileAmount struct {
	Ref         string `json:"ref"`          // 渠道流水号,最长 64 字符
	AmountCents int64  `json:"amount_cents"` // 渠道金额,单位分;同一流水号出现多次会累加
}

// reconcileType 取对应台账、调用领域层的逐笔比对规则，再把结果落库。
// 编排而已：怎么算差异是 money.DiffReconcile 的事，台账怎么取是 database 的事。
func (a ResourceAPI) reconcileType(c *gin.Context, kind string, date time.Time, channel map[string]int64) (paystore.ReconcileRow, error) {
	row := paystore.ReconcileRow{ReconcileType: kind, ReconcileDate: date.Format("2006-01-02"), Diffs: []paystore.ReconcileDiff{}}
	ctx, userDB, billingDB, adminDB := c.Request.Context(), a.Store.UserDB, a.Store.BillingDB, a.Store.AdminDB
	var (
		internal map[string]int64
		err      error
	)
	switch kind {
	case "wechat_refund":
		internal, err = paystore.InternalRefunds(ctx, userDB, date)
	case "wechat_pay":
		internal, err = paystore.InternalPayments(ctx, userDB, date)
	case "split":
		internal, err = paystore.InternalSplits(ctx, billingDB, date)
	default:
		internal, err = paystore.InternalWithdraws(ctx, billingDB, date)
	}
	if err != nil {
		return row, err
	}
	result := money.DiffReconcile(internal, channel)
	row.InternalCount, row.ExternalCount = result.InternalCount, result.ExternalCount
	row.DiffCount, row.DiffCents = result.DiffCount, result.DiffCents
	row.InternalCents, row.ExternalCents = result.InternalCents, result.ExternalCents
	row.Diffs = paystore.ToReconcileDiffs(result.Diffs)
	if err := paystore.SaveReconcileResult(ctx, adminDB, kind, row.ReconcileDate, result); err != nil {
		return row, err
	}
	return row, nil
}

// resolveReconcile 标记一条对账记录是否已处理，行锁下按 ID 更新，重复调用结果一致。
func (a ResourceAPI) resolveReconcile(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	// finance_reconcile_log 没有备注列，早先这里声明的 note 字段只会被静默丢弃，
	// 让调用方以为处理说明已保存。已移除：需要备注时应先加列。
	var in struct {
		Resolved bool `json:"resolved"` // 是否标记为已处理
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		return a.financeStore().SetReconcileResolved(tx, id, in.Resolved)
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "resolved": in.Resolved})
}

// validUUID 判断字符串是不是合法 UUID，用于提现、导出等接口的请求号幂等校验。
// utf8Count 按 Unicode 字符而不是字节统计长度，用于中文字段的 255 之类上限校验。
func utf8Count(s string) int { return len([]rune(s)) }

// registerFinanceOps 注册分账、提现和对账路由，分别校验读取和操作权限。
func (a ResourceAPI) registerFinanceOps(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/settlements", a.Auth.Require("finance.read"), a.settlements)
	r.GET("/api/v1/admin/billing/withdraws", a.Auth.Require("finance.read"), a.withdraws)
	r.POST("/api/v1/admin/billing/withdraws", a.Auth.Require("finance.withdraw.create"), a.createWithdraw)
	r.POST("/api/v1/admin/billing/withdraws/:withdraw_no/decide", a.Auth.Require("finance.withdraw.review"), a.decideWithdraw)
	r.POST("/api/v1/admin/billing/withdraws/:withdraw_no/pay", a.Auth.Require("finance.withdraw.review"), a.payWithdraw)
	r.GET("/api/v1/admin/billing/reconciles", a.Auth.Require("finance.read"), a.reconciles)
	r.POST("/api/v1/admin/billing/reconciles", a.Auth.Require("finance.read"), a.runReconcile)
	r.POST("/api/v1/admin/billing/reconciles/:id/resolve", a.Auth.Require("finance.read"), a.resolveReconcile)
}

// settlements 分页查询分账汇总及各参与方的金额和比例。
// 查询列显式映射到 Go 字段，保持聚合结果与模型一致。
func (a ResourceAPI) settlements(c *gin.Context) {
	page, ok := chargehttp.ReadAdminPage(c, "pending confirmed paid failed")
	if !ok {
		return
	}
	out, err := a.financeStore().ListSettlements(c.Request.Context(), page)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// withdraws 分页返回提现单，状态与关键字过滤交给 ResourceStore，避免两个接口各写一套。
func (a ResourceAPI) withdraws(c *gin.Context) {
	page, ok := chargehttp.ReadAdminPage(c, "pending approved rejected paid failed")
	if !ok {
		return
	}
	out, err := a.Store.Withdraws(c.Request.Context(), page)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// reconciles 按类型和处理状态分页查询对账记录，将 diffs_json 转为文本后逐条解码。
func (a ResourceAPI) reconciles(c *gin.Context) {
	page, ok := chargehttp.ReadAdminPage(c, "wechat_refund wechat_pay split withdraw")
	if !ok {
		return
	}
	// 只有精确的 true / false 才当筛选条件，其他取值忽略，避免把乱值静默变成"全查"。
	var resolved *bool
	if raw := c.Query("resolved"); raw == "true" || raw == "false" {
		value := raw == "true"
		resolved = &value
	}
	out, err := a.financeStore().ListReconciles(c.Request.Context(), page.Status, resolved, page)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// registerWebhookDelivery 注册 Webhook 投递日志查询与手动重发路由，让运营能救回一个
// 因外部原因而一直投不出去的订阅，重发复用 webhook.create 权限。
func (a ResourceAPI) registerWebhookDelivery(r *gin.Engine) {
	r.GET("/api/v1/admin/webhooks/:id/deliveries", a.Auth.Require("webhook.read"), a.webhookDeliveries)
	r.POST("/api/v1/admin/webhooks/:id/deliveries/:event_id/retry", a.Auth.Require("webhook.create"), a.retryWebhookDelivery)
}

// webhookDeliveries 分页返回某个订阅的投递日志，支持按事件 ID 或事件类型关键字过滤，按 ID 倒序。
func (a ResourceAPI) webhookDeliveries(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	page, ok := chargehttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	out, err := a.financeStore().ListWebhookDeliveries(c.Request.Context(), id, page)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// retryWebhookDelivery 从原始事件体生成新事件，复用正式签名投递链路。
// 使用新的事件 ID；订阅停用时拒绝重发。
func (a ResourceAPI) retryWebhookDelivery(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	eventID := strings.TrimSpace(c.Param("event_id"))
	if eventID == "" || len(eventID) > 64 {
		httpapi.BadRequest(c, "事件 ID 无效")
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	subscription, err := a.financeStore().FindWebhookSubscription(c.Request.Context(), id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if !subscription.Enabled {
		httpapi.Write(c, 409, 2009, "订阅已停用，无法重发", nil)
		return
	}
	delivery, err := a.financeStore().FindWebhookDelivery(c.Request.Context(), id, eventID)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	// 经由事件流重新投递，是为了让签名、SSRF 检查和投递日志都留在同一条投递
	// 链路上。
	envelope := map[string]any{
		"event_id": "retry-" + uuid.NewString(), "event_type": delivery.EventType, "source": "admin_manual_retry",
		"occurred_at": time.Now().UTC(), "data": json.RawMessage(delivery.RequestBody),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := a.financeStore().EnqueueAdminEvent(tx, "charge_events_stream", envelope["event_id"].(string), payload); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, profile, "retry", "webhook", id, nil, gin.H{"event_id": eventID, "subscription_id": id}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	}); err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"event_id": eventID, "requeued": true})
}
