// Package snowflake 为使用 central_db 的多个进程分配不重复的整数 ID。
package snowflake

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ID 由 41 位时间戳、10 位节点号和 12 位序列号组成。
// central_db 串行分配，节点号固定为零；持久化时间戳和序列号，避免重启或时钟回拨导致重复。
// 编号分配与使用该编号的业务记录在同一事务中提交。
const (
	epochMillis  int64 = 1577836800000 // 2020-01-01T00:00:00Z
	sequenceBits       = 12
	nodeBits           = 10
	maxSequence        = 1<<sequenceBits - 1
	maxMillis          = 1<<41 - 1
)

type state struct {
	ID              uint8  `gorm:"column:id;primaryKey"`
	LastMillisecond uint64 `gorm:"column:last_millisecond"`
	Sequence        uint16 `gorm:"column:sequence"`
}

// TableName 将编号状态映射到初始化 SQL 创建的 snowflake_state 表。
func (state) TableName() string { return "snowflake_state" }

// Next 锁住并更新编号状态，必须与使用该编号的记录在同一 central_db 事务中提交。
// 状态行由数据库初始化创建；时钟或状态无效、编号时间位用尽时返回错误。
func Next(tx *gorm.DB) (uint64, error) {
	var current state
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = 1").Take(&current).Error; err != nil {
		return 0, err
	}
	id, next, err := advance(current, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	if err := tx.Model(&state{}).Where("id = 1").Updates(map[string]any{
		"last_millisecond": next.LastMillisecond, "sequence": next.Sequence,
	}).Error; err != nil {
		return 0, err
	}
	return id, nil
}

// NextSQL 为使用 database/sql 的示例数据事务分配 ID。
func NextSQL(ctx context.Context, tx *sql.Tx) (uint64, error) {
	var current state
	if err := tx.QueryRowContext(ctx, "SELECT last_millisecond, sequence FROM snowflake_state WHERE id=1 FOR UPDATE").Scan(&current.LastMillisecond, &current.Sequence); err != nil {
		return 0, err
	}
	id, next, err := advance(current, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE snowflake_state SET last_millisecond=?, sequence=? WHERE id=1", next.LastMillisecond, next.Sequence)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// advance 在时钟回拨时沿用已保存的时间，并增加序号；序号用尽后将时间推进一毫秒。
func advance(current state, unixMillis int64) (uint64, state, error) {
	if unixMillis <= epochMillis || current.LastMillisecond > maxMillis || current.Sequence > maxSequence {
		return 0, state{}, errors.New("invalid Snowflake clock or state")
	}
	millis := uint64(unixMillis - epochMillis)
	sequence := uint16(0)
	if millis <= current.LastMillisecond {
		millis = current.LastMillisecond
		if current.Sequence == maxSequence {
			millis++
		} else {
			sequence = current.Sequence + 1
		}
	}
	if millis > maxMillis {
		return 0, state{}, errors.New("Snowflake timestamp exhausted")
	}
	next := state{ID: 1, LastMillisecond: millis, Sequence: sequence}
	return millis<<(nodeBits+sequenceBits) | uint64(sequence), next, nil
}
