package session

// Store persists session metadata.
// The default implementation is memory-only; a file- or database-backed
// store can be plugged in for durable metadata.
type Store interface {
	// Record persists a newly created session.
	Record(Metadata) error
	// Forget removes a destroyed session.
	Forget(id string) error
	// All returns all recorded session metadata.
	All() []Metadata
}

// Metadata is the persistence view of a session.
type Metadata struct {
	ID        string
	Command   string
	Args      []string
	State     State
	CreatedAt int64 // unix seconds
}

// MemoryStore keeps session metadata in memory.
type MemoryStore struct {
	items map[string]Metadata
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: map[string]Metadata{}}
}

// Record implements Store.
func (s *MemoryStore) Record(meta Metadata) error {
	s.items[meta.ID] = meta
	return nil
}

// Forget implements Store.
func (s *MemoryStore) Forget(id string) error {
	delete(s.items, id)
	return nil
}

// All implements Store.
func (s *MemoryStore) All() []Metadata {
	all := make([]Metadata, 0, len(s.items))
	for _, meta := range s.items {
		all = append(all, meta)
	}
	return all
}
