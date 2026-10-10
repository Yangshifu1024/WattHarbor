package wallet

// TxnDirection 是中央库 wallet_txn.direction 列的资金方向。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（in/out）。
type TxnDirection string

const (
	// TxnIn 表示入账：余额增加。
	TxnIn TxnDirection = "in"
	// TxnOut 表示出账：余额减少。
	TxnOut TxnDirection = "out"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (d TxnDirection) Known() bool {
	return d == TxnIn || d == TxnOut
}

// AllTxnDirections 是 TxnDirection 的全量取值串，供 HTTP 入参白名单校验直接引用。
const AllTxnDirections = "in out"

// TxnBizType 是中央库 wallet_txn.biz_type 列的流水业务类型，记录账户资金方向的原因。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（recharge/pay/refund/
// gift/freeze/unfreeze/adjust）。
//
// 与 domain/money.PaymentBizType 是两组不同的词表（后者只有 charge/wallet_recharge），
// 禁止取并集：钱包流水记录的是账户资金方向，支付关联记录的是本次收付款挂在哪笔业务上。
//
// gift/freeze/unfreeze/adjust 四个取值在当前 Go 代码中没有写入路径，
// 保留以完整覆盖列约束，不得因无写入路径就删除（可能存在历史数据）。
type TxnBizType string

const (
	// TxnBizRecharge 表示钱包充值入账。
	TxnBizRecharge TxnBizType = "recharge"
	// TxnBizPay 表示消费出账。
	TxnBizPay TxnBizType = "pay"
	// TxnBizRefund 表示退款入账。
	TxnBizRefund TxnBizType = "refund"
	// TxnBizGift 表示赠送入账；当前无写入路径。
	TxnBizGift TxnBizType = "gift"
	// TxnBizFreeze 表示资金冻结扣减；当前无写入路径。
	TxnBizFreeze TxnBizType = "freeze"
	// TxnBizUnfreeze 表示冻结释放入账；当前无写入路径。
	TxnBizUnfreeze TxnBizType = "unfreeze"
	// TxnBizAdjust 表示人工调账；当前无写入路径。
	TxnBizAdjust TxnBizType = "adjust"
)

// Known 判断是否属于该列的数据库合法集合（七值）。
func (b TxnBizType) Known() bool {
	switch b {
	case TxnBizRecharge, TxnBizPay, TxnBizRefund, TxnBizGift,
		TxnBizFreeze, TxnBizUnfreeze, TxnBizAdjust:
		return true
	default:
		return false
	}
}

// AllTxnBizTypes 是 TxnBizType 的全量取值串，顺序与上方常量声明一致。
const AllTxnBizTypes = "recharge pay refund gift freeze unfreeze adjust"
