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

// UserCardRepo 是用户侧在线卡操作的读写入口。
type UserCardRepo struct {
	DB *gorm.DB /* central 卡连接，事务由状态或刷卡用例拥有 */
}

// LockUserCard 在 tx 中当前读并锁定 cardNo/userID 自己尚未解绑的卡；返回原卡或读取错误。
// 卡不存在、属于他人或已解绑均返回 gorm.ErrRecordNotFound；不写入或提交。
func (s UserCardRepo) LockUserCard(tx *gorm.DB, cardNo string, userID uint64) (OnlineCard, error) {
	var card OnlineCard
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("card_no=? AND user_id=? AND status<>'unbound'", cardNo, userID).Take(&card).Error
	return card, err
}

// SetUserCardStatus 在 tx 中为已锁定 card 写 status，并以 userID 记录 user_ 前缀审计。
// 返回状态或审计存储错误；调用方核对归属及允许状态，任一步失败回滚，不提交或改写余额。
func (s UserCardRepo) SetUserCardStatus(tx *gorm.DB, card OnlineCard, userID uint64, status string) error {
	if err := tx.Model(&card).Update("status", status).Error; err != nil {
		return err
	}
	return tx.Table("online_card_audit").Create(map[string]any{
		"card_id": card.ID, "actor_id": userID, "action": "user_" + status, "detail": "用户主动" + status,
	}).Error
}

// FindCardOperationByEvent 在 ctx 下用 db 查询 eventID/cardNo/portCode，返回原操作、存在标志及存储错误。
// 三个条件缺一不可：只看事件 ID 会把别的卡或别的端口的操作算到这次刷卡上。
// 找不到返回零操作、false 和 nil；只读，不加锁或提交，首次事务继续当前读核对。
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

// List 在 ctx 下返回 userID 尚未解绑的原卡记录或存储错误；空集合为 []，只读。
func (s UserCardRepo) List(ctx context.Context, userID uint64) ([]OnlineCard, error) {
	rows := []OnlineCard{}
	err := s.DB.WithContext(ctx).Where("user_id=? AND status<>'unbound'", userID).Find(&rows).Error
	return rows, err
}
