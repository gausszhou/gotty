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

// outputRingCapacity bounds the per-session output buffer kept for the
// attach-time screen restore (see attachReplayTailBytes). 4MB keeps the
// tail slice inside a self-consistent window for essentially any session.
const outputRingCapacity = 4 * 1024 * 1024

// attachReplayTailBytes is how much of the recent output a fresh attach
// replays so the client sees the pre-refresh screen instead of a blank
// terminal. It is a small tail (a few thousand lines), NOT the whole
// history: replaying megabytes scrolls the page frantically and, once the
// ring has wrapped, starts at an arbitrary byte that no longer reconstructs
// the terminal state. A short tail renders instantly, and xterm tolerates
// a cut-in sequence (dropped escape); full-screen programs redraw anyway
// on the attach-time SIGWINCH, and the shell prompt is inside the tail.
const attachReplayTailBytes = 256 * 1024

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
	// the tail that was just replayed to this client).
	replaySeq int64

	// out keeps the most recent terminal output so that a fresh attach
	// can replay its tail (the client screen would otherwise stay blank
	// until the process produces new output).
	out *ring

	// lastCols/lastRows remember the most recent successful resize, used
	// to jitter the PTY size when a restored attach replays history (see
	// jitterSize). Zero means the terminal was never resized.
	lastCols, lastRows int

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
	// 刷新/重连时重放最近一段输出(attachReplayTailBytes),让新客户端
	// 看到刷新前的画面而不是空白终端。只重放尾部——全量重放会疯狂
	// 滚动,且环形回绕后从任意字节开始,无法重建终端状态(画面拼错)。
	// 备用屏(全屏程序)场景由客户端首个 resize 的 SIGWINCH 整帧重绘兜底。
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
	// 握手完成标记:客户端收到它之后才允许上行输入。新 xterm 会对自身
	// 生成的终端查询(DSR/DECRQM/OSC)自动应答;在输入上行开启前这些应答
	// 被静默丢弃,不会写回 PTY。见 apps/web/src/utils/ws.ts 的输入静默。
	if _, err := conn.Write(terminal.EncodeReplayDone()); err != nil {
		return err
	}
	// 恢复历史会话(重放了非空输出尾部):重放只是字节回放,画面可能与
	// 程序真实状态不一致(备用屏入口丢失/半帧/旧几何)。补一次真实
	// TIOCSWINSZ 抖动向 PTY 前台进程组发 SIGWINCH,让前台程序整帧重绘,
	// 画面与真实状态收敛。新会话(ring 为空)不抖,避免打扰程序启动。
	if len(snapshot) > 0 {
		s.jitterSize()
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

// replayOutput sends the tail of a pre-captured output snapshot (from
// bridge) as output frames, capped at attachReplayTailBytes — enough to
// restore the screen the user saw before the refresh, without scrolling
// through the whole history. A tail cut in the middle of an escape
// sequence is tolerated by xterm (the partial sequence is dropped);
// full-screen programs redraw on the attach-time SIGWINCH anyway.
func (s *Session) replayOutput(conn io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > attachReplayTailBytes {
		// 切尾只取最近 attachReplayTailBytes,并把起点对齐到安全边界:
		// 直接截断会把转义序列(OSC/CSI/DCS)拦腰切开,其载荷会被 xterm
		// 当普通文本显示 —— 退出 TUI(opencode 等)后刷新,屏上出现
		// "10;rgb:...""1R10..." 这类乱码。见 alignTailStart。
		data = data[alignTailStart(data, len(data)-attachReplayTailBytes):]
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

// alignTailStart moves the tail-cut start to a safe byte boundary:
//
//   - UTF-8 continuation bytes at the start are skipped, so a multi-byte
//     character cut in half never shows as a corrupted glyph;
//   - when the start is inside (or right after) an escape sequence, it
//     rewinds to the nearest ESC (0x1b, within 1KB): xterm then parses
//     the sequence from its head — its payload (OSC colors, CSI
//     responses, DCS data) is consumed silently instead of being echoed
//     as garbage text. Sequences whose terminator lies beyond the tail
//     end stay in "collecting" state and never render either.
func alignTailStart(b []byte, start int) int {
	if start >= len(b) {
		return start
	}
	for start < len(b) && b[start]&0xC0 == 0x80 {
		start++ // 跳过被切开的 UTF-8 续字节(丢弃半个字符而非显示乱码)
	}
	for i := start - 1; i >= 0 && start-i <= 1024; i-- {
		if b[i] == 0x1b {
			return i
		}
	}
	return start
}

// jitterSize nudges the PTY size (rows-1 → rows), each jump sending a real
// SIGWINCH to the foreground process group so the foreground program
// redraws its whole frame. Manual SIGWINCH does not reach the program
// through the shell; a real TIOCSWINSZ always does. Used after replaying
// history to a restored attach so the tail picture converges with the
// program's true state.
func (s *Session) jitterSize() {
	s.mu.Lock()
	cols, rows := s.lastCols, s.lastRows
	s.mu.Unlock()
	if cols <= 1 || rows <= 1 {
		return
	}
	if err := s.term.Resize(cols, rows-1); err == nil {
		_ = s.term.Resize(cols, rows)
	}
}

// outputPump is the session's single PTY reader, started once at session
// creation. It feeds the output ring and delivers output to the currently
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
			// Client is gone; the next attach replays the ring tail.
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
			// 忽略"1 行"探测尺寸(任何客户端来源):FitAddon 在隐藏容器上
			// 会钳出 rows=1,写进 PTY 会让会话永久只剩一行、无法向下。
			// 真实尺寸(表达布局)的 resize 不会被误伤。
			if args.Columns < 2 || args.Rows < 2 {
				return nil
			}
			if err := s.term.Resize(args.Columns, args.Rows); err != nil {
				return fmt.Errorf("failed to resize terminal: %w", err)
			}
			// 记录最近一次成功尺寸,供恢复会话时 jitterSize 使用。
			s.mu.Lock()
			s.lastCols, s.lastRows = args.Columns, args.Rows
			s.mu.Unlock()
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
