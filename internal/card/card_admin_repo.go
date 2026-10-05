package card

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 运营后台对在线卡的绑定与状态变更。每次变更都在 online_card_audit 留一条
// 审计：谁、什么时候、把这张卡从什么状态改成了什么状态、依据是什么。
// 卡本身不记录历史，审计表是唯一的追溯来源。

// ErrCardAlreadyBound 表示卡已绑定他人，必须由原用户先解绑。
var ErrCardAlreadyBound = errors.New("卡已绑定，须先由原用户解绑")

// CardAdminRepo 是后台在线卡操作的读写入口。
type CardAdminRepo struct {
	DB *gorm.DB /* central 卡连接，事务由管理用例拥有 */
}

// LockCardByNo 在调用方 tx 中当前读并锁定 cardNo，返回原卡或缺失／存储错误。
// 绑定用例须先竞争唯一键，再取得此锁核对原归属；不创建卡或提交事务。
func (s CardAdminRepo) LockCardByNo(tx *gorm.DB, cardNo string) (OnlineCard, error) {
	var card OnlineCard
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("card_no=?", cardNo).Take(&card).Error
	return card, err
}

// LockCardByID 在 tx 中当前读并锁定 id，返回原卡或缺失／存储错误；不写入或提交。
func (s CardAdminRepo) LockCardByID(tx *gorm.DB, id uint64) (OnlineCard, error) {
	var card OnlineCard
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&card).Error
	return card, err
}

// BindOnlineCard 在已锁卡的 tx 中为 id 更新 userID 和 active 状态，返回存储错误。
// 调用方已核对未绑定及用户准入；审计及提交由用例执行，不迁移钱包余额。
func (s CardAdminRepo) BindOnlineCard(tx *gorm.DB, id, userID uint64) error {
	return tx.Model(&OnlineCard{}).Where("id=?", id).
		Updates(map[string]any{"user_id": userID, "status": "active"}).Error
}

// SetOnlineCardStatus 在 tx 内为行锁读取的 card 写 status，返回存储错误。
// card 必须带原主键，调用方核对状态约束并同事务审计；不结束充电或提交事务。
func (s CardAdminRepo) SetOnlineCardStatus(tx *gorm.DB, card OnlineCard, status string) error {
	return tx.Model(&card).Update("status", status).Error
}

// AppendOnlineCardAudit 在调用方 tx 中保存 in 原卡、操作者、动作及依据，返回存储错误。
// 不独立提交，失败必须回滚同次卡状态或绑定变更。
func (s CardAdminRepo) AppendOnlineCardAudit(tx *gorm.DB, in OnlineCardAuditInput) error {
	return tx.Table("online_card_audit").Create(map[string]any{
		"card_id": in.CardID, "actor_id": in.ActorID, "action": in.Action, "detail": in.Detail,
	}).Error
}

// ClaimBindingInTx 在 tx 中以 cardNo 唯一键首次绑定 userID，返回是否新建或存储错误。
// 重复键不改原归属，后续必须行锁核对身份；避免 RR 对缺失卡先取间隙锁后并发插入的死锁。
// 本函数不审计或提交，调用方必须与人工依据一起提交或回滚。
func (s CardAdminRepo) ClaimBindingInTx(tx *gorm.DB, cardNo string, userID uint64) (bool, error) {
	res := tx.Exec("INSERT IGNORE INTO online_card (card_no,user_id,status) VALUES (?,?,'active')", cardNo, userID)
	return res.RowsAffected == 1, res.Error
}

// List 在 ctx 下返回最新500张卡或存储错误；空集合为 []，调用方负责运营权限，不修改归属。
func (s CardAdminRepo) List(ctx context.Context) ([]OnlineCard, error) {
	rows := []OnlineCard{}
	err := s.DB.WithContext(ctx).Order("id DESC").Limit(500).Find(&rows).Error
	return rows, err
}
