package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Metadata is the persistence view of a session (session record).
// The server keeps records by session id only — it does not keep a
// session list (devices own their own manifests).
type Metadata struct {
	ID        string   `json:"id"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Title     string   `json:"title,omitempty"` // 显示名(可空,前端回退到自动编号)
	State     string   `json:"state"`
	CreatedAt int64    `json:"created_at"` // unix seconds
	RunCount  int      `json:"run_count"`  // resurrect 次数(首次创建为 0)
}

// Store persists session metadata (session history).
// The default implementation is memory-only; a file-backed store
// (FileStore) provides durable history across server restarts.
type Store interface {
	// Record upserts a session record.
	Record(Metadata) error
	// Forget removes a session record.
	Forget(id string) error
	// Get returns the record for a session id, if present.
	Get(id string) (Metadata, bool)
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

// Get implements Store.
func (s *MemoryStore) Get(id string) (Metadata, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.items[id]
	return meta, ok
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

// FileStore persists session records as a JSON file on disk.
// Records are keyed by session id (a JSON object), so resurrecting a
// session by id is a direct lookup. Legacy "array of records" files
// (pre manifest era) are migrated on load. Writes are atomic
// (tmp file + rename) so a crash never corrupts the file.
type FileStore struct {
	mu    sync.Mutex
	path  string
	items map[string]Metadata
}

type fileStorePayload struct {
	Sessions map[string]Metadata `json:"sessions"`
}

// legacyFileStorePayload is the pre-manifest file format (an array),
// accepted on load and rewritten as a map.
type legacyFileStorePayload struct {
	Sessions []Metadata `json:"sessions"`
}

// NewFileStore loads (or creates) a session record file at path.
func NewFileStore(path string) (*FileStore, error) {
	store := &FileStore{
		path:  path,
		items: map[string]Metadata{},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 首次运行:空记录,等待第一条记录写入
			return store, nil
		}
		return nil, fmt.Errorf("failed to read session file at `%s`: %w", path, err)
	}

	// 新格式:{"sessions": {"<id>": {...}}} —— 按 id 键控的记录
	var payload fileStorePayload
	if err := json.Unmarshal(data, &payload); err == nil {
		for id, meta := range payload.Sessions {
			store.items[id] = meta
		}
		return store, nil
	}

	// 旧格式:{"sessions": [{...}]} —— 数组,迁移为 map 并原子重写
	var legacy legacyFileStorePayload
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("failed to parse session file at `%s`: %w", path, err)
	}
	for _, meta := range legacy.Sessions {
		store.items[meta.ID] = meta
	}
	if err := store.persist(); err != nil {
		return nil, fmt.Errorf("failed to migrate session file `%s`: %w", path, err)
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

// Get implements Store.
func (s *FileStore) Get(id string) (Metadata, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.items[id]
	return meta, ok
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

// persist rewrites the whole record file atomically.
func (s *FileStore) persist() error {
	payload := fileStorePayload{
		Sessions: make(map[string]Metadata, len(s.items)),
	}
	for id, meta := range s.items {
		payload.Sessions[id] = meta
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session records: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create session file directory `%s`: %w", dir, err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("failed to replace session file: %w", err)
	}
	return nil
}
