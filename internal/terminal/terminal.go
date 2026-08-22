package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Terminal runs a command inside a PTY and bridges raw bytes between
// the PTY and an io.Reader/io.Writer peer (typically a WebSocket connection).
//
// It knows nothing about sessions or HTTP: it only manages the process.
type Terminal struct {
	command string
	args    []string
	env     []string

	closeSignal  syscall.Signal
	closeTimeout time.Duration
	size         pty.Winsize

	cmd *exec.Cmd
	pty *os.File

	// exited is closed once the process has exited and the PTY is closed.
	exited  chan struct{}
	waitMu  sync.Mutex
	waitErr error
}

// New starts command with args inside a new PTY.
// The first option is applied after the defaults:
// CloseSignal defaults to SIGHUP and the kill escalation is disabled.
func New(command string, args []string, options ...Option) (*Terminal, error) {
	term := &Terminal{
		command:      command,
		args:         append([]string{}, args...),
		closeSignal:  DefaultCloseSignal,
		closeTimeout: DefaultCloseTimeout,
		exited:       make(chan struct{}),
	}

	for _, option := range options {
		option(term)
	}

	cmd := exec.Command(command, term.args...)
	cmd.Env = buildEnv(command, term.env)
	term.cmd = cmd

	var ptmx *os.File
	var err error
	if term.size.Cols > 0 && term.size.Rows > 0 {
		ptmx, err = pty.StartWithSize(cmd, &term.size)
	} else {
		ptmx, err = pty.Start(cmd)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to start command `%s`: %w", command, err)
	}
	term.pty = ptmx

	// When the process exits, close the PTY so that Read() breaks with EOF.
	go func() {
		term.waitMu.Lock()
		term.waitErr = cmd.Wait()
		term.waitMu.Unlock()
		ptmx.Close()
		close(term.exited)
	}()

	return term, nil
}

// buildEnv composes the process environment: the current environment,
// a TERM value (defaulting to "xterm-256color") and the configured overrides.
// Overrides listed later win over the defaults and never duplicate keys.
//
// For bash sessions it additionally injects PROMPT_COMMAND so that the mouse
// reporting modes (DECSET 1000/1002/1003/1006) are reset just before every
// prompt is shown. This cleans up after full-screen TUIs that enable mouse
// reporting and die (or are killed) without sending the reset sequence:
// otherwise the shell keeps echoing SGR mouse bytes (ESC[<b;x;yM) caused by
// clicks, which appear as garbage on the terminal.
func buildEnv(command string, extra []string) []string {
	termValue := "xterm-256color"
	// 默认注入 PROMPT_COMMAND;用户显式配置时以用户为准
	promptReset := true
	extras := make([]string, 0, len(extra))
	for _, kv := range extra {
		switch {
		case strings.HasPrefix(kv, "TERM="):
			termValue = strings.TrimPrefix(kv, "TERM=")
		case strings.HasPrefix(kv, "PROMPT_COMMAND="):
			promptReset = false
			extras = append(extras, kv)
		default:
			extras = append(extras, kv)
		}
	}

	original := os.Environ()
	env := make([]string, 0, len(original)+len(extras)+2)
	for _, kv := range original {
		if !strings.HasPrefix(kv, "TERM=") {
			env = append(env, kv)
		}
	}
	env = append(env, "TERM="+termValue)

	if isBash(command) && promptReset {
		// bash 每次显示提示符前执行 printf,发送鼠标模式复位序列。
		// 覆盖 TUI 退出/被杀未复位 ?1000h 等导致鼠标字节被 echo 成乱码的场景。
		env = append(env,
			"PROMPT_COMMAND=printf '\\033[?1000l\\033[?1002l\\033[?1003l\\033[?1006l'")
	}

	return append(env, extras...)
}

// isBash reports whether the command is the bash shell (interactive
// sessions; nested bash inherits the injected PROMPT_COMMAND as well).
func isBash(command string) bool {
	return command == "bash" ||
		strings.HasSuffix(command, "/bash") ||
		strings.HasSuffix(command, "/bash.exe")
}

// Command returns the command name of the process.
func (t *Terminal) Command() string {
	return t.command
}

// Args returns the command arguments of the process.
func (t *Terminal) Args() []string {
	return t.args
}

// PID returns the process id.
func (t *Terminal) PID() int {
	return t.cmd.Process.Pid
}

// Read reads raw output from the PTY.
func (t *Terminal) Read(p []byte) (int, error) {
	return t.pty.Read(p)
}

// Write writes raw input to the PTY.
func (t *Terminal) Write(p []byte) (int, error) {
	return t.pty.Write(p)
}

// Resize sets the size of the PTY.
func (t *Terminal) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid terminal size: %dx%d", cols, rows)
	}
	return pty.Setsize(t.pty, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
}

// Signal sends a signal to the process, if it is still running.
func (t *Terminal) Signal(sig syscall.Signal) error {
	if t.Exited() {
		return fmt.Errorf("%w: cannot signal an exited process", ErrTerminalClosed)
	}
	return t.cmd.Process.Signal(sig)
}

// Exited reports whether the process has exited (non-blocking).
func (t *Terminal) Exited() bool {
	select {
	case <-t.exited:
		return true
	default:
		return false
	}
}

// Wait blocks until the process has exited.
func (t *Terminal) Wait() error {
	<-t.exited
	t.waitMu.Lock()
	defer t.waitMu.Unlock()
	return t.waitErr
}

// Close asks the process to exit: it sends closeSignal, then escalates
// to SIGKILL after closeTimeout. The PTY is closed once the process exits.
// Closing an already-exited terminal is a no-op.
func (t *Terminal) Close() error {
	if t.Exited() {
		return nil
	}

	if t.cmd.Process != nil {
		if err := t.cmd.Process.Signal(t.closeSignal); err != nil {
			return fmt.Errorf("failed to send signal %v: %w", t.closeSignal, err)
		}
	}

	if t.closeTimeout >= 0 {
		select {
		case <-t.exited:
			return nil
		case <-time.After(t.closeTimeout):
			if err := t.cmd.Process.Signal(syscall.SIGKILL); err != nil {
				return fmt.Errorf("failed to send SIGKILL: %w", err)
			}
			<-t.exited
		}
		return nil
	}

	<-t.exited
	return nil
}

// WindowTitleVariables returns values that can be used to fill out
// the window title of a terminal.
func (t *Terminal) WindowTitleVariables() map[string]interface{} {
	return map[string]interface{}{
		"command": t.command,
		"argv":    t.args,
		"pid":     t.cmd.Process.Pid,
	}
}
