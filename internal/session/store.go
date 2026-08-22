package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Metadata is the persistence view of a session (session history).
type Metadata struct {
	ID        string   `json:"id"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Title     string   `json:"title,omitempty"` // 显示名(可空,前端回退到自动编号)
	State     string   `json:"state"`
	CreatedAt int64    `json:"created_at"` // unix seconds
}

// Store persists session metadata (session history).
// The default implementation is memory-only; a file-backed store
// (FileStore) provides durable history across server restarts.
type Store interface {
	// Record upserts a session record.
	Record(Metadata) error
	// Forget removes a session record.
	Forget(id string) error
	// All returns all recorded session metadata.
	All() []Metadata
}

// MemoryStore keeps session metadata in memory (tests / ephemeral runs).
type MemoryStore struct {
	mu    sync.Mutex
	items map[string]Metadata
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: map[string]Metadata{}}
}

// Record implements Store.
func (s *MemoryStore) Record(meta Metadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[meta.ID] = meta
	return nil
}

// Forget implements Store.
func (s *MemoryStore) Forget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return nil
}

// All implements Store.
func (s *MemoryStore) All() []Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]Metadata, 0, len(s.items))
	for _, meta := range s.items {
		all = append(all, meta)
	}
	return all
}

// FileStore persists session history as a JSON file on disk.
// Writes are atomic (tmp file + rename) so a crash never corrupts the file.
type FileStore struct {
	mu    sync.Mutex
	path  string
	items map[string]Metadata
}

type fileStorePayload struct {
	Sessions []Metadata `json:"sessions"`
}

// NewFileStore loads (or creates) a session history file at path.
func NewFileStore(path string) (*FileStore, error) {
	store := &FileStore{
		path:  path,
		items: map[string]Metadata{},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 首次运行:空历史,等待第一条记录写入
			return store, nil
		}
		return nil, fmt.Errorf("failed to read session history at `%s`: %w", path, err)
	}

	var payload fileStorePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse session history at `%s`: %w", path, err)
	}
	for _, meta := range payload.Sessions {
		store.items[meta.ID] = meta
	}
	return store, nil
}

// Record implements Store.
func (s *FileStore) Record(meta Metadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[meta.ID] = meta
	return s.persist()
}

// Forget implements Store.
func (s *FileStore) Forget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return s.persist()
}

// All implements Store.
func (s *FileStore) All() []Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]Metadata, 0, len(s.items))
	for _, meta := range s.items {
		all = append(all, meta)
	}
	return all
}

// persist rewrites the whole history file atomically.
func (s *FileStore) persist() error {
	payload := fileStorePayload{
		Sessions: make([]Metadata, 0, len(s.items)),
	}
	for _, meta := range s.items {
		payload.Sessions = append(payload.Sessions, meta)
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session history: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create history directory `%s`: %w", dir, err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("failed to write session history: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("failed to replace session history: %w", err)
	}
	return nil
}
