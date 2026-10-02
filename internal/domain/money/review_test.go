package money

import (
	"errors"
	"strings"
	"testing"
)

func TestReviewReplayRequiresSameActorConclusionAndComment(t *testing.T) {
	prior := PriorReview{ActorID: 7, Approved: true, Comment: "已核对渠道流水"}
	// 完全一致 → 视为同一次操作
	if err := ReviewReplay(prior, 7, true, "已核对渠道流水"); err != nil {
		t.Fatalf("一致的重放被拒：%v", err)
	}
	// 三者任一不同都必须冲突
	for name, err := range map[string]error{
		"换了审核人":  ReviewReplay(prior, 8, true, "已核对渠道流水"),
		"改了结论":   ReviewReplay(prior, 7, false, "已核对渠道流水"),
		"改了依据":   ReviewReplay(prior, 7, true, "换个说法"),
		"人结论都改":  ReviewReplay(prior, 8, false, "别的依据"),
		"依据仅差空白": ReviewReplay(prior, 7, true, "已核对渠道流水 "),
	} {
		if !errors.Is(err, ErrReviewConflict) {
			t.Fatalf("%s 应判冲突，实得 %v", name, err)
		}
	}
}

func TestReleaseReplayIgnoresConclusion(t *testing.T) {
	prior := PriorReview{ActorID: 7, Approved: false, Comment: "风险解除"}
	if err := ReleaseReplay(prior, 7, "风险解除"); err != nil {
		t.Fatalf("同人与同依据应放行：%v", err)
	}
	if err := ReleaseReplay(prior, 9, "风险解除"); !errors.Is(err, ErrReviewConflict) {
		t.Fatal("换人解冻应判冲突")
	}
	if err := ReleaseReplay(prior, 7, "换个说法"); !errors.Is(err, ErrReviewConflict) {
		t.Fatal("改依据应判冲突")
	}
}

func TestValidReviewCommentRejectsBlankOversizeAndControl(t *testing.T) {
	if !ValidReviewComment("已核对渠道流水") {
		t.Fatal("正常依据被拒")
	}
	// 中文按字数而非字节计，255 个汉字应当通过
	if !ValidReviewComment(strings.Repeat("据", 255)) {
		t.Fatal("255 字依据被拒")
	}
	if ValidReviewComment(strings.Repeat("据", 256)) {
		t.Fatal("256 字应判超长")
	}
	for name, comment := range map[string]string{
		"空串":   "",
		"纯空白":  "   ",
		"含换行":  "第一行\n第二行",
		"含制表符": "说明\t补充",
		"含退格":  "说明\b",
	} {
		if ValidReviewComment(comment) {
			t.Fatalf("%s 应判无效", name)
		}
	}
}
