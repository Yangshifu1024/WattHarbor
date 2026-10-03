package card

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对在线卡的绑定与状态变更。每次变更都在 online_card_audit 留一条
// 审计：谁、什么时候、把这张卡从什么状态改成了什么状态、依据是什么。
// 卡本身不记录历史，审计表是唯一的追溯来源。

// ErrCardAlreadyBound 表示卡已绑定他人，必须由原用户先解绑。
var ErrCardAlreadyBound = errors.New("卡已绑定，须先由原用户解绑")

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
