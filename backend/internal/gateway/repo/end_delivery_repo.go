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

// ErrEndDeliveryReadFailed 表示查询已保存结束请求失败。
var ErrEndDeliveryReadFailed = errors.New("read frozen end delivery failed")

// ErrEndDeliveryFreezeFailed 表示保存失败，或主键冲突后无法读取原请求。
var ErrEndDeliveryFreezeFailed = errors.New("freeze end delivery failed")

// ErrEndDeliveryIdentityConflict 表示忽略 meter.segments 后，两份结束请求内容仍不一致。
var ErrEndDeliveryIdentityConflict = errors.New("frozen end result identity conflict")

// FreezeEndDelivery 保存首次结束请求；已有内容一致时返回原请求和 true。
// 只在主键冲突（1062）后重新读取；比较时忽略计量分段，其余内容必须一致。
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
		// 主键冲突仅重读一次；读取失败或未见存量沿用冻结失败分类。
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

// frozenEndPayload 读取已保存的结束请求；不存在时返回空串。
func frozenEndPayload(ctx context.Context, db *gorm.DB, deviceEventID uint64) (string, error) {
	var row struct {
		// PayloadJSON 冻结结束回执 JSON 字节。
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

// insertFrozenEndDelivery 插入已确定的结束请求，不读取旧值或自动重试。
func insertFrozenEndDelivery(db *gorm.DB, deviceEventID, chargeOrderID uint64, payload string) error {
	return db.Table("charge_end_delivery").Create(map[string]any{
		"device_event_id": deviceEventID, "charge_order_id": chargeOrderID, "payload_json": payload,
	}).Error
}

// sameEndIdentity 忽略 meter.segments 后比较 JSON，保留整数精度。
// 无效 JSON 直接比较原文。
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
