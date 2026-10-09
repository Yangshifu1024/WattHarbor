package order

import (
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/persist"
	"context"
	"errors"
	"gorm.io/gorm"
	"strings"
)

// FeeFacts 保存已结算的电费、服务费和总额，单位分。
type FeeFacts struct {
	OrderID        uint64 // 充电订单编号
	ElectricCents  int64  // 已确认电费，分
	ServiceCents   int64  // 已确认服务费，分
	TotalCents     int64  // 已确认总费，分
	ShortfallCents int64  // 已确认资金缺口，分
}

// FeeReader 由结算对应模块实现批量读取回执，订单不引用结算模型或实现。
type FeeReader interface {
	// Fees 批量读取 ids 订单，返回有效已确认金额；缺失、无法解码回执不在映射中。
	Fees(ctx context.Context, ids []uint64) (map[uint64]FeeFacts, error)
}

// QueryError 标记订单、费用读取失败，HTTP 只映射错误文案，不拼装费用查询。
type QueryError struct {
	Stage string /* order 或 fee */
	Cause error  /* 读取错误 */
}

// QueryInputError 保存订单号或筛选参数的校验错误，不表示订单是否存在。
type QueryInputError struct {
	Message string /* 公开输入错误 */
}

// Error 返回 e 的校验错误文案。
func (e *QueryInputError) Error() string { return e.Message }

// Error 返回 e 的读取失败原因。
func (e *QueryError) Error() string { return e.Cause.Error() }

// Unwrap 返回 e 的读取错误，供缺行及取消判定。
func (e *QueryError) Unwrap() error { return e.Cause }

// QueryService 负责用户订单隔离、历史批量费用及后台只读组合；不持有网关 URL 或结算实现。
type QueryService struct {
	DB   *gorm.DB  /* 所属订单连接，由进程管理 */
	Fees FeeReader /* cmd 注入的结算回执批量读取 */
}

// Resolve 按 userID 和 raw 业务单号返回未删除订单或输入、缺失、读取错误。
// 越权与缺失共用 gorm.ErrRecordNotFound，避免通过公开查询泄漏他人订单。
func (s QueryService) Resolve(ctx context.Context, userID uint64, raw string) (ChargeOrderRecord, error) {
	no := strings.TrimSpace(raw)
	if no == "" || len(no) > 64 {
		return ChargeOrderRecord{}, &QueryInputError{"订单号无效"}
	}
	return (QueryRepo{DB: s.DB}).Owned(ctx, userID, no)
}

// Ongoing 读取 userID 最新 paid/charging 订单，返回信息，缺失时返回错误。
// 缺失由适配层输出空状态，不把其他读取故障当作无充电订单。
func (s QueryService) Ongoing(ctx context.Context, userID uint64) (ChargeOrderRecord, error) {
	return (QueryRepo{DB: s.DB}).Ongoing(ctx, userID)
}

// History 按 userID 和 q 已校验分页读取订单，再一次批量加载有效费用回执。
// 返回分页结果或订单、费用阶段错误；未确认费用保持 nil，不将缺失当作零，不逐行访问外部服务。
func (s QueryService) History(ctx context.Context, userID uint64, q persist.PageQuery) (persist.Page[UserOrderHistoryRow], error) {
	out, err := ListUserOrderHistory(ctx, s.DB, userID, q.Status, q.Page, q.PageSize)
	if err != nil {
		return out, &QueryError{"order", err}
	}
	if len(out.Items) == 0 {
		return out, nil
	}
	ids := make([]uint64, 0, len(out.Items))
	for _, row := range out.Items {
		ids = append(ids, row.OrderID)
	}
	fees, err := s.Fees.Fees(ctx, ids)
	if err != nil {
		return out, &QueryError{"fee", err}
	}
	for i := range out.Items {
		if f, ok := fees[out.Items[i].OrderID]; ok {
			out.Items[i].ElectricCents = &f.ElectricCents
			out.Items[i].ServiceCents = &f.ServiceCents
			out.Items[i].TotalCents = &f.TotalCents
		}
	}
	return out, nil
}

// List 按实时 order.read 权限与查询条件读取后台订单页。
// 沿用全局订单权限和显式站点筛选；套餐名称来自冻结快照，不读取当前价格。
func (s QueryService) List(ctx context.Context, actor operations.Profile, q OrderQuery) (persist.Page[OrderView], error) {
	if !actor.HasPermission("order.read") {
		return persist.Page[OrderView]{}, operations.ErrForbidden
	}
	return ListOrders(ctx, s.DB, q)
}

// Detail 按实时 order.read 权限读取后台订单详情与冻结套餐。
// 实时计量由 charging/query 补充，当前方案不能替换购买快照。
func (s QueryService) Detail(ctx context.Context, actor operations.Profile, id uint64) (OrderView, error) {
	if !actor.HasPermission("order.read") {
		return OrderView{}, operations.ErrForbidden
	}
	row, err := FindOrderView(ctx, s.DB, id)
	if err != nil {
		return row, err
	}
	rows := []OrderView{row}
	if err := FillOrderStations(ctx, s.DB, rows); err != nil {
		return OrderView{}, err
	}
	row = rows[0]
	selected, err := OrderPackageSnapshot(ctx, s.DB, id)
	if err != nil {
		return OrderView{}, err
	}
	row.SelectedPackage = selected
	row.SelectedSchemeName = charging.SelectedSchemeName(selected)
	row.SelectedPackageName = charging.SelectedPackageName(selected)
	return row, nil
}

// Timeline 按实时 order.read 权限确认订单存在后读取事件。
// 没有事件返回空数组，没有订单返回错误，两者不能混淆。
func (s QueryService) Timeline(ctx context.Context, actor operations.Profile, id uint64) ([]OrderTimelineRow, error) {
	if !actor.HasPermission("order.read") {
		return nil, operations.ErrForbidden
	}
	if err := OrderExists(ctx, s.DB, id); err != nil {
		return nil, err
	}
	return ListOrderEvents(ctx, s.DB, id)
}

// IsFeeQueryError 核对 err 是否为批量费用读取失败，返回检查结果。
func IsFeeQueryError(err error) bool {
	var e *QueryError
	return errors.As(err, &e) && e.Stage == "fee"
}

// ProcessIdentity 校验实时 order.read 权限，再读取订单 ID、业务单号、设备编号与端口号。
func (s QueryService) ProcessIdentity(ctx context.Context, actor operations.Profile, id uint64) (ProcessIdentity, error) {
	if !actor.HasPermission("order.read") {
		return ProcessIdentity{}, operations.ErrForbidden
	}
	return findProcessIdentity(ctx, s.DB, id)
}
