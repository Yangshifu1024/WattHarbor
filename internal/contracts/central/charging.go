// Package central 定义 central 内部 HTTP 接口的请求与响应契约。
package central

const PaidStartsPath = "/api/v1/internal/charge-orders/paid-starts"

// PaidStartOrder 是已付清且待启动的订单；ID 是内部扫描游标，保持 uint64 整数。
type PaidStartOrder struct {
	ID      uint64 `json:"id"`       // 原持久主键，保持uint64整数精度
	OrderNo string `json:"order_no"` // 原业务订单号
}

// PaidStartsReply 按主键递增返回；无待启动订单时 Items 为 []，不能为 null。
type PaidStartsReply struct {
	Items []PaidStartOrder `json:"items"` // 原有序记录集合；空集合保持数组
}
