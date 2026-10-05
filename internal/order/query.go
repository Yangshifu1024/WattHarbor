package order

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"strings"
)

// FeeFacts 是所属费用回执的已确认金额，不代表实时计量估算。
type FeeFacts struct {
	OrderID        uint64 // 原充电订单身份
	ElectricCents  int64  // 已确认电费，分
	ServiceCents   int64  // 已确认服务费，分
	TotalCents     int64  // 已确认总费，分
	ShortfallCents int64  // 已确认资金缺口，分
}

// FeeReader 由结算所属实现批量读取原回执，订单不引用结算模型或实现。
type FeeReader interface {
	// Fees 在 ctx 下批量读取 ids 原订单，返回有效已确认金额或存储错误；缺失／无法解码回执不在映射中。
	Fees(ctx context.Context, ids []uint64) (map[uint64]FeeFacts, error)
}

// QueryError 标记订单／费用读取失败，HTTP 只映射原错误文案，不拼装费用查询。
type QueryError struct {
	Stage string /* order 或 fee */
	Cause error  /* 原读取错误 */
}

// QueryInputError 保留原订单引用与筛选的公开准入文案，不含存在性事实。
type QueryInputError struct {
	Message string /* 原公开输入错误 */
}

// Error 返回 e 的原准入文案，无 IO。
func (e *QueryInputError) Error() string { return e.Message }

// Error 返回 e 的原读取原因，无 IO。
func (e *QueryError) Error() string { return e.Cause.Error() }

// Unwrap 返回 e 的原读取错误，供缺行及取消判定，无副作用。
func (e *QueryError) Unwrap() error { return e.Cause }

// QueryService 拥有用户订单隔离、历史批量费用及后台只读组合；不持有网关 URL 或结算实现。
type QueryService struct {
	DB   *gorm.DB  /* 所属订单连接，由进程管理 */
	Fees FeeReader /* cmd 注入的结算回执批量读取 */
}

// Resolve 在 ctx 下按 userID 和 raw 原业务号返回未删除订单或输入／缺失／读取错误，无写入。
// 越权与缺失共用 gorm.ErrRecordNotFound，避免通过公开查询泄漏他人订单。
func (s QueryService) Resolve(ctx context.Context, userID uint64, raw string) (ChargeOrderRecord, error) {
	no := strings.TrimSpace(raw)
	if no == "" || len(no) > 64 {
		return ChargeOrderRecord{}, &QueryInputError{"订单号无效"}
	}
	return (QueryRepo{DB: s.DB}).Owned(ctx, userID, no)
}

// Ongoing 在 ctx 下读取 userID 最新 paid/charging 订单，返回原事实或缺失／读取错误，无写入。
// 缺失由适配层输出原空状态，不把其他读取故障当作无充电订单。
func (s QueryService) Ongoing(ctx context.Context, userID uint64) (ChargeOrderRecord, error) {
	return (QueryRepo{DB: s.DB}).Ongoing(ctx, userID)
}

// History 在 ctx 下按 userID 和 q 已校验分页读取订单，再一次批量加载有效费用回执。
// 返回原页或订单／费用阶段错误；未确认费用保持 nil，不将缺失当作零，不逐行访问外部服务。
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

// List 在 ctx 下按 actor 原 order.read 权限与 q 已校验条件读取后台原订单页，返回原页或禁止／读取错误。
// 沿用原全局订单权限和显式站点筛选；批量站点、套餐来自原只读快照，不读取当前价格。
func (s QueryService) List(ctx context.Context, actor operations.Profile, q OrderQuery) (persist.Page[OrderView], error) {
	if !actor.HasPermission("order.read") {
		return persist.Page[OrderView]{}, operations.ErrForbidden
	}
	return ListOrders(ctx, s.DB, q)
}

// Detail 在 ctx 下按 actor 原 order.read 权限读取 id 后台档案及冻结套餐，返回原投影或禁止／缺失／读取错误。
// 实时计量由 charging/query 后续组合，不把当前方案覆盖冻结快照，无业务写入。
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

// Timeline 在 ctx 下按 actor 原 order.read 权限核对 id 存在再读取事件，返回原有序数组或禁止／缺失／读取错误。
// 空事件为数组，不能把缺失订单当成空时间线，不执行外部调用或写入。
func (s QueryService) Timeline(ctx context.Context, actor operations.Profile, id uint64) ([]OrderTimelineRow, error) {
	if !actor.HasPermission("order.read") {
		return nil, operations.ErrForbidden
	}
	if err := OrderExists(ctx, s.DB, id); err != nil {
		return nil, err
	}
	return ListOrderEvents(ctx, s.DB, id)
}

// IsFeeQueryError 核对 err 是否为批量费用读取失败，返回事实，无 IO。
func IsFeeQueryError(err error) bool {
	var e *QueryError
	return errors.As(err, &e) && e.Stage == "fee"
}
