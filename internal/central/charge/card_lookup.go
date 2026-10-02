package charge

import (
	"context"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
)

// ScanPortLookup 将 scan 家族的 ScanAPI 查询适配为 money.PortLookup，
// 供 card.CardAPI 注入；端口发现的实现与协议仍归属 scan 家族（payment 包）。
type ScanPortLookup struct {
	API payment.ScanAPI
}

func (a ScanPortLookup) LookupPort(ctx context.Context, portID string) (money.PortRef, int) {
	result, status := a.API.Lookup(ctx, portID)
	ref := money.PortRef{
		Found:        status == http.StatusOK && result.Port != nil,
		Kind:         result.Kind,
		DeviceID:     result.DeviceID,
		StationID:    result.StationID,
		DeviceStatus: result.DeviceStatus,
	}
	if result.Port != nil {
		ref.PortID = result.Port.PortID
		ref.PortNo = result.Port.PortNo
		ref.Online = result.Port.Online
		ref.Available = result.Port.Available
	}
	return ref, status
}
