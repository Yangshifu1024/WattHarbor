package settlement

import (
	"gorm.io/gorm"
)

// AdminFinanceStore 持有后台事件入队所需的连接。
//
// 原先这里还承接分账汇总、提现单与对账差异的只读查询，随多主体分账
// 一并删除：站点自有、没有加盟商，"按 10000 基点拆给各方再走提现打款"
// 这条链路没有业务对象。计价与开账单不经过这里。
type AdminFinanceStore struct {
	AdminDB *gorm.DB
}
