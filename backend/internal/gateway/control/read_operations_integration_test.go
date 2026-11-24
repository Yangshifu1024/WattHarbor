//go:build integration

package control

import (
	"context"
	"errors"
	"testing"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	gatewayrepo "chargepilot/internal/gateway/repo"
)

// TestGatewayReadOperationsHonorCanceledContext 验证订单采样读取返回真实取消错误，不能把取消当作空数据成功。
func TestGatewayReadOperationsHonorCanceledContext(t *testing.T) {
	db := gatewayContractDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	from := time.Now().UTC().Add(-time.Hour)
	to := from.Add(time.Hour)
	_, processErr := gatewayrepo.ListChargeProcessPoints(ctx, db, "canceled", 0, "", 0, 0, 1)
	_, curveErr := gatewayrepo.OrderCurve(ctx, db, contract.OrderCurveRequest{
		OrderID: 1, OrderNo: "canceled", DeviceID: "device", PortNo: 1, From: from, To: to, Granularity: "auto",
	})
	for name, err := range map[string]error{"curve": curveErr, "process": processErr} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s cancellation became success or changed error: %v", name, err)
		}
	}
}
