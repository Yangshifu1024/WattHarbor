package control

import (
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
)

// syncCommandDTO 接收原 row，将命令身份及可空端口、结果和 UTC 确认时刻映射为共享记录；返回记录，无 IO。
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
type workerSyncCommandRow = gatewayrepo.SyncCommandRow

// workerSyncEventRow 沿用 repo 的 device_event 同步列投影，保留原持久事件身份、载荷与可空处理时刻。
type workerSyncEventRow = gatewayrepo.SyncEventRow

// workerSyncPortRow 沿用 repo 的 device_port 释放列投影，保留原端口自增主键、状态及占用订单。
type workerSyncPortRow = gatewayrepo.SyncPortRow
