package dc589sim

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSharedLoggerShowsStartupAndControlLogsInTUI(t *testing.T) {
	logger := NewTUILogger()
	logger.Print("registering as test-board")
	logger.Print("physical controls: http://127.0.0.1:9190/state and POST /events")
	logger.Print("connecting to test-gateway, scenario by-energy")
	logger.Print("control: listener failed")
	logs := logger.Writer().(*terminalLog)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	inputs := make(chan Input, 16)
	config := quietConfig(t, ScenarioByEnergy)
	config.Gateway, _ = serveGateway(t, ctx, newSink())
	config.Log, config.Inputs = logger, inputs
	finished := make(chan error, 1)
	go func() {
		finished <- runTUI(ctx, config, inputs, cancel,
			tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	}()
	registered := false
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !registered {
		select {
		case <-ticker.C:
			registered = strings.Contains(logs.tail(200), "registered at")
		case err := <-finished:
			t.Fatalf("TUI stopped before runtime log reached the shared buffer: %v", err)
		case <-ctx.Done():
			t.Fatal("runtime logs did not reach the shared buffer")
		}
	}
	m := terminalModel{logs: logs, tab: 3, width: 100, height: 35}
	view := m.View()
	for _, text := range []string{"registering as test-board", "physical controls:", "connecting to test-gateway", "control: listener failed"} {
		if !strings.Contains(view, text) {
			t.Errorf("log missing from TUI: %s", text)
		}
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("TUI did not exit after cancellation")
	}
}

func TestTUIReturnsBoardFailureAfterProgramExits(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "invalid-state.json")
	if err := os.WriteFile(stateFile, []byte("invalid-json"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	inputs := make(chan Input, 16)
	err := runTUI(ctx, Config{StateFile: stateFile, Inputs: inputs, Log: NewTUILogger()}, inputs, cancel,
		tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("board error not returned: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("TUI returned without canceling its caller")
	}
}

func TestTUIQuitCancelsBoardWithoutReportingFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	inputs := make(chan Input, 16)
	logger := NewTUILogger()
	logger.Print("startup before TUI")
	err := runTUI(ctx, Config{Gateway: "invalid-address", Inputs: inputs, Log: logger}, inputs, cancel,
		tea.WithInput(strings.NewReader("q")), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	if err != nil {
		t.Fatalf("normal quit reported failure: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("quit did not cancel the caller before timeout: %v", ctx.Err())
	}
	if !strings.Contains(logger.Writer().(*terminalLog).tail(200), "startup before TUI") {
		t.Fatal("startup log lost")
	}
}

func TestTUICancellationExitsWithoutReportingFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inputs := make(chan Input, 16)
	if err := runTUI(ctx, Config{Inputs: inputs, Log: NewTUILogger()}, inputs, cancel,
		tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer()); err != nil {
		t.Fatal(err)
	}
}
