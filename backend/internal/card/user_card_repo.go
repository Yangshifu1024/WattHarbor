package card

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 用户卡查询直接限制 user_id 与未解绑状态，避免先读卡再核对归属的竞态。

// UserCardRepo 是用户侧在线卡操作的读写入口。
type UserCardRepo struct {
	DB *gorm.DB /* central 卡连接，事务由状态或刷卡用例负责 */
}

// LockUserCard 在事务中读取最新记录并加锁 cardNo/userID 自己尚未解绑的卡；返回卡。
// 卡不存在、属于他人或已解绑均返回 gorm.ErrRecordNotFound。
func (s UserCardRepo) LockUserCard(tx *gorm.DB, cardNo string, userID uint64) (OnlineCard, error) {
	var card OnlineCard
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("card_no=? AND user_id=? AND status<>'unbound'", cardNo, userID).Take(&card).Error
	return card, err
}

// SetUserCardStatus 在事务中为已锁定 card 写 status，并以 userID 记录 user_ 前缀审计。
// 返回状态或审计数据库错误；调用方核对归属及允许状态，任一步失败回滚，不提交或改写余额。
func (s UserCardRepo) SetUserCardStatus(tx *gorm.DB, card OnlineCard, userID uint64, status CardStatus) error {
	if err := tx.Model(&card).Update("status", status).Error; err != nil {
		return err
	}
	return tx.Table("online_card_audit").Create(map[string]any{
		"card_id": card.ID, "actor_id": userID, "action": "user_" + status, "detail": "用户主动" + status,
	}).Error
}

// FindCardOperationByEvent 按事件、卡号与端口查找首次操作，三个条件缺一不可。
// 没有记录时返回零值、false、nil；首次扣款事务仍须加锁核对，避免并发重复。
func (s UserCardRepo) FindCardOperationByEvent(ctx context.Context, db *gorm.DB, eventID, cardNo, portCode string) (CardOperation, bool, error) {
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

// List 返回 userID 尚未解绑的卡记录；空集合为 []，只读。
func (s UserCardRepo) List(ctx context.Context, userID uint64) ([]OnlineCard, error) {
	rows := []OnlineCard{}
	err := s.DB.WithContext(ctx).Where("user_id=? AND status<>'unbound'", userID).Find(&rows).Error
	return rows, err
}
