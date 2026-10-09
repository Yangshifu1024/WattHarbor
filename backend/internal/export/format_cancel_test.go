package export

import (
	"context"
	"errors"
	"testing"
)

// cancellingOutput 模拟首个写入时请求断开，第二次写入必须拒绝。
type cancellingOutput struct {
	cancel context.CancelFunc // 第一次输出时取消请求
	writes int                // 已经发起的真实输出次数
}

// Write 首次写入后取消请求，随后写入报错，不关闭输出。
func (w *cancellingOutput) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, errors.New("output continued after cancellation")
	}
	w.cancel()
	return len(p), nil
}

// TestFormatCancellation 验证未开始的取消不输出，输出中取消不能报告编码成功或继续写入。
func TestFormatCancellation(t *testing.T) {
	for _, format := range []string{"csv", "xlsx"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &cancellingOutput{cancel: cancel}
			err := (FormatWriter{}).Write(ctx, w, format, []string{"中文字段"}, [][]string{{"内容"}})
			if !errors.Is(err, context.Canceled) || w.writes != 1 {
				t.Fatalf("mid-write cancellation=%v writes=%d", err, w.writes)
			}
			w.writes = 0
			err = (FormatWriter{}).Write(ctx, w, format, []string{"中文字段"}, nil)
			if !errors.Is(err, context.Canceled) || w.writes != 0 {
				t.Fatalf("pre-cancellation=%v writes=%d", err, w.writes)
			}
		})
	}
}
