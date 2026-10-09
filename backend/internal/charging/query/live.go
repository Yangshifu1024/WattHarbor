// Package query 负责充电只读组合与实时估算，最终结算仍由 settlement 执行。
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
	// ChargingEvidence 按设备、端口与开始时间批量查询心跳，按请求顺序返回各订单的分页数据。
	ChargingEvidence(ctx context.Context, queries []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error)
}

// SnapshotReader 由订单对应模块实现提供冻结 JSON，缺行不能用当前价格替换。
type SnapshotReader interface {
	// Snapshots 按订单 ID 批量读取冻结 JSON，不用当前方案补齐缺失快照。
	Snapshots(ctx context.Context, ids []uint64) (map[uint64][]byte, error)
}

// PurchaseReader 由卡对应模块实现提供累计购买记录，非卡订单不进入映射。
type PurchaseReader interface {
	// Purchases 批量读取 ids，返回累计购买映射，不扣款或续购。
	Purchases(ctx context.Context, ids []uint64) (map[uint64]charging.OrderCardPurchase, error)
}

// LiveService 按最后观测量及订单冻结方案估算费用，只向公开查询结果回填。
type LiveService struct {
	Gateway EvidenceReader   // cmd 注入的网关批量取证能力
	Orders  SnapshotReader   // cmd 注入的订单快照读取
	Cards   PurchaseReader   // cmd 注入的累计刷卡购买读取
	Now     func() time.Time // 测试可固定观测上界；未注入时使用当前 UTC
}

// now 返回 s 的观测时刻或当前 UTC，不执行 I/O。
func (s LiveService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Fill 为充电中订单填充最新观测与估算费用，全部操作最多四秒。
// 每 20 单批量取证，成功取证后分别批量读取价格快照与刷卡购买记录。
// 一批失败不影响其他批，请求取消后停止后续查询；证据不全时显示不可估算。
// 只更新展示结果，不写入最终结算，返回时没有后台任务。
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
	// observed 将有效心跳计量关联到输入订单行，供充电中展示回填；它是临时查询结果，不写入结束计量或结算金额。
	type observed struct {
		index int                  // 本次 rows 下标，保持分页结果排序
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

// priceLive 用冻结方案与累计购买信息计算截至最后观测时刻的费用。
// 方案无效、购买信息冲突或计量不足时只提示不可估算，不读取当前价格或写入结算。
func priceLive(view *charging.LiveMeterView, meter charging.ActualMeter, raw []byte, purchase charging.OrderCardPurchase, isCard bool) {
	var frozen struct {
		Rule  charging.Rule   `json:"rule"`  // 完整计费规则
		Offer *charging.Offer `json:"offer"` // 购买套餐，缺失时不可估算
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

// Read 用订单、设备、端口与开始时间查询最新计量，断线期间不继续累计计费时长。
func (s LiveService) Read(ctx context.Context, id uint64, device string, port uint8, start time.Time) (*charging.LiveMeterView, error) {
	rows := []order.OrderView{{OrderID: id, DeviceID: device, PortNo: port, Status: "charging", StartedAt: &start}}
	s.Fill(ctx, rows)
	if rows[0].LiveUnavailable != "" {
		return rows[0].Live, errors.New(rows[0].LiveUnavailable)
	}
	return rows[0].Live, nil
}
