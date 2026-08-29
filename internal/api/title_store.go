package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gausszhou/gotty/internal/utils"
)

// TitleStore holds the deployment-wide page title (the browser tab title,
// set from the settings dialog). It is persisted to a JSON file so it
// survives restarts; an empty path keeps it in memory only.
type TitleStore struct {
	path  string
	mu    sync.RWMutex
	title string
}

// NewTitleStore loads the page title from path (memory-only when empty).
// Read/parse failures fall back to an empty title instead of failing
// startup — a corrupt or missing file must not take the server down.
func NewTitleStore(path string) *TitleStore {
	store := &TitleStore{path: path}
	if path == "" {
		return store
	}
	if data, err := os.ReadFile(utils.Expand(path)); err == nil {
		var payload struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(data, &payload) == nil {
			store.title = payload.Title
		}
	}
	return store
}

// Get returns the current page title ("" = not set).
func (s *TitleStore) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.title
}

// Set stores the page title and persists it (atomic temp-file rewrite,
// same pattern as the session record store).
func (s *TitleStore) Set(title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.title = title
	if s.path == "" {
		return nil
	}

	payload := struct {
		Title string `json:"title"`
	}{Title: title}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal page title: %w", err)
	}

	path := utils.Expand(s.path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create title file directory `%s`: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("failed to write title file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("failed to replace title file: %w", err)
	}
	return nil
}
