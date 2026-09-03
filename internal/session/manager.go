package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
	"github.com/gausszhou/gotty/internal/utils"
)

// Errors returned by the Manager.
var (
	// ErrNotFound is returned when no session with the given id exists.
	ErrNotFound = errors.New("session not found")
	// ErrTooManySessions is returned when the manager is full.
	ErrTooManySessions = errors.New("too many sessions")
	// ErrNoCommand is returned when a fresh session (no record to
	// resurrect) is created without a command.
	ErrNoCommand = errors.New("no command given")
)

// TerminalFactory creates a Terminal. The default factory wraps
// terminal.New; tests may inject a stub.
type TerminalFactory func(command string, args []string, opts ...terminal.Option) (Terminal, error)

// Manager is the registry and lifecycle owner of all sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	store    Store

	maxSession  int
	idleTimeout time.Duration
	baseOpts    []terminal.Option
	factory     TerminalFactory
	mirrorF     MirrorFactory
	// answerQueries is forwarded to every session: answer terminal queries
	// from the mirror when no browser client is attached (--answer-queries).
	answerQueries bool
}

// MirrorFactory builds the screen mirror for a new session (nil return
// disables it). The concrete factory lives in the api layer — session
// must not import capture, whose browser engine depends on session.
type MirrorFactory func(term Terminal) ScreenMirror

// Option configures a Manager.
type Option func(*Manager)

// WithMaxSession caps the number of concurrently alive sessions (0 = unlimited).
func WithMaxSession(n int) Option {
	return func(m *Manager) {
		m.maxSession = n
	}
}

// WithIdleTimeout destroys sessions that stay unattached for the given
// duration (0 = disabled).
func WithIdleTimeout(d time.Duration) Option {
	return func(m *Manager) {
		m.idleTimeout = d
	}
}

// WithTerminalOptions applies base options to every created terminal.
func WithTerminalOptions(options terminal.Options) Option {
	return func(m *Manager) {
		m.baseOpts = append(m.baseOpts,
			terminal.WithCloseSignal(parseSignal(options.CloseSignal, terminal.DefaultCloseSignal)),
			terminal.WithTerm(options.Term),
			terminal.WithEnv(options.Env),
		)
		if options.CloseTimeout >= 0 {
			m.baseOpts = append(m.baseOpts,
				terminal.WithCloseTimeout(time.Duration(options.CloseTimeout)*time.Second))
		}
	}
}

// WithTerminalFactory overrides the terminal constructor (mainly for tests).
func WithTerminalFactory(factory TerminalFactory) Option {
	return func(m *Manager) {
		m.factory = factory
	}
}

// WithStore sets the metadata store. Defaults to a MemoryStore.
func WithStore(store Store) Option {
	return func(m *Manager) {
		m.store = store
	}
}

// WithMirrorFactory sets the per-session screen-mirror builder that
// powers the agent-driving API (GET /screen, POST /wait). A nil factory
// disables the mirror entirely (--mirror=false).
func WithMirrorFactory(f MirrorFactory) Option {
	return func(m *Manager) {
		m.mirrorF = f
	}
}

// WithAnswerQueries forwards the --answer-queries flag to every session:
// terminal queries are answered from the mirror when no browser client is
// attached. Default true when unset.
func WithAnswerQueries(enabled bool) Option {
	return func(m *Manager) {
		m.answerQueries = enabled
	}
}

// NewManager creates a session manager.
func NewManager(options ...Option) *Manager {
	m := &Manager{
		sessions: make(map[string]*Session),
		store:    NewMemoryStore(),
		factory: func(command string, args []string, opts ...terminal.Option) (Terminal, error) {
			return terminal.New(command, args, opts...)
		},
	}
	for _, option := range options {
		option(m)
	}
	return m
}

// parseSignal converts a numeric signal to syscall.Signal, falling back
// to the default when the value is invalid.
func parseSignal(value int, fallback syscall.Signal) syscall.Signal {
	if value > 0 {
		return syscall.Signal(value)
	}
	return fallback
}

// Create starts a new session running command, letting the server
// generate the session id (legacy clients that do not send one).
// termOpts are applied on top of the manager's base terminal options
// (e.g. terminal.WithInitialSize for the requested PTY size).
func (m *Manager) Create(command string, args []string, termOpts ...terminal.Option) (*Session, error) {
	sess, _, err := m.CreateWithID("", command, args, termOpts...)
	return sess, err
}

// CreateWithID starts a session under a client-chosen id with
// idempotent and resurrect semantics:
//
//   - a session with the id already alive is returned unchanged
//     (created = false) — the request is a no-op;
//   - otherwise, if the store holds a record for the id, the session is
//     **resurrected**: the recorded command/args are used to rebuild a
//     session with the same id and run_count is incremented;
//   - otherwise a fresh session is started with the given id.
//
// An empty id makes the server generate one (legacy clients).
// The id must be format-validated by the caller before calling this.
func (m *Manager) CreateWithID(id, command string, args []string, termOpts ...terminal.Option) (*Session, bool, error) {
	m.mu.Lock()

	// 幂等:同 id 已存活 → 直接返回现有会话
	// (进程已退出的会话不算存活,走记录复活路径)
	if id != "" {
		if existing, ok := m.sessions[id]; ok && !existing.Exited() {
			m.mu.Unlock()
			return existing, false, nil
		}
	}

	if m.maxSession > 0 && len(m.sessions) >= m.maxSession {
		m.mu.Unlock()
		return nil, false, ErrTooManySessions
	}

	// 复活:记录存在 → 用记录的 command/args 重建同 id 会话
	var record Metadata
	recorded := false
	if id != "" {
		if meta, ok := m.store.Get(id); ok {
			record = meta
			command = record.Command
			args = record.Args
			recorded = true
		}
	}

	if id == "" {
		id = utils.RandomString(16)
	}

	// 全新会话必须携带命令;复活会话在下方使用记录命令
	if !recorded && command == "" {
		m.mu.Unlock()
		return nil, false, ErrNoCommand
	}

	opts := make([]terminal.Option, 0, len(m.baseOpts)+len(termOpts))
	opts = append(opts, m.baseOpts...)
	opts = append(opts, termOpts...)

	term, err := m.factory(command, args, opts...)
	if err != nil {
		m.mu.Unlock()
		return nil, false, fmt.Errorf("failed to create terminal: %w", err)
	}

	var mirror ScreenMirror
	if m.mirrorF != nil {
		mirror = m.mirrorF(term)
	}
	s := New(id, term, WithScreenMirror(mirror), withAnswerQueries(m.answerQueries))
	m.sessions[s.ID()] = s

	meta := Metadata{
		ID:      s.ID(),
		Command: command,
		Args:    args,
		State:   s.State().String(),
	}
	if recorded {
		// 复活:保留原记录的创建时间与标题,运行次数 +1
		meta.CreatedAt = record.CreatedAt
		meta.Title = record.Title
		meta.RunCount = record.RunCount + 1
	} else {
		meta.CreatedAt = s.CreatedAt().Unix()
	}
	_ = m.store.Record(meta)

	m.mu.Unlock()
	return s, true, nil
}

// Status returns the alive sessions among ids (order preserved,
// missing/exited ones skipped). It powers the client manifest
// polling: POST /api/sessions/status.
func (m *Manager) Status(ids []string) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := make([]*Session, 0, len(ids))
	for _, id := range ids {
		if s, ok := m.sessions[id]; ok && s.State() != StateDestroyed && !s.Exited() {
			sessions = append(sessions, s)
		}
	}
	return sessions
}

// Get returns the session with the given id.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// List returns a snapshot of all alive sessions.
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

// Count returns the number of alive sessions.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Destroy stops the process of the session and removes it from the registry.
// The session record stays in the store as history.
func (m *Manager) Destroy(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	m.mu.Unlock()

	if err := s.Destroy(); err != nil {
		return err
	}

	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

// History returns all recorded sessions (alive ones excluded),
// i.e. the durable session history across restarts.
func (m *Manager) History() []Metadata {
	alive := make(map[string]struct{}, len(m.sessions))
	m.mu.Lock()
	for id := range m.sessions {
		alive[id] = struct{}{}
	}
	m.mu.Unlock()

	history := make([]Metadata, 0)
	for _, meta := range m.store.All() {
		if _, ok := alive[meta.ID]; !ok {
			history = append(history, meta)
		}
	}
	return history
}

// UpdateTitle renames a session (alive or historical) in the store.
func (m *Manager) UpdateTitle(id, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 活会话同步显示名
	if s, ok := m.sessions[id]; ok {
		s.SetTitle(title)
	}

	for _, meta := range m.store.All() {
		if meta.ID != id {
			continue
		}
		meta.Title = title
		return m.store.Record(meta)
	}
	return ErrNotFound
}

// Start runs the maintenance loop (expiry sweep) until ctx is canceled.
func (m *Manager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.DestroyExpired()
			}
		}
	}()
}

// DestroyExpired removes exited and destroyed sessions, and destroys
// sessions that stayed unattached beyond the idle timeout.
// Session records stay in the store as history.
func (m *Manager) DestroyExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, s := range m.sessions {
		switch {
		case s.State() == StateDestroyed, s.Exited():
			delete(m.sessions, id)

		case m.idleTimeout > 0 && s.State() == StateIdle &&
			time.Since(s.LastTouched()) >= m.idleTimeout:
			s.Destroy()
			delete(m.sessions, id)
		}
	}
}
