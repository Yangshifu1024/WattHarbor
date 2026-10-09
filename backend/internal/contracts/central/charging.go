// Package central 定义 central 内部 HTTP 接口的路径、请求和响应结构。
package central

const PaidStartsPath = "/api/v1/internal/charge-orders/paid-starts"

// PaidStartOrder 是已付清且待启动的订单；ID 是内部扫描游标，保持 uint64 整数。
type PaidStartOrder struct {
	ID      uint64 `json:"id"`       // 已保存的 ID，保持 uint64 精度
	OrderNo string `json:"order_no"` // 业务订单号
}

// PaidStartsReply 按主键递增返回；无待启动订单时 Items 为 []，不能为 null。
type PaidStartsReply struct {
	Items []PaidStartOrder `json:"items"` // 按原顺序排列的记录；无记录时返回空数组
}
