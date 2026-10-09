package charge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestSyncBatchErrorBoundsTextAndRetainsEveryCause 验证50条失败只输出单行摘要，所有具体错误仍可errors.Is识别。
func TestSyncBatchErrorBoundsTextAndRetainsEveryCause(t *testing.T) {
	var batch syncBatchErrors
	if batch.err() != nil {
		t.Fatal("empty batch returned error")
	}
	causes := make([]error, 50)
	for i := range causes {
		causes[i] = fmt.Errorf("cause-%d: %s", i, strings.Repeat("失败\n", 200))
		batch.add("end", fmt.Sprint(i+1), causes[i], context.DeadlineExceeded)
	}
	err := batch.err()
	if len([]rune(err.Error())) > 320 || strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), "50 failed") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded summary: %s", err)
	}
	for _, cause := range causes {
		if !errors.Is(err, cause) {
			t.Fatal("cause lost", cause)
		}
	}
}

// TestStartBatchFailuresContinueAndReturnSummary 验证真实批编排对50项中央失败均登记退避，返回单行摘要并保留原始错误。
func TestStartBatchFailuresContinueAndReturnSummary(t *testing.T) {
	failure := errors.New("central unavailable")
	scenario := &startSyncScenario{deliverErr: failure}
	for i := range 50 {
		row := startSyncRow()
		row.CommandID = fmt.Sprintf("start-%d", i)
		scenario.rows = append(scenario.rows, row)
	}
	n, err := (Synchronizer{Gateway: scenario, Central: scenario}).SyncBatch(context.Background())
	if n != 0 || !errors.Is(err, failure) || len(scenario.deferred) != 50 || len(scenario.results) != 50 || len(scenario.reported) != 0 || !strings.Contains(err.Error(), "50 failed") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("count=%d deferred=%d submitted=%d err=%v", n, len(scenario.deferred), len(scenario.results), err)
	}
}
