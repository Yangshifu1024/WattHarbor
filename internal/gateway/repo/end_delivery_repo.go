package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// ErrEndDeliveryReadFailed 表示首次读取冻结载荷失败，底层原因由返回错误保留。
var ErrEndDeliveryReadFailed = errors.New("read frozen end delivery failed")

// ErrEndDeliveryFreezeFailed 表示插入失败或唯一键冲突后未能读取存量；不把未知写结果重试为新写入。
var ErrEndDeliveryFreezeFailed = errors.New("freeze end delivery failed")

// ErrEndDeliveryIdentityConflict 表示存量与候选载荷在忽略 meter.segments 后的原 JSON 规范化比较不一致。
var ErrEndDeliveryIdentityConflict = errors.New("frozen end result identity conflict")

// FreezeEndDelivery 接收 ctx、db、deviceEventID、chargeOrderID 与原 payload，先读存量，缺失才插入，只有 1062 才重读。
// 返回首次写入的原载荷和 false，或身份一致的存量载荷和 true；读、冻结、身份冲突按哨兵分类，SQL 原因保留。
// 保留原请求取消链和 JSON 身份规则，不另加事务、行锁或重试，不更换事件或订单身份。
func FreezeEndDelivery(
	ctx context.Context,
	db *gorm.DB,
	deviceEventID, chargeOrderID uint64,
	payload string,
) (string, bool, error) {
	stored, err := frozenEndPayload(ctx, db, deviceEventID)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", ErrEndDeliveryReadFailed, err)
	}
	if stored == "" {
		err = insertFrozenEndDelivery(db.WithContext(ctx), deviceEventID, chargeOrderID, payload)
		if err == nil {
			return payload, false, nil
		}
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
			return "", false, fmt.Errorf("%w: %w", ErrEndDeliveryFreezeFailed, err)
		}
		// 主键冲突仅重读一次；读取失败或未见存量沿用原冻结失败分类。
		stored, err = frozenEndPayload(ctx, db, deviceEventID)
		if err != nil {
			return "", false, fmt.Errorf("%w: %w", ErrEndDeliveryFreezeFailed, err)
		}
		if stored == "" {
			return "", false, ErrEndDeliveryFreezeFailed
		}
	}
	if !sameEndIdentity(stored, payload) {
		return "", false, ErrEndDeliveryIdentityConflict
	}
	return stored, true, nil
}

// frozenEndPayload 接收 ctx、db 与 deviceEventID，读取原冻结结束载荷；无记录返回空串和 nil，SQL 失败返回错误，无写入。
func frozenEndPayload(ctx context.Context, db *gorm.DB, deviceEventID uint64) (string, error) {
	var row struct {
		// PayloadJSON 原冻结结束回执 JSON 字节。
		PayloadJSON []byte `gorm:"column:payload_json"`
	}
	res := db.WithContext(ctx).Table("charge_end_delivery").
		Where("device_event_id=?", deviceEventID).Find(&row)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", nil
	}
	return string(row.PayloadJSON), nil
}

// insertFrozenEndDelivery 接收已携带请求 context 的 db、deviceEventID、chargeOrderID 与 payload，插入原冻结记录；返回原 SQL 错误，不重读或重试。
func insertFrozenEndDelivery(db *gorm.DB, deviceEventID, chargeOrderID uint64, payload string) error {
	return db.Table("charge_end_delivery").Create(map[string]any{
		"device_event_id": deviceEventID, "charge_order_id": chargeOrderID, "payload_json": payload,
	}).Error
}

// sameEndIdentity 接收存量与候选原文，使用精确 JSON 数字并仅忽略 meter.segments 后规范化比较；返回是否一致，无 IO。
// 无效 JSON 沿用原文比较，不增加字段验证或修改任一原载荷。
func sameEndIdentity(stored, candidate string) bool {
	strip := func(raw string) string {
		var value map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil || !json.Valid([]byte(raw)) {
			return raw
		}
		if meter, ok := value["meter"].(map[string]any); ok {
			delete(meter, "segments")
		}
		normalized, err := json.Marshal(value)
		if err != nil {
			return raw
		}
		return string(normalized)
	}
	return strip(stored) == strip(candidate)
}
