package settlement

// 本文件集中定义结算与计费各表状态列的类型。取值串以
// migrations/central_db/0001_init.sql 的列定义为唯一依据；
// 同名字面量分散在多张表上，含义各不相同，因此按列分别建模、不做包级合并。
//
// 约定：
//   - 类型定义只描述「该列合法取哪几个值」，不决定任何状态能否流转。
//   - 读取路径遇到未知值一律原值透传，不得据此拒绝、替换或降级。
//   - 不定义 MarshalJSON/UnmarshalJSON：这些类型都以内建 string 为底层，
//     编码结果与原裸 string 逐字节相同。

// BillingJobStatus 是 charge_billing_job.status 列的计费任务状态，
// 描述一笔订单的异步计费任务处于待执行、已转人工还是已结束。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （pending/manual_review/done），数据库默认值为 pending。
//
// 词表边界：
//   - pending：等待计费执行，也是 Defer 失败重排与人工决定后重新排队的目标状态。
//   - manual_review：自动计费无法得出费用，转人工定价或计量核实。
//   - done：费用已入账（重放与首次入账都写此值）。
//
// 注意 manual_review 与 refund_record.execution_policy 的 manual_review 是两回事：
// 前者是「这笔计费任务转人工」，后者是「这笔退款须人工双签复核」，
// 两者由不同表的不同列约束，不得互相赋值或共用常量。
//
// 另注意：订单尚未入队计费任务时，FindBillingJob 返回的零值为空串。
// 空串不是该列的合法取值，仅表示「无任务」，必须原样透出给调用方。
type BillingJobStatus string

const (
	// BillingJobPending 表示待执行或等待重试。
	BillingJobPending BillingJobStatus = "pending"
	// BillingJobManualReview 表示已转人工定价或计量核实。
	BillingJobManualReview BillingJobStatus = "manual_review"
	// BillingJobDone 表示费用已入账。
	BillingJobDone BillingJobStatus = "done"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
// 对空串返回 false 是正确的：空串只表示「尚未排队」，不是该列取值。
// 读取路径不得据此拒绝或替换空串。
func (s BillingJobStatus) Known() bool {
	switch s {
	case BillingJobPending, BillingJobManualReview, BillingJobDone:
		return true
	default:
		return false
	}
}

// AllBillingJobStatuses 是该列的全量取值串，供入参白名单校验直接引用。
const AllBillingJobStatuses = "pending manual_review done"

// BillStatus 是 charge_bill.status 列的用户账单状态。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （issued/settled/voided），数据库默认值为 issued。
//
// 词表边界：
//   - issued：账单已按已确认的结算结果签发，等待结清。
//   - settled：已结清，同时写入 settled_at。
//   - voided：作废。当前 Go 代码没有任何写入或判定路径使用该值，
//     仅因数据库列约束保留，不为其臆造业务语义。
type BillStatus string

const (
	// BillIssued 表示已出账、未结清。
	BillIssued BillStatus = "issued"
	// BillSettled 表示已结清。
	BillSettled BillStatus = "settled"
	// BillVoided 表示作废；当前无 Go 侧写入路径，仅保留以完整覆盖列约束。
	BillVoided BillStatus = "voided"
)

// MeterReviewStatus 是 charge_meter_review.status 列的计量核对状态，
// 描述对设备计量异常提出的人工修正提案走到了哪一步。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （awaiting_second/approved/rejected），数据库默认值为 awaiting_second。
//
// 词表边界：awaiting_second 已首审待复核，approved 复核通过并重新排队计费，
// rejected 复核驳回。注意 approved 表示「计费任务已被重新排回 pending」，
// 不等于账单已结清。
//
// 与 invoice_admin_review.review_status 共享三个字面量但分属两表，
// 两者记录的业务对象不同，按列分别建模。
type MeterReviewStatus string

const (
	// MeterReviewAwaitingSecond 表示已首审，等待第二位审核人复核。
	MeterReviewAwaitingSecond MeterReviewStatus = "awaiting_second"
	// MeterReviewApproved 表示复核通过。
	MeterReviewApproved MeterReviewStatus = "approved"
	// MeterReviewRejected 表示复核驳回。
	MeterReviewRejected MeterReviewStatus = "rejected"
)

// ManualFeeReviewStatus 是 manual_fee_review.status 列的人工定价兜底单状态，
// 描述一笔无法自动计费的订单在人工核实队列里的处理进度。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （pending/resolved），数据库默认值为 pending。
//
// 词表边界：pending 等待人工核实，resolved 已处置（resolved_at 非空）。
//
// 注意与 BillingJobStatus 分立：本表的 pending 表示「人工工单未处理」，
// 而 charge_billing_job 的 pending 表示「计费任务待执行」，两者流转条件不同，
// 字面量相同不得合并。
type ManualFeeReviewStatus string

const (
	// ManualFeeReviewPending 表示等待人工核实。
	ManualFeeReviewPending ManualFeeReviewStatus = "pending"
	// ManualFeeReviewResolved 表示已处置。
	ManualFeeReviewResolved ManualFeeReviewStatus = "resolved"
)

// InvoiceType 是 invoice_request.invoice_type 列的申请发票票种。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （normal/vat_special），数据库默认值为 normal。
type InvoiceType string

const (
	// InvoiceTypeNormal 表示普通发票。
	InvoiceTypeNormal InvoiceType = "normal"
	// InvoiceTypeVatSpecial 表示增值税专用发票。
	InvoiceTypeVatSpecial InvoiceType = "vat_special"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (t InvoiceType) Known() bool {
	return t == InvoiceTypeNormal || t == InvoiceTypeVatSpecial
}

// AllInvoiceTypes 是该列的全量取值串，供入参白名单校验直接引用。
const AllInvoiceTypes = "normal vat_special"

// InvoiceRequestReviewStatus 是 invoice_request.review_status 列的开票申请审核状态，
// 描述申请单本身走到了哪一步。取值与 migrations/central_db/0001_init.sql 中
// 该 ENUM 列一致（pending/approved/rejected/issued），数据库默认值为 pending。
//
// 词表边界：
//   - pending：申请已受理，等待首次审核（这是唯一允许发起审核的状态）。
//   - rejected：审核驳回，终态。
//   - approved：双审通过且发票链接已登记。
//   - issued：已开票。代码中通过双人复核的最后一步直接写入该值，
//     approved 只出现在 invoice_admin_review 侧，不写回本列。
//
// 注意与 InvoiceAdminReviewStatus 分立：本列有 issued 而后者没有；
// 本列有 pending 而后者没有。取值串不同，不得因 approved/rejected 重名而共用常量。
type InvoiceRequestReviewStatus string

const (
	// InvoiceRequestPending 表示申请已受理，等待首次审核。
	InvoiceRequestPending InvoiceRequestReviewStatus = "pending"
	// InvoiceRequestApproved 表示双审通过。
	InvoiceRequestApproved InvoiceRequestReviewStatus = "approved"
	// InvoiceRequestRejected 表示审核驳回。
	InvoiceRequestRejected InvoiceRequestReviewStatus = "rejected"
	// InvoiceRequestIssued 表示已开票。
	InvoiceRequestIssued InvoiceRequestReviewStatus = "issued"
)

// InvoiceAdminReviewStatus 是 invoice_admin_review.review_status 列的双人复核进度，
// 描述首审是否已登记以及二审是否通过。取值与 migrations/central_db/0001_init.sql
// 中该 ENUM 列一致（awaiting_second/approved/rejected），该列没有 DEFAULT。
//
// 词表边界：awaiting_second 已首审待二审，approved 二审通过，rejected 驳回。
//
// 与 InvoiceRequestReviewStatus 分立，两者取值串不同；也与 MeterReviewStatus
// 字面量重合但分属两表，记录的业务对象不同。
type InvoiceAdminReviewStatus string

const (
	// InvoiceAdminAwaitingSecond 表示已首审，等待二审。
	InvoiceAdminAwaitingSecond InvoiceAdminReviewStatus = "awaiting_second"
	// InvoiceAdminApproved 表示二审通过。
	InvoiceAdminApproved InvoiceAdminReviewStatus = "approved"
	// InvoiceAdminRejected 表示驳回。
	InvoiceAdminRejected InvoiceAdminReviewStatus = "rejected"
)

// RefundReviewOutcome 是一次人工退款复核动作得出的结论词表
// （rejected/awaiting_second/approved），供运营接口和审计记录展示。
//
// 注意它不是数据库列：refund_review 表没有 review_status 列，
// 复核结论由 second_signer 与 approved_at 是否为空推导得出
// （migrations/central_db/0001_init.sql 中该表仅这两列表达复核进度）。
// 因此本类型只描述「一次复核动作返回什么」，不代表任何 ENUM 列。
//
// 特别地，它与 money.RefundStatus（refund_record.status）分立：
// 复核驳回时结论为 rejected 且退款单状态也写 money.RefundRejected，
// 但 awaiting_second 与 approved 都不属于 money.RefundStatus 的取值集合，
// 不得按同一状态理解。
type RefundReviewOutcome string

const (
	// RefundOutcomeRejected 表示单人驳回，退款单转为 rejected。
	RefundOutcomeRejected RefundReviewOutcome = "rejected"
	// RefundOutcomeAwaitingSecond 表示首审通过，等待第二人复核。
	RefundOutcomeAwaitingSecond RefundReviewOutcome = "awaiting_second"
	// RefundOutcomeApproved 表示双人复核通过，退款转为自动执行。
	RefundOutcomeApproved RefundReviewOutcome = "approved"
)
