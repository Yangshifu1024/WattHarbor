package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"chargepilot/internal/protocol"
)

type fakeSession struct {
	closed atomic.Bool
	sent   atomic.Int32
}

func (f *fakeSession) Send(context.Context, protocol.Command) error {
	if f.closed.Load() {
		return errors.New("closed")
	}
	f.sent.Add(1)
	return nil
}
func (f *fakeSession) Close() error { f.closed.Store(true); return nil }

func TestReconnectRetiresOldSessionWithoutLosingReplacement(t *testing.T) {
	registry := &Registry{}
	old := &fakeSession{}
	var replaced atomic.Int32
	detachOld := registry.Attach("board", old, func(protocol.Session) { replaced.Add(1) })
	current := &fakeSession{}
	detachCurrent := registry.Attach("board", current, func(protocol.Session) { replaced.Add(1) })
	if !old.closed.Load() {
		t.Fatal("previous connection stayed open")
	}
	if replaced.Load() != 1 {
		t.Fatalf("replacement callback ran %d times, want 1 (only the displaced session)", replaced.Load())
	}
	detachOld()
	if err := registry.Send(context.Background(), "board", protocol.Command{Kind: protocol.CommandStop}); err != nil || current.sent.Load() != 1 {
		t.Fatalf("replacement lost: sent=%d err=%v", current.sent.Load(), err)
	}
	detachCurrent()
	if err := registry.Send(context.Background(), "board", protocol.Command{Kind: protocol.CommandStop}); !errors.Is(err, ErrOffline) {
		t.Fatalf("detached connection still routable: %v", err)
	}
}
