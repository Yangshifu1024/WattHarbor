package control

import (
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
)

func syncCommandDTO(row workerSyncCommandRow) gatewaycontract.SyncCommand {
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

// 与 worker 同步端点相关的记录类型，定义在持久化层。
type workerSyncCommandRow = gatewaystore.SyncCommandRow

type workerSyncEventRow = gatewaystore.SyncEventRow

type workerSyncPortRow = gatewaystore.SyncPortRow
