package money

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 人工复核共用依据校验与重放比对规则，避免退款、风控、发票、计量核实各自判断。

// ErrReviewConflict 表示重放的审核人、结论或依据与首次记录不同，不能覆盖记录。
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

// ReviewReplay 比对审核人、结论与依据；全部一致才沿用首次回执，否则返回 ErrReviewConflict。
// 记录是否存在由存储层判断。
func ReviewReplay(prior PriorReview, actorID uint64, approved bool, comment string) error {
	if prior.ActorID != actorID || prior.Approved != approved || prior.Comment != comment {
		return ErrReviewConflict
	}
	return nil
}

// ReleaseReplay 比对解冻操作的审核人与依据；解冻没有通过或驳回结论。
func ReleaseReplay(prior PriorReview, actorID uint64, comment string) error {
	if prior.ActorID != actorID || prior.Comment != comment {
		return ErrReviewConflict
	}
	return nil
}
