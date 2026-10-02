package control

import (
	"database/sql"
	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
)

func nullableInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// 与 worker 同步端点相关的记录类型，定义在持久化层。
type workerSyncCommandRow = gatewaystore.SyncCommandRow

type workerSyncEventRow = gatewaystore.SyncEventRow

type workerSyncPortRow = gatewaystore.SyncPortRow
