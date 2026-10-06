package central

const (
	// BillingDispatchPath 是 central 已注册的计费批次触发路径。
	BillingDispatchPath = "/api/v1/internal/billing/dispatch"
	// RefundDispatchPath 是 central 已注册的退款批次触发路径。
	RefundDispatchPath = "/api/v1/internal/refunds/dispatch"
	// AutoStopPath 是 central 已注册的自动停机扫描触发路径。
	AutoStopPath = "/api/v1/internal/charge-orders/auto-stop"
)

// BillingDispatchRequest 表达无业务参数的计费批次触发，不携带订单、金额或请求身份。
// 原 HTTP 请求没有正文和 Content-Type；客户端不将空结构序列化为 JSON。
type BillingDispatchRequest struct{}

// BillingDispatchReply 是计费批次回执；失败也可携带已完成进度，不代表整轮成功。
type BillingDispatchReply struct {
	Completed int `json:"completed"` // 已完成计费数量，回执必须明确提供非负整数。
}

// RefundDispatchRequest 表达无业务参数的退款批次触发，不携带金额、渠道身份或重试标识。
// 原 HTTP 请求没有正文和 Content-Type；退款身份由已持久化记录决定。
type RefundDispatchRequest struct{}

// RefundDispatchReply 是退款批次回执；失败保留进度且可不声明渠道是否启用。
type RefundDispatchReply struct {
	Enabled   *bool `json:"enabled,omitempty"` // 成功时必须明确提供；未启用只能有零进度，失败原回执可缺失。
	Processed int   `json:"processed"`         // 已处理数量，回执必须明确提供非负整数。
}

// AutoStopRequest 表达无业务参数的停机扫描触发，不携带订单、截止点或设备命令身份。
// 原 HTTP 请求正文为 JSON null 并携带 application/json，由客户端保持该形状。
type AutoStopRequest struct{}

// AutoStopReply 是停机扫描回执；已处理数量不表示所有设备已经停电。
type AutoStopReply struct {
	Stopped int `json:"stopped"` // 已处理扫描数量，成功及未完成回执都必须明确提供非负整数。
}
