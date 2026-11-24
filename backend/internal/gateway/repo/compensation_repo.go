package repo

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"wattharbor/internal/domain/devices"
	"wattharbor/internal/domain/money"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/protocol"
)

// StopJob 保存启动补偿重试需要的命令标识、设备端口及 STOP 报文，复用停止会话。
type StopJob struct {
	// CommandID 持久命令 UUID。
	CommandID string
	// OrderNo 业务订单号。
	OrderNo string
	// DeviceID 设备业务编号。
	DeviceID string
	// PortNo 协议端口序号。
	PortNo uint8
	// Wire 待下发的协议命令，不表示设备已确认。
	Wire protocol.Command
}

// CompensateStart 在 READ COMMITTED 事务锁定启动所需编号；未发送则拒绝并释放空闲端口，否则准备 STOP；返回任务、需下发标志及冲突或 SQL 错误。
func (s MySQLSink) CompensateStart(ctx context.Context, orderNo, commandID string) (StopJob, bool, error) {
	var job StopJob
	shouldSend := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row chargeCommandRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrOrderConflict
		}
		if err != nil {
			return err
		}
		if row.CommandID != commandID || !row.PortID.Valid || row.PortID.Int64 <= 0 {
			return ErrOrderConflict
		}
		// 已结束或换单的端口不再补偿，不改写历史 START 的状态，也不向新订单发送 STOP。
		var port devicePortRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND device_id=? AND port_no=?", row.PortID.Int64, row.DeviceID, row.PortNo).Take(&port).Error; err != nil {
			return err
		}
		if !port.CurrentOrderID.Valid || port.CurrentOrderID.String != row.OrderNo {
			return nil
		}
		// SQL 内 status = 'pending'／status IN ('sent','acked')／status = 'stopping' 分别对应
		// devices.CommandPending／CommandSent、CommandAcked／CommandStopping（charge_command.status）。
		if row.Status == devices.CommandPending {
			changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'pending'", commandID).
				Updates(map[string]any{"status": devices.CommandRejected, "result_code": 254, "ack_at": gorm.Expr("CURRENT_TIMESTAMP(3)"), "error": "start authorization revoked before send"})
			if err := changed.Error; err != nil {
				return err
			}
			if changed.RowsAffected != 1 {
				return ErrOrderConflict
			}
			return tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = ?", row.PortID.Int64, orderNo, money.PortIdle).
				Update("current_order_id", nil).Error
		}
		if row.Status == devices.CommandRejected {
			return nil
		}
		if row.Status != devices.CommandStopping {
			if err := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','acked')", commandID).
				Updates(map[string]any{"status": devices.CommandStopping, "error": "central rejected device start"}).Error; err != nil {
				return err
			}
		}
		job, err = stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.StopSessionID)
		if err != nil {
			return err
		}
		shouldSend = true
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StopJob{}, false, err
	}
	return job, shouldSend, nil
}

// CommandDevice 按订单和命令身份读取设备编号，供控制服务在改变补偿状态前取得设备许可。
// 身份不存在返回 ErrOrderConflict，不改变命令或占用；设备编号在命令生命周期内不可变。
func (s MySQLSink) CommandDevice(ctx context.Context, orderNo, commandID string) (string, error) {
	var row chargeCommandRow
	err := s.DB.WithContext(ctx).Select("device_id").Where("order_no=? AND command_id=?", orderNo, commandID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrOrderConflict
	}
	return row.DeviceID, err
}

// StoppingJobs 在 LIMIT 前过滤在线设备及仍属于原订单的端口，按最近发送尝试读取最多五十项。
// online 是连接登记快照；空集合不查询，允许已预占但 START 尚未确认的 idle 端口。
func (s MySQLSink) StoppingJobs(ctx context.Context, online []string) ([]StopJob, error) {
	if len(online) == 0 {
		return []StopJob{}, nil
	}
	var rows []chargeCommandRow
	// SQL 内 status = 'stopping' 对应 devices.CommandStopping（charge_command.status）。
	err := s.DB.WithContext(ctx).Table("charge_command c").Select("c.command_id, c.order_no, c.device_id, c.port_no, c.stop_session_id").
		Joins("JOIN device_port p ON p.id=c.port_id AND p.device_id=c.device_id AND p.port_no=c.port_no AND p.current_order_id=c.order_no").
		Where("c.device_id IN ? AND c.status=? AND p.status IN ?", online, devices.CommandStopping, idleOrChargingPorts()).
		Where("c.stop_sent_at IS NULL OR c.stop_sent_at < DATE_SUB(NOW(3), INTERVAL 10 SECOND)").
		Order("COALESCE(c.stop_sent_at,c.updated_at),c.command_id").Limit(50).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	jobs := make([]StopJob, 0, len(rows))
	for _, row := range rows {
		job, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.StopSessionID)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// MarkStopSent 在同设备许可内核对原订单占用后保存补偿 STOP 的尝试时间，返回是否仍可发送及 SQL 错误。
// 零影响表示已经失去占用或命令已终结，不发包、不确认停止、不释放端口。
func (s MySQLSink) MarkStopSent(ctx context.Context, commandID string) (bool, error) {
	// SQL 内 status = 'stopping' 对应 devices.CommandStopping（charge_command.status）。
	res := s.DB.WithContext(ctx).Exec(`UPDATE charge_command c
JOIN device_port p ON p.id=c.port_id AND p.device_id=c.device_id AND p.port_no=c.port_no
SET c.stop_sent_at=CURRENT_TIMESTAMP(3)
WHERE c.command_id=? AND c.status=? AND p.current_order_id=c.order_no AND p.status IN ?`, commandID, devices.CommandStopping, idleOrChargingPorts())
	return res.RowsAffected == 1, res.Error
}

// stopJob 解码 6 字节会话并生成 STOP 任务；会话无效返回 ErrOrderConflict。
func stopJob(commandID, orderNo, deviceID string, port uint8, encoded sql.NullString) (StopJob, error) {
	if !encoded.Valid {
		return StopJob{}, ErrOrderConflict
	}
	value, err := hex.DecodeString(encoded.String)
	if err != nil || len(value) != 6 {
		return StopJob{}, ErrOrderConflict
	}
	var session [6]byte
	copy(session[:], value)
	return StopJob{CommandID: commandID, OrderNo: orderNo, DeviceID: deviceID, PortNo: port,
		Wire: protocol.Command{Kind: protocol.CommandStop, SessionID: session, Port: port}}, nil
}
