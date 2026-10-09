// Package query 拥有充电只读组合与实时估算，最终结算仍由 settlement 执行。
package query

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/order"
	"chargepilot/internal/protocol"
)

// EvidenceReader 是实时估算需要的有界网关取证能力，不携带 URL、令牌或通用传输。
type EvidenceReader interface {
	// ChargingEvidence 在 ctx 下按 queries 原设备／端口／开始时刻批量取证，返回原序页或调用错误，无业务写入。
	ChargingEvidence(ctx context.Context, queries []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
}

// SnapshotReader 由订单所属实现提供原冻结 JSON，缺行不能用当前价格替换。
type SnapshotReader interface {
	// Snapshots 在 ctx 下批量读取 ids 原订单，返回身份到原 JSON 的映射或存储错误，无写入。
	Snapshots(ctx context.Context, ids []uint64) (map[uint64][]byte, error)
}

// PurchaseReader 由卡所属实现提供累计购买事实，非卡订单不进入映射。
type PurchaseReader interface {
	// Purchases 在 ctx 下批量读取 ids，返回累计购买映射或存储错误，不扣款或续购。
	Purchases(ctx context.Context, ids []uint64) (map[uint64]charging.OrderCardPurchase, error)
}

// LiveService 按最后观测量及订单冻结方案估算费用，只向公开读模型回填。
type LiveService struct {
	Gateway EvidenceReader   // cmd 注入的网关批量取证能力
	Orders  SnapshotReader   // cmd 注入的原订单快照读取
	Cards   PurchaseReader   // cmd 注入的累计刷卡购买读取
	Now     func() time.Time // 测试可固定观测上界；未注入时使用当前 UTC
}

// now 返回 s 的观测时刻或当前 UTC，不执行 IO。
func (s LiveService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Fill 在 ctx 的取消范围及最多4秒期限内，为 rows 中 charging 且已开始的订单原位填充实时视图。
// 每20单一次取证，全部成功取证的计价数据各一次批量读取；不逐订单外部调用或写库。
// 独立证据批失败保留其它批结果，取消后不再发起后续调用；失败或无证据保留原公开不可读提示。
// 缺失计价口径及不完整证据只隐藏估算费用，不将实时量写入最终结算字段；函数返回时无后台任务。
func (s LiveService) Fill(ctx context.Context, rows []order.OrderView) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	indices := []int{}
	for i := range rows {
		if rows[i].Status == "charging" && rows[i].StartedAt != nil {
			rows[i].Live = nil
			rows[i].LiveUnavailable = "设备计量暂不可读取"
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 || s.Gateway == nil || s.Orders == nil || s.Cards == nil {
		return
	}
	// observed 将有效心跳计量关联到输入订单行，供充电中展示回填；它是临时读模型，不写入结束计量或结算金额。
	type observed struct {
		index int                  // 本次 rows 下标，保持原页排序
		meter charging.ActualMeter // 截至最后心跳的实时估算计量，非最终结算依据
	}
	valid := []observed{}
	ids := []uint64{}
	now := s.now()
	for begin := 0; begin < len(indices); begin += gatewaycontract.MaxEvidenceBatch {
		if ctx.Err() != nil {
			break
		}
		batch := indices[begin:min(begin+gatewaycontract.MaxEvidenceBatch, len(indices))]
		queries := make([]gatewaycontract.EvidenceQueryRequest, len(batch))
		for j, i := range batch {
			queries[j] = gatewaycontract.EvidenceQueryRequest{DeviceID: rows[i].DeviceID, PortNo: rows[i].PortNo, StartedAt: rows[i].StartedAt.UTC()}
		}
		pages, err := s.Gateway.ChargingEvidence(ctx, queries)
		if err != nil || len(pages) != len(batch) {
			continue
		}
		for j, i := range batch {
			page, q := pages[j], queries[j]
			if page.DeviceID != q.DeviceID || page.PortNo != q.PortNo || page.Samples == nil || (!page.Complete && page.NextAfterID <= q.AfterID) {
				continue
			}
			samples := make([]protocol.Event, len(page.Samples))
			for n, e := range page.Samples {
				samples[n] = protocol.Event{Type: e.Type, DeviceID: e.DeviceID, ReceivedAt: e.ReceivedAt, ChargingPorts: e.ChargingPorts}
			}
			view, meter, ok := liveMeterEvidence(q.DeviceID, q.PortNo, q.StartedAt, samples, now)
			if !ok {
				continue
			}
			rows[i].Live, rows[i].LiveUnavailable = view, ""
			if !page.Complete {
				view.FeeUnavailable = "计量证据超过范围，费用待结算"
				continue
			}
			valid = append(valid, observed{i, meter})
			ids = append(ids, rows[i].OrderID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if ctx.Err() != nil {
		for _, v := range valid {
			rows[v.index].LiveUnavailable = "设备计量暂不可读取"
		}
		return
	}
	snapshots, err := s.Orders.Snapshots(ctx, ids)
	if err != nil {
		for _, v := range valid {
			rows[v.index].LiveUnavailable = "设备计量暂不可读取"
		}
		return
	}
	if ctx.Err() != nil {
		for _, v := range valid {
			rows[v.index].LiveUnavailable = "设备计量暂不可读取"
		}
		return
	}
	purchases, err := s.Cards.Purchases(ctx, ids)
	if err != nil {
		for _, v := range valid {
			rows[v.index].LiveUnavailable = "设备计量暂不可读取"
		}
		return
	}
	for _, v := range valid {
		purchase, isCard := purchases[rows[v.index].OrderID]
		priceLive(rows[v.index].Live, v.meter, snapshots[rows[v.index].OrderID], purchase, isCard)
	}
}

// priceLive 按 raw 原冻结口径及 purchase 原累计购买／isCard 原卡关联为 view 回填 meter 截至观测时刻的估算费用。
// 缺失／非法口径、购买冲突及不足计量只回填不可估算文案，不写库或读取当前模板。
func priceLive(view *charging.LiveMeterView, meter charging.ActualMeter, raw []byte, purchase charging.OrderCardPurchase, isCard bool) {
	var frozen struct {
		Rule  charging.Rule   `json:"rule"`  // 原完整计费规则
		Offer *charging.Offer `json:"offer"` // 原购买套餐，缺失时不可估算
	}
	if json.Unmarshal(raw, &frozen) != nil || frozen.Offer == nil {
		view.FeeUnavailable = "缺少订单计费快照"
		return
	}
	if isCard {
		base := frozen.Offer.PriceCents
		if base <= 0 || purchase.PaidCents <= 0 || purchase.PaidCents%base != 0 || purchase.PaidCents/base > 65535 {
			view.FeeUnavailable = "刷卡购买记录待确认"
			return
		}
		frozen.Offer.PurchaseCount = uint16(purchase.PaidCents / base)
		frozen.Offer.PriceCents = purchase.PaidCents
		frozen.Offer.DurationMinutes = purchase.PurchasedMinutes
	}
	fee, err := charging.PriceOfferActual(frozen.Rule, frozen.Offer, meter)
	if err != nil {
		view.FeeUnavailable = "计量不足以计算当前费用"
		return
	}
	view.Fee = &charging.LiveFee{ElectricCents: fee.ElectricCents, ServiceCents: fee.ServiceCents, TotalCents: fee.TotalCents}
}

// Read 在 ctx 下为 id/device/port/start 原单读取实时视图，返回观测视图或不可读错误，无写入。
// 批量入口与单单入口共用最后观测和冻结计价规则，断线时间不会累加到计费时长。
func (s LiveService) Read(ctx context.Context, id uint64, device string, port uint8, start time.Time) (*charging.LiveMeterView, error) {
	rows := []order.OrderView{{OrderID: id, DeviceID: device, PortNo: port, Status: "charging", StartedAt: &start}}
	s.Fill(ctx, rows)
	if rows[0].LiveUnavailable != "" {
		return rows[0].Live, errors.New(rows[0].LiveUnavailable)
	}
	return rows[0].Live, nil
}
