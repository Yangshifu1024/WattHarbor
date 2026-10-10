package order

// 本文件集中 feedback 表的列取值词汇，供订单反馈的提交校验、后台回复与状态流转共用。
// 两个类型分别对应同一张表的两个不同列，词表不同，不合并。

// FeedbackStatus 是 feedback.status 列的反馈处理状态，用于初始写入、后台列表筛选、
// 回复与关闭的流转判定。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（pending／processed／closed）。
// 可写集合按操作边界建模：提交只写 FeedbackPending；回复写 FeedbackProcessed；
// 关闭写 FeedbackClosed。列表读取不设白名单，persist.PageQuery.Status 原样透传给 WHERE，
// 遇到未知值保持原值返回，不据此拒绝或替换。
type FeedbackStatus string

const (
	// FeedbackPending 表示待处理，是用户提交反馈时的初始状态。
	FeedbackPending FeedbackStatus = "pending"
	// FeedbackProcessed 表示已回复但未关闭，后台回复成功后写入。
	FeedbackProcessed FeedbackStatus = "processed"
	// FeedbackClosed 表示已关闭；关闭动作可从任意未关闭状态写入。
	FeedbackClosed FeedbackStatus = "closed"
)

// Known 判断是否属于 feedback.status 的合法集合（三值）。
// 仅用于识别未知值，不决定任何状态能否流转；读取路径遇到未知值保持原值透传。
func (s FeedbackStatus) Known() bool {
	switch s {
	case FeedbackPending, FeedbackProcessed, FeedbackClosed:
		return true
	default:
		return false
	}
}

// FeedbackCategory 是 feedback.category 列的反馈分类，由用户提交时选定。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致
// （rating／complaint／suggestion）。
// 分类仅在提交时写入，代码没有分类变更路径；词表不随新增分类而自动扩展。
type FeedbackCategory string

const (
	// FeedbackCategoryRating 表示评分类反馈。
	FeedbackCategoryRating FeedbackCategory = "rating"
	// FeedbackCategoryComplaint 表示投诉类反馈。
	FeedbackCategoryComplaint FeedbackCategory = "complaint"
	// FeedbackCategorySuggestion 表示建议类反馈。
	FeedbackCategorySuggestion FeedbackCategory = "suggestion"
)

// Known 判断是否属于 feedback.category 的合法集合（三值）。
// 仅用于识别未知值，读取路径保持原值透传。
func (c FeedbackCategory) Known() bool {
	switch c {
	case FeedbackCategoryRating, FeedbackCategoryComplaint, FeedbackCategorySuggestion:
		return true
	default:
		return false
	}
}
