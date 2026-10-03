package card

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 用户侧的在线卡操作与刷卡事件幂等判定。
//
// 用户只能对自己的卡做挂失与解绑，所以查询条件里就带上 user_id 与
// "未解绑"，不用先取卡再比对——条件写在 SQL 里，少一次竞态窗口。

// UserCardStore 是用户侧在线卡操作的读写入口。
type UserCardStore struct{ DB *gorm.DB }

// LockUserCard 在行锁下取用户自己的、尚未解绑的卡。
// 卡不存在或已解绑都返回 gorm.ErrRecordNotFound，调用方据此报 404。
func (s UserCardStore) LockUserCard(tx *gorm.DB, cardNo string, userID uint64) (OnlineCard, error) {
	var card OnlineCard
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("card_no=? AND user_id=? AND status<>'unbound'", cardNo, userID).Take(&card).Error
	return card, err
}

// SetUserCardStatus 改卡状态，并追加一条用户发起的审计。
// 审计动作带 user_ 前缀，与运营侧的同名动作区分开。
func (s UserCardStore) SetUserCardStatus(tx *gorm.DB, card OnlineCard, userID uint64, status string) error {
	if err := tx.Model(&card).Update("status", status).Error; err != nil {
		return err
	}
	return tx.Table("online_card_audit").Create(map[string]any{
		"card_id": card.ID, "actor_id": userID, "action": "user_" + status, "detail": "用户主动" + status,
	}).Error
}

// FindCardOperationByEvent 按事件 ID 查刷卡操作，并确认它属于该卡与该端口。
// 三个条件缺一不可：只看事件 ID 会把别的卡或别的端口的操作算到这次刷卡上。
// 找不到返回空切片（RowsAffected=0），调用方据此走首次处理分支。
func (s UserCardStore) FindCardOperationByEvent(ctx context.Context, db *gorm.DB, eventID, cardNo, portCode string) (CardOperation, bool, error) {
	var prior CardOperation
	found := db.WithContext(ctx).Table("card_operation o").Select("o.*").
		Joins("JOIN online_card c ON c.id=o.card_id").
		Joins("JOIN card_charge s ON s.charge_order_id=o.charge_order_id").
		Where("o.event_id=? AND c.card_no=? AND s.port_code=?", eventID, cardNo, portCode).Find(&prior)
	if found.Error != nil {
		return prior, false, found.Error
	}
	return prior, found.RowsAffected > 0, nil
}
