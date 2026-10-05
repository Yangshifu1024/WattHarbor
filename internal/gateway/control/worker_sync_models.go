package control

import (
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
)

// syncCommandDTO 接收原 row，将命令身份及可空端口、结果和 UTC 确认时刻映射为共享记录；返回记录，无 IO。
func syncCommandDTO(row gatewayrepo.SyncCommandRow) gatewaycontract.SyncCommand {
	out := gatewaycontract.SyncCommand{CommandID: row.CommandID, StopCommandID: row.StopCommandID,
		ChargeOrderID: row.ChargeOrderID, OrderNo: row.OrderNo, DeviceID: row.DeviceID,
		PortNo: row.PortNo, Status: row.Status}
	if row.PortID.Valid {
		out.PortID = &row.PortID.Int64
	}
	if row.ResultCode.Valid {
		out.ResultCode = &row.ResultCode.Int16
	}
	if row.AckAt.Valid {
		stamp := row.AckAt.Time.UTC()
		out.AckAt = &stamp
	}
	return out
}
