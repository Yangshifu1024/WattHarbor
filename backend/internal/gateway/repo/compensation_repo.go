package repo

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"chargepilot/internal/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
		if row.Status == "pending" {
			changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'pending'", commandID).
				Updates(map[string]any{"status": "rejected", "result_code": 254, "ack_at": gorm.Expr("CURRENT_TIMESTAMP(3)"), "error": "start authorization revoked before send"})
			if err := changed.Error; err != nil {
				return err
			}
			if changed.RowsAffected != 1 {
				return ErrOrderConflict
			}
			return tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = 'idle'", row.PortID.Int64, orderNo).
				Update("current_order_id", nil).Error
		}
		if row.Status == "rejected" {
			return nil
		}
		if row.Status != "stopping" {
			if err := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','acked')", commandID).
				Updates(map[string]any{"status": "stopping", "error": "central rejected device start"}).Error; err != nil {
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

// StoppingJobs 按更新时间读取最多五十个到期待补偿命令并还原 STOP 会话；返回任务或 SQL、会话错误，不更新状态。
func (s MySQLSink) StoppingJobs(ctx context.Context) ([]StopJob, error) {
	var rows []chargeCommandRow
	err := s.DB.WithContext(ctx).Select("command_id, order_no, device_id, port_no, stop_session_id").
		Where("status = 'stopping' AND (stop_sent_at IS NULL OR stop_sent_at < DATE_SUB(NOW(3), INTERVAL 10 SECOND))").
		Order("updated_at").Limit(50).Find(&rows).Error
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

// MarkStopSent 仅更新 stopping 命令的停止发送时间；返回 SQL 错误，不确认停止或释放端口。
func (s MySQLSink) MarkStopSent(ctx context.Context, commandID string) error {
	return s.DB.WithContext(ctx).Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'stopping'", commandID).
		Update("stop_sent_at", gorm.Expr("CURRENT_TIMESTAMP(3)")).Error
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
