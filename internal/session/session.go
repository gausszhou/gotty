package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// State is the lifecycle state of a session.
type State int

const (
	// StateIdle means the PTY is running and no client is attached.
	StateIdle State = iota
	// StateRunning means the PTY is running and a client is attached.
	StateRunning
	// StateDestroyed means the session has been destroyed.
	StateDestroyed
)

// String returns the wire representation of the state.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateDestroyed:
		return "destroyed"
	default:
		return "unknown"
	}
}

// Errors returned by session operations.
var (
	// ErrSessionPreempted is returned by an attach that was taken over
	// by a newer attach to the same session (same session id).
	ErrSessionPreempted = errors.New("session preempted by another client")
	// ErrSessionDestroyed is returned when operating on a destroyed session.
	ErrSessionDestroyed = errors.New("session is destroyed")
	// ErrClientClosed indicates the client connection was closed.
	ErrClientClosed = errors.New("client closed")
)

// AttachOptions parameterizes a single attach operation.
type AttachOptions struct {
	// PermitWrite allows the client to write input to the PTY.
	PermitWrite bool
	// FixedCols/FixedRows pin the terminal size; client resize requests
	// are ignored while they are non-zero.
	FixedCols, FixedRows int
	// WindowTitle is sent as a SetWindowTitle frame on attach.
	WindowTitle []byte
	// ReconnectSeconds enables client reconnection when > 0.
	ReconnectSeconds int
	// Preferences is sent as a SetPreferences frame when non-empty.
	Preferences []byte
}

// Terminal is the capability a session needs from the terminal layer.
// *terminal.Terminal implements it; tests may plug in stubs.
type Terminal interface {
	io.ReadWriter

	Resize(cols, rows int) error
	Signal(sig syscall.Signal) error
	Close() error
	Exited() bool
	Wait() error

	PID() int
	Command() string
	Args() []string
	WindowTitleVariables() map[string]interface{}
}

// outputRingCapacity bounds the per-session replay buffer kept for
// reconnecting clients.
//
// 4MB 而非 128KB:重放的是原始字节流,它的隐式终端状态(备用屏进出、
// 光标、已入镜的转义序列)必须与 PTY 真实状态一致才有意义。128KB 在
// 一次实质性的 TUI 会话(opencode/懒加载滚动/长输出)中极易回绕,
// 把较早的 `ESC[?1049h`(进入备用屏)挤出缓冲 —— 刷新后回放失去备用屏
// 入口,程序退出时的 `ESC[?1049l` 变成空操作,画面残留无法清除。
// 4MB 使正常会话几乎不可能回绕,回放保持自洽。
const outputRingCapacity = 4 * 1024 * 1024

// Session is one terminal process with its lifecycle state.
// A Session is safe for concurrent use.
type Session struct {
	id        string
	term      Terminal
	createdAt time.Time
	title     string // 显示名(重命名;同时持久化在 Store)

	mu          sync.Mutex
	state       State
	conn        io.ReadWriter
	lastTouched time.Time // last attach or detach; idle-timeout reference
	// attachEpoch 是所有权令牌:每次 Attach 递增;被抢占的旧 attach
	// 只有在自己的 epoch 仍是当前值时才能把状态退回 Idle。
	attachEpoch uint64

	// outMu makes ring writes (outputPump), the attach-time replay
	// snapshot and the replay boundary atomic relative to each other.
	outMu sync.Mutex
	// total counts the bytes ever appended to out (monotonic).
	total int64
	// replaySeq is the value of total captured when the current attach
	// took its replay snapshot. The pump delivers a chunk live only when
	// its start sequence is >= replaySeq (i.e. the chunk is not part of
	// the replay that was just sent to this client).
	replaySeq int64

	// out keeps the most recent terminal output so that a later attach
	// can replay it (the client screen would otherwise stay blank until
	// the process produces new output).
	out *ring

	// termExited is closed by outputPump once reading the PTY fails,
	// i.e. the terminal process has gone away.
	termExited chan struct{}
}

// New creates a Session wrapping an already-started terminal.
func New(id string, term Terminal) *Session {
	s := &Session{
		id:          id,
		term:        term,
		createdAt:   time.Now(),
		state:       StateIdle,
		lastTouched: time.Now(),
		out:         newRing(outputRingCapacity),
		termExited:  make(chan struct{}),
	}
	// A single persistent reader owns the PTY for the whole session
	// lifetime. If the pump lived inside Attach, a detached client would
	// leave a goroutine stuck in PTY read that steals the output of the
	// next attach.
	go s.outputPump()
	return s
}

// ID returns the session id.
func (s *Session) ID() string { return s.id }

// CreatedAt returns the creation time.
func (s *Session) CreatedAt() time.Time { return s.createdAt }

// Title returns the display name (empty = automatic numbering).
func (s *Session) Title() string { return s.title }

// SetTitle updates the display name.
func (s *Session) SetTitle(title string) { s.title = title }

// Command returns the command running in the session.
func (s *Session) Command() string { return s.term.Command() }

// Args returns the arguments of the command.
func (s *Session) Args() []string { return s.term.Args() }

// PID returns the process id of the session.
func (s *Session) PID() int { return s.term.PID() }

// State returns the current lifecycle state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Exited reports whether the underlying terminal process has exited.
func (s *Session) Exited() bool { return s.term.Exited() }

// Resize resizes the underlying PTY, unless a fixed size is enforced.
func (s *Session) Resize(cols, rows int) error {
	return s.term.Resize(cols, rows)
}

// Signal sends a signal to the underlying process.
func (s *Session) Signal(sig syscall.Signal) error {
	if s.State() == StateDestroyed {
		return ErrSessionDestroyed
	}
	return s.term.Signal(sig)
}

// StateDescription returns the view of the session used by the REST API.
func (s *Session) StateDescription() StateDescription {
	return StateDescription{
		ID:        s.id,
		State:     s.State().String(),
		Command:   s.Command(),
		Args:      s.Args(),
		PID:       s.PID(),
		Exited:    s.Exited(),
		Title:     s.title,
		CreatedAt: s.createdAt.Format(time.RFC3339),
	}
}

// StateDescription is the wire representation of a session.
type StateDescription struct {
	ID        string   `json:"id"`
	State     string   `json:"state"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	PID       int      `json:"pid"`
	Exited    bool     `json:"exited"`
	Title     string   `json:"title,omitempty"` // 显示名(空 = 自动编号)
	CreatedAt string   `json:"created_at"`
}

// Attach bridges conn with the terminal using the binary protocol and
// blocks until the client disconnects, the context is canceled, or the
// terminal process exits.
//
// Attach implements preemption: at most one client is attached per
// session, and a new attach (same session id — e.g. a page refresh)
// immediately takes over by closing the previous client connection.
// The preempted attach returns ErrSessionPreempted and does not touch
// the session state.
func (s *Session) Attach(ctx context.Context, conn io.ReadWriter, opts AttachOptions) error {
	s.mu.Lock()
	if s.state == StateDestroyed {
		s.mu.Unlock()
		return ErrSessionDestroyed
	}
	// 抢占:接管现有附着者
	old := s.conn
	s.attachEpoch++
	myEpoch := s.attachEpoch
	s.conn = conn
	s.state = StateRunning
	s.lastTouched = time.Now()
	s.mu.Unlock()

	if old != nil {
		if closer, ok := old.(io.Closer); ok {
			// 在锁外关闭旧连接,避免阻塞状态机
			closer.Close()
		}
	}

	err := s.bridge(ctx, conn, opts)

	s.mu.Lock()
	switch {
	case s.state == StateDestroyed:
		// 会话已销毁:状态已定,不回收
	case s.attachEpoch == myEpoch:
		// 仍持有所有权:正常脱离
		s.state = StateIdle
		if s.conn == conn {
			s.conn = nil
		}
	default:
		// 已被更新的 attach 接管,不动状态
		err = ErrSessionPreempted
	}
	s.lastTouched = time.Now()
	s.mu.Unlock()

	return err
}

// Detach force-closes the current client connection, if any.
// The PTY keeps running.
func (s *Session) Detach() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()

	if closer, ok := conn.(io.Closer); ok && conn != nil {
		closer.Close()
	}
}

// Destroy tears the session down: it closes the client connection when
// attached and kills the terminal process, then marks the session destroyed.
// attachEpoch is bumped so a still-running attach loses ownership and
// cannot flip the state back to Idle afterwards.
func (s *Session) Destroy() error {
	s.mu.Lock()
	if s.state == StateDestroyed {
		s.mu.Unlock()
		return nil
	}
	s.state = StateDestroyed
	s.attachEpoch++
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()

	if closer, ok := conn.(io.Closer); ok && conn != nil {
		closer.Close()
	}

	return s.term.Close()
}

// LastTouched returns the time of the last attach or detach.
func (s *Session) LastTouched() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTouched
}

// bridge pumps bytes in both directions.
func (s *Session) bridge(ctx context.Context, conn io.ReadWriter, opts AttachOptions) error {
	// Take the replay snapshot and raise the delivery boundary BEFORE
	// anything live can reach this client. outMu makes the snapshot,
	// the boundary and the pump's ring writes atomic relative to each
	// other: bytes recorded before the snapshot are replayed here and
	// skipped by the pump; bytes recorded after are delivered by the
	// pump. Either way nothing is duplicated or lost.
	s.outMu.Lock()
	s.replaySeq = s.total
	snapshot := s.out.Bytes()
	s.outMu.Unlock()

	if err := s.sendInitializeMessage(conn, opts); err != nil {
		return err
	}
	if err := s.replayOutput(conn, snapshot); err != nil {
		return err
	}
	// 回放完成标记:客户端收到它之后才允许上行输入。回放字节流里带着
	// 程序启动时的终端查询(DSR/DECRQM/OSC),新 xterm 会为它们自动生成
	// 应答;这些应答若被写回 PTY,等于向一个早已不等待的程序注入陈旧
	// 且位置错误的数据。见 apps/web/src/utils/ws.ts 的输入静默。
	if _, err := conn.Write(terminal.EncodeReplayDone()); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		errs <- s.masterToSlave(ctx, conn, opts)
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.termExited:
		return terminal.ErrTerminalClosed
	}
}

func (s *Session) sendInitializeMessage(conn io.ReadWriter, opts AttachOptions) error {
	messages := [][]byte{
		terminal.EncodeWindowTitle(opts.WindowTitle),
	}
	if opts.ReconnectSeconds > 0 {
		messages = append(messages, terminal.EncodeReconnect(opts.ReconnectSeconds))
	}
	if len(opts.Preferences) > 0 {
		messages = append(messages, terminal.EncodePreferences(opts.Preferences))
	}

	for _, message := range messages {
		if _, err := conn.Write(message); err != nil {
			return fmt.Errorf("failed to send initializing message: %w", err)
		}
	}
	return nil
}

// replayOutput sends a pre-captured snapshot of the session output
// (from bridge) as output frames.
func (s *Session) replayOutput(conn io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	const chunk = 32 * 1024
	for len(data) > 0 {
		n := len(data)
		if n > chunk {
			n = chunk
		}
		if _, err := conn.Write(terminal.EncodeOutput(data[:n])); err != nil {
			return err
		}

		data = data[n:]
	}
	return nil
}

// outputPump is the session's single PTY reader, started once at session
// creation. It feeds the replay ring and delivers output to the currently
// attached client (if any), so output is never lost or stolen between
// attaches. It exits when the terminal closes.
//
// A chunk is delivered live only when its start sequence is at or beyond
// the current attach's replay boundary; a chunk that fell into the
// attach's replay snapshot is skipped (the client already got it).
func (s *Session) outputPump() {
	buffer := make([]byte, 32*1024)
	for {
		n, err := s.term.Read(buffer)
		if err != nil {
			select {
			case <-s.termExited:
			default:
				close(s.termExited)
			}
			return
		}
		if n == 0 {
			continue
		}

		var deliverTo io.ReadWriter
		s.outMu.Lock()
		chunkStart := s.total
		s.total += int64(n)
		s.out.Write(buffer[:n])
		if chunkStart >= s.replaySeq {
			s.mu.Lock()
			deliverTo = s.conn
			s.mu.Unlock()
		}
		s.outMu.Unlock()

		if deliverTo == nil {
			continue
		}
		if _, err := deliverTo.Write(terminal.EncodeOutput(buffer[:n])); err != nil {
			// Client is gone; the next attach replays from the ring.
			continue
		}
	}
}

// frameReader yields one complete client message (one frame) per call.
// wsConn(WebSocket 连接)实现它;io.Pipe 等测试替身不实现,走逐块读取。
type frameReader interface {
	ReadMessage() ([]byte, error)
}

// masterToSlave relays client frames to the terminal.
//
// 帧读取:WebSocket 连接的 Read 是字节流,一次 Read 并不等于一帧 ——
// 输入帧超过单次缓冲(32KB)时会被拆成多段,只有第一段带类型字节,
// 其余段会被误判为独立帧(第二个"帧"以负载字节开头,解析即错)。
// 所以支持帧读取的连接按"一条完整消息 = 一帧"解析,帧大小只受服务端
// 读限(16MB)约束;不支持者为兼容测试/内部接口退回升级前的逐块路径。
func (s *Session) masterToSlave(ctx context.Context, conn io.ReadWriter, opts AttachOptions) error {
	// 每次 attach 的第一个 resize 帧额外做一次尺寸抖动,让内核向 PTY
	// 前台进程组发 SIGWINCH,强制前台程序整帧重绘。重放结束后画面因此
	// 与程序真实状态收敛(抹掉重放残余的半帧/旧几何内容)。手动向 shell
	// 发 SIGWINCH 无效(bash 不转发),真实 TIOCSWINSZ 才保证到达前台。
	firstResize := true

	// 帧解析后的分发逻辑(两种读取路径共用)
	dispatch := func(message terminal.ClientMessage) error {
		switch message.Type {
		case terminal.Input:
			if !opts.PermitWrite || len(message.Payload) == 0 {
				return nil
			}
			if _, err := s.term.Write(message.Payload); err != nil {
				return fmt.Errorf("failed to write received data to terminal: %w", err)
			}

		case terminal.Ping:
			if _, err := conn.Write(terminal.EncodePong()); err != nil {
				return ErrClientClosed
			}

		case terminal.ResizeTerminal:
			if opts.FixedCols > 0 && opts.FixedRows > 0 {
				return nil
			}
			args, err := terminal.ParseResizeArgs(message.Payload)
			if err != nil {
				return err
			}
			if err := s.term.Resize(args.Columns, args.Rows); err != nil {
				return fmt.Errorf("failed to resize terminal: %w", err)
			}
			if firstResize {
				firstResize = false
				if args.Rows > 1 && args.Columns > 1 {
					// 抖动:r-1 → r 两跳,各触发一次 SIGWINCH;失败只记录,
					// 不阻断正常 resize(前台程序最终按真实尺寸重绘)。
					if err := s.term.Resize(args.Columns, args.Rows-1); err == nil {
						_ = s.term.Resize(args.Columns, args.Rows)
					}
				}
			}
		}
		return nil
	}

	// 帧读取路径(WebSocket):一次 ReadMessage = 一帧
	if fr, ok := conn.(frameReader); ok {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			message, err := fr.ReadMessage()
			if err != nil {
				return ErrClientClosed
			}
			if len(message) == 0 {
				continue
			}
			decoded, err := terminal.DecodeClientFrame(message)
			if err != nil {
				return err
			}
			if err := dispatch(decoded); err != nil {
				return err
			}
		}
	}

	buffer := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := conn.Read(buffer)
		if err != nil {
			return ErrClientClosed
		}

		message, err := terminal.DecodeClientFrame(buffer[:n])
		if err != nil {
			return err
		}
		if err := dispatch(message); err != nil {
			return err
		}
	}
}
