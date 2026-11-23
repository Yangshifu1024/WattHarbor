package repo

import (
	"sync"
	"time"

	contract "chargepilot/internal/contracts/gateway"
)

const snapshotTTL = 2 * time.Minute

type snapshotPort struct {
	device string
	port   uint8
}

// ProcessSnapshots 缓存成功提交的最近订单心跳，每个端口只保留一单一拍。
type ProcessSnapshots struct {
	mu       sync.Mutex
	ports    map[snapshotPort]chargeProcessRow
	prunedAt time.Time
}

func (c *ProcessSnapshots) record(rows []chargeProcessRow) {
	if c == nil || len(rows) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-snapshotTTL)
	if c.ports == nil {
		c.ports = make(map[snapshotPort]chargeProcessRow)
	}
	if now.Sub(c.prunedAt) >= time.Minute {
		for key, point := range c.ports {
			if point.TS.Before(cutoff) {
				delete(c.ports, key)
			}
		}
		c.prunedAt = now
	}
	for _, row := range rows {
		key := snapshotPort{row.DeviceID, row.PortNo}
		old, exists := c.ports[key]
		if row.TS.Before(cutoff) || exists && row.TS.Before(old.TS) || !exists && len(c.ports) >= 8192 {
			continue
		}
		c.ports[key] = row
	}
}

// Latest 只返回与订单及时间窗同时匹配的已提交样本。
func (c *ProcessSnapshots) Latest(q contract.OrderCurveRequest) (contract.CurvePoint, bool) {
	if c == nil {
		return contract.CurvePoint{}, false
	}
	c.mu.Lock()
	row, exists := c.ports[snapshotPort{q.DeviceID, q.PortNo}]
	c.mu.Unlock()
	if !exists || time.Since(row.TS) > snapshotTTL || row.ChargeOrderID != q.OrderID || row.OrderNo != q.OrderNo || row.TS.Before(q.From) || row.TS.After(q.To) {
		return contract.CurvePoint{}, false
	}
	power, meter := float64(row.PowerDeciwatts)/10, float64(row.ChargedMWh)/1000000
	out := contract.CurvePoint{TS: row.TS.UTC().Format(time.RFC3339Nano), PowerW: &power, MeterKWh: &meter}
	if row.VoltageV != nil {
		voltage := float64(*row.VoltageV)
		out.VoltageV = &voltage
	}
	if row.TemperatureC != nil {
		temp := float64(*row.TemperatureC)
		out.TemperatureC = &temp
	}
	return out, true
}
