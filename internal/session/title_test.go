package session

import (
	"path/filepath"
	"testing"
)

func TestUpdateTitleAliveAndHistory(t *testing.T) {
	factory, _ := stubFactory()
	path := filepath.Join(t.TempDir(), "sessions.json")
	store, _ := NewFileStore(path)

	m := NewManager(WithTerminalFactory(factory), WithStore(store))
	s, _ := m.Create("mock", nil)
	if err := m.UpdateTitle(s.ID(), "会话A"); err != nil {
		t.Fatalf("failed to rename alive session: %s", err)
	}
	_ = m.Destroy(s.ID())
	if err := m.UpdateTitle(s.ID(), "会话B"); err != nil {
		t.Fatalf("failed to rename historical session: %s", err)
	}

	// 重启后标题仍在
	store2, _ := NewFileStore(path)
	m2 := NewManager(WithTerminalFactory(factory), WithStore(store2))
	h := m2.History()
	if len(h) != 1 || h[0].Title != "会话B" {
		t.Fatalf("title lost after restart: %+v", h)
	}

	if err := m2.UpdateTitle("no-such", "x"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}
