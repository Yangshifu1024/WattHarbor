package money

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 人工复核（退款、风控审核、发票、计量核实）共用的两条规则：
// 复核依据的合法性，以及同一请求被重复提交时的回放一致性。
// 它们与存储和传输无关，放在领域层，各处不再各判一次。

// ErrReviewConflict 表示重放内容与首次复核不一致：不同审核人、不同结论或不同依据。
// 这类请求必须被拒绝而不是覆盖——覆盖会让先前的审核结论凭空消失。
var ErrReviewConflict = errors.New("复核重放与首次记录不一致")

// MaxReviewComment 是复核依据的字数上限。
const MaxReviewComment = 255

// ValidReviewComment 校验人工复核依据：非空、不超过 MaxReviewComment 字，且不含控制字符。
// 控制字符会污染工单展示与后续导出，即使长度合法也判为无效。
func ValidReviewComment(comment string) bool {
	if strings.TrimSpace(comment) == "" || utf8.RuneCountInString(comment) > MaxReviewComment {
		return false
	}
	for _, r := range comment {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// PriorReview 是某次人工复核的首次记录。重放时只有与它完全一致才算同一次操作。
type PriorReview struct {
	ActorID  uint64 // 首次复核人 ID
	Approved bool   // 首次结论：true 通过 / false 驳回
	Comment  string // 首次复核依据
}

// ReviewReplay 判定一次重放能否沿用首次复核的结论。
//
// 三个条件缺一不可：同一个人、同一个结论、同一份依据。任一不同都返回
// ErrReviewConflict。相同的场景下返回 nil，调用方据此原样回放首次回执。
//
// 只有"是否有人复核过"与结论本身应由存储回答，规则不关心记录存在与否。
func ReviewReplay(prior PriorReview, actorID uint64, approved bool, comment string) error {
	if prior.ActorID != actorID || prior.Approved != approved || prior.Comment != comment {
		return ErrReviewConflict
	}
	return nil
}

// ReleaseReplay 是解冻类操作的重放判定：解冻没有通过/驳回之分，只比对人与依据。
// 与 ReviewReplay 分开是因为"结论不参与比对"本身就是一种不同的约束，
// 混成一个带开关的函数，调用处看不出到底比了哪些字段。
func ReleaseReplay(prior PriorReview, actorID uint64, comment string) error {
	if prior.ActorID != actorID || prior.Comment != comment {
		return ErrReviewConflict
	}
	return nil
}
