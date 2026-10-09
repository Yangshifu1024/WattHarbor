package central

const (
	// BillingDispatchPath 是 central 已注册的计费批次触发路径。
	BillingDispatchPath = "/api/v1/internal/billing/dispatch"
	// RefundDispatchPath 是 central 已注册的退款批次触发路径。
	RefundDispatchPath = "/api/v1/internal/refunds/dispatch"
	// AutoStopPath 是 central 已注册的自动停机扫描触发路径。
	AutoStopPath = "/api/v1/internal/charge-orders/auto-stop"
)

// BillingDispatchRequest 触发计费批次，不指定订单或金额；HTTP 请求没有正文和 Content-Type。
type BillingDispatchRequest struct{}

// BillingDispatchReply 是计费批次回执；失败也可携带已完成进度，不代表整轮成功。
type BillingDispatchReply struct {
	Completed int `json:"completed"` // 已完成计费数量，回执必须明确提供非负整数。
}

// RefundDispatchRequest 触发退款批次，不指定金额或退款编号；HTTP 请求没有正文和 Content-Type。
// 退款编号来自已保存的退款记录。
type RefundDispatchRequest struct{}

// RefundDispatchReply 是退款批次回执；失败保留进度且可不声明渠道是否启用。
type RefundDispatchReply struct {
	Enabled   *bool `json:"enabled,omitempty"` // 成功响应必须提供，禁用时进度只能为零；失败响应可缺失。
	Processed int   `json:"processed"`         // 已处理数量，回执必须明确提供非负整数。
}

// AutoStopRequest 触发停机扫描，不指定订单或命令编号；请求正文为 JSON null，Content-Type 为 application/json。
type AutoStopRequest struct{}

// AutoStopReply 是停机扫描回执；已处理数量不表示所有设备已经停电。
type AutoStopReply struct {
	Stopped int `json:"stopped"` // 已处理扫描数量，成功及未完成回执都必须明确提供非负整数。
}
