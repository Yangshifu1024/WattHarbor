package database

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对在线卡的绑定与状态变更。每次变更都在 online_card_audit 留一条
// 审计：谁、什么时候、把这张卡从什么状态改成了什么状态、依据是什么。
// 卡本身不记录历史，审计表是唯一的追溯来源。

// ErrCardAlreadyBound 表示卡已绑定他人，必须由原用户先解绑。
var ErrCardAlreadyBound = errors.New("卡已绑定，须先由原用户解绑")

// OnlineCardAuditInput 是一次在线卡变更的审计内容。
type OnlineCardAuditInput struct {
	CardID  uint64
	ActorID uint64
	Action  string // bind 绑定 / active 启用 / lost 挂失 / disabled 停用 / unbound 解绑
	Detail  string // 人工依据
}

// CardAdminStore 是后台在线卡操作的读写入口。
type CardAdminStore struct{ DB *gorm.DB }

// FindActiveUser 确认用户存在、启用且未软删除。
func (s CardAdminStore) FindActiveUser(tx *gorm.DB, userID uint64) error {
	var user struct{ Status string }
	return tx.Table("user").Where("id=? AND status='active' AND deleted_at IS NULL", userID).Take(&user).Error
}

// LockCardByNo 在行锁下按卡号取卡。取不到返回 gorm.ErrRecordNotFound，
// 调用方据此走新建分支——并发绑定同一张卡时两个请求会串行化，
// 后到的那个看到的是已绑定的卡，不会重复插入。
func (s CardAdminStore) LockCardByNo(tx *gorm.DB, cardNo string) (OnlineCard, error) {
	var card OnlineCard
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("card_no=?", cardNo).Find(&card).Error
	return card, err
}

// LockCardByID 在行锁下按主键取卡。
func (s CardAdminStore) LockCardByID(tx *gorm.DB, id uint64) (OnlineCard, error) {
	var card OnlineCard
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&card).Error
	return card, err
}

// CreateOnlineCard 落一张新卡并绑定到用户。
func (s CardAdminStore) CreateOnlineCard(tx *gorm.DB, cardNo string, userID uint64) (uint64, error) {
	card := OnlineCard{CardNo: cardNo, UserID: userID, Status: "active"}
	if err := tx.Create(&card).Error; err != nil {
		return 0, err
	}
	return card.ID, nil
}

// BindOnlineCard 把一张未绑定的卡绑给用户。
func (s CardAdminStore) BindOnlineCard(tx *gorm.DB, id, userID uint64) error {
	return tx.Model(&OnlineCard{}).Where("id=?", id).
		Updates(map[string]any{"user_id": userID, "status": "active"}).Error
}

// SetOnlineCardStatus 写入卡状态。必须传入行锁下读出的那张卡：gorm 只在模型
// 带主键时才生成 WHERE 条件，传一个空壳模型会被当作全表更新而拒绝执行。
func (s CardAdminStore) SetOnlineCardStatus(tx *gorm.DB, card OnlineCard, status string) error {
	return tx.Model(&card).Update("status", status).Error
}

// AppendOnlineCardAudit 记一条卡变更审计。
func (s CardAdminStore) AppendOnlineCardAudit(tx *gorm.DB, in OnlineCardAuditInput) error {
	return tx.Table("online_card_audit").Create(map[string]any{
		"card_id": in.CardID, "actor_id": in.ActorID, "action": in.Action, "detail": in.Detail,
	}).Error
}

// DeviceAlert 是设备告警的流转所需字段。
// EventID 是最近一次故障事件的键，也是恢复判定的水印；CreatedMonth 是分区列，
// 更新时必须带上，否则会扫全部分区。
type DeviceAlert struct {
	ID           uint64
	DeviceID     string
	Metric       string
	EventID      string
	Status       string
	CreatedAt    time.Time
	CreatedMonth time.Time
	ResolvedAt   *time.Time
}

// AlertSyncStore 承接设备告警的同步落库与恢复判定。
type AlertSyncStore struct{ DB *gorm.DB }

// LockLatestDeviceAlert 在行锁下取该设备最近一条非规则告警。
// 故障是持续状态，同一设备同一指标只保留一条告警，靠它做去重。
func (s AlertSyncStore) LockLatestDeviceAlert(tx *gorm.DB, deviceID, metric string) (DeviceAlert, bool, error) {
	var row DeviceAlert
	found := tx.Table("alert_event").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND metric = ? AND rule_id IS NULL", deviceID, metric).
		Order("created_at DESC, id DESC").Take(&row)
	if errors.Is(found.Error, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	if found.Error != nil {
		return row, false, found.Error
	}
	return row, true, nil
}

// MarkAlertOccurrence 推进持续故障的告警：把事件键更新为最近一次报告。
// 恢复判定以这个键为水印，早于它的心跳不构成恢复证据。
func (s AlertSyncStore) MarkAlertOccurrence(tx *gorm.DB, row DeviceAlert, eventID string, faultCode int) error {
	return tx.Table("alert_event").Where("id = ? AND created_month = ?", row.ID, row.CreatedMonth).
		Updates(map[string]any{"event_id": eventID, "value": faultCode}).Error
}

// InsertAlert 落一条新告警。created_month 由调用方按事件时间给出：表按月分区，
// 该列没有默认值，不给会报 Field 'created_month' doesn't have a default value。
func (s AlertSyncStore) InsertAlert(tx *gorm.DB, values map[string]any) error {
	return tx.Table("alert_event").Create(values).Error
}

// ListRecoverableAlerts 返回可能已恢复但尚未核销的告警：设备类告警里
// active 与 acknowledged 两种状态都还在等恢复证据。
func (s AlertSyncStore) ListRecoverableAlerts(ctx context.Context) ([]DeviceAlert, error) {
	rows := []DeviceAlert{}
	err := s.DB.WithContext(ctx).Table("alert_event").
		Where("rule_id IS NULL AND status IN ('active','acknowledged') AND " +
			"(metric IN ('smoke','high_temperature','device_fault') OR metric LIKE 'port_fault_%')").
		Find(&rows).Error
	return rows, err
}

// AutoResolveAlert 把已确认恢复的告警标记为自动核销。
// 带上 event_id 是水印条件：期间又报了新故障的告警不该被这次心跳核销掉。
func (s AlertSyncStore) AutoResolveAlert(tx *gorm.DB, row DeviceAlert, at time.Time) error {
	return tx.Table("alert_event").
		Where("id = ? AND created_month = ? AND event_id = ? AND status IN ('active','acknowledged')",
			row.ID, row.CreatedMonth, row.EventID).
		Updates(map[string]any{"status": "auto_resolved", "resolved_at": at}).Error
}
