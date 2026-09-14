package terminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	// workDir overrides the child process working directory (per-session
	// `cwd`); empty means childWorkDir().
	workDir string

	closeSignal  syscall.Signal
	closeTimeout time.Duration
	size         pty.Winsize

	// rawMode configures the PTY slave as a raw terminal (see WithRawMode).
	rawMode bool

	// proc is the platform PTY + attached process (see ptyProcess):
	// creack/pty on unix, a ConPTY pseudoconsole on Windows.
	proc ptyProcess

	// writeMu serializes writes to the PTY master: attach input
	// (masterToSlave), the agent keys API and emulator query answers
	// all go through Write concurrently; a per-write lock keeps
	// interleaved input from corrupting a frame.
	writeMu sync.Mutex

	// exited is closed once the process has exited and the PTY is closed.
	exited  chan struct{}
	waitMu  sync.Mutex
	waitErr error
	// exitCode is the process exit status, published with waitErr
	// (nil before exit, or when the status is unavailable).
	exitCode *int
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

	proc, err := startProcess(
		command,
		term.args,
		buildEnv(command, term.env),
		term.childWorkDir(),
		term.size.Cols,
		term.size.Rows,
		term.rawMode,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start command `%s`: %w", command, err)
	}
	term.proc = proc

	// 进程退出后不立即关闭 master:PTY slave 挂起(hangup)会让 master
	// 的 read 先返回缓冲中尚未取走的数据、再返回 EIO——读者(驱动/session
	// pump)因此得以收下进程的最后一块输出。若在 cmd.Wait() 返回的同一
	// 瞬间关闭 master,缓冲数据会随 EBADF 一起丢失(读与关闭的竞态,
	// 快速退出的进程最易触发)。master 由 Close()/调用方统一回收。
	//
	// Windows 上的等价契约是 releaseAfterExit():进程退出后关闭
	// pseudoconsole(而不是管道),同样让读者先取走最后一块输出。
	go func() {
		term.waitMu.Lock()
		term.waitErr = proc.Wait()
		term.exitCode = exitCodeOf(term.waitErr)
		term.waitMu.Unlock()
		proc.releaseAfterExit()
		close(term.exited)
	}()

	return term, nil
}

// childWorkDir returns the working directory for this session: the
// per-session override when one was given, otherwise the default (the user's
// home directory — see childWorkDir).
func (t *Terminal) childWorkDir() string {
	if t.workDir != "" {
		return t.workDir
	}
	return childWorkDir()
}

// exitCodeOf extracts the process exit status from a Wait error: 0 on a clean
// exit, the code from an *exec.ExitError, nil when unavailable.
func exitCodeOf(err error) *int {
	if err == nil {
		code := 0
		return &code
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

// buildEnv composes the process environment: the current environment,
// a TERM value (defaulting to "xterm-256color"), COLORTERM=truecolor
// (native 24-bit, which xterm.js renders) and the configured overrides.
// Overrides listed later win over the defaults and never duplicate keys.
//
// COLORTERM=truecolor lets TUIs (neovim, lazygit, fzf, …) enable 24-bit
// colors — xterm.js renders them natively, so the capability always holds.
// Live OSC 10/11 queries (vim's t_RB, tmux background detection) are
// answered by the xterm.js side, so programs that ask adapt to whatever
// the page currently renders without server-side plumbing.
//
// For bash sessions it additionally injects PROMPT_COMMAND, which runs just
// before every prompt is shown and does two things:
//
//   - Drops the newline escape at the start of Git Bash's default prompt. That
//     prompt (/etc/profile.d/git-prompt.sh) literally begins with '\n', so every
//     prompt is preceded by an empty line — harmless in a native terminal, but in
//     the web terminal it just eats a row under the tab bar. The strip is guarded
//     so it only fires while the prompt still carries the Git Bash title
//     terminator followed by that newline, which makes it idempotent (a second run
//     cannot eat the newline before "$ ") and leaves a user's own prompt alone.
//   - Resets terminal modes, so a TUI that died (or was killed) without restoring
//     the terminal leaves nothing behind:
//   - mouse reporting (?1000l/?1002l/?1003l/?1006l) — otherwise the shell
//     echoes SGR mouse bytes (ESC[<b;x;yM) from clicks as garbage;
//   - cursor show (?25h) and bracketed paste off (?2004l).
//
// It does NOT send ?1049l (leave the alternate screen): bash never enters
// the alternate screen, yet xterm.js treats ?1049l as "switch back to the
// main buffer and restore the saved cursor position". Sent before every
// prompt, it yanks the cursor to the saved (stale) position, so the fresh
// prompt is drawn onto a line of earlier output — the "prompt + file line
// merged" corruption seen after long listings. Full-screen TUIs (vim,
// htop) leave and enter the alternate screen on their own, so no prompt
// hook is needed for them.
func buildEnv(command string, extra []string) []string {
	termValue := "xterm-256color"
	colorTermValue := "truecolor"
	// 默认注入 PROMPT_COMMAND;用户显式配置时以用户为准
	promptReset := true
	extras := make([]string, 0, len(extra))
	for _, kv := range extra {
		switch {
		case strings.HasPrefix(kv, "TERM="):
			termValue = strings.TrimPrefix(kv, "TERM=")
		case strings.HasPrefix(kv, "COLORTERM="):
			colorTermValue = strings.TrimPrefix(kv, "COLORTERM=")
		case strings.HasPrefix(kv, "PROMPT_COMMAND="):
			promptReset = false
			extras = append(extras, kv)
		default:
			extras = append(extras, kv)
		}
	}

	original := os.Environ()
	env := make([]string, 0, len(original)+len(extras)+4)
	for _, kv := range original {
		// 颜色相关的继承变量一律剥离,统一以本层默认/覆盖值为准
		if !strings.HasPrefix(kv, "TERM=") &&
			!strings.HasPrefix(kv, "COLORTERM=") {
			env = append(env, kv)
		}
	}
	env = append(env, "TERM="+termValue)
	env = append(env, "COLORTERM="+colorTermValue)

	if isBash(command) && promptReset {
		// bash 每次显示提示符前执行两件事:
		//   1. 去掉 Git Bash 默认提示符开头的换行转义(见 buildEnv 文档注释);
		//      守卫条件使这一步幂等,并且不碰用户自己写的提示符。
		//   2. printf 发送终端模式复位序列:关闭鼠标上报 + 显示光标 + 关闭括号粘贴,
		//      覆盖 TUI 退出/被杀未复位导致的残留画面、鼠标乱字节与隐藏光标。
		env = append(env,
			"PROMPT_COMMAND=[[ $PS1 == *'\\007\\]\\n'* ]] && PS1=${PS1/\\\\n/}; "+
				"printf '\\033[?1000l\\033[?1002l\\033[?1003l\\033[?1006l\\033[?25h\\033[?2004l'")
	}

	return append(env, extras...)
}

// isBash reports whether the command is the bash shell (interactive
// sessions; nested bash inherits the injected PROMPT_COMMAND as well).
//
// It compares the base name instead of testing a "/bash" suffix: on Windows the
// command arrives as `D:\...\Git\usr\bin\bash.exe`, which a forward-slash suffix
// test silently fails to match — and that miss disabled the PROMPT_COMMAND
// injection entirely for the default Git Bash session on Windows (the default
// command is $SHELL, an absolute Windows path).
func isBash(command string) bool {
	base := strings.ToLower(filepath.Base(command))
	return base == "bash" || base == "bash.exe"
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
	return t.proc.PID()
}

// Read reads raw output from the PTY.
func (t *Terminal) Read(p []byte) (int, error) {
	return t.proc.Read(p)
}

// Write writes raw input to the PTY.
func (t *Terminal) Write(p []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.proc.Write(p)
}

// Size returns the terminal size last configured via WithInitialSize or
// Resize. Zero values mean the size was never set explicitly (the PTY
// then uses the platform default).
func (t *Terminal) Size() (cols, rows int) {
	return int(t.size.Cols), int(t.size.Rows)
}

// Resize sets the size of the PTY.
func (t *Terminal) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid terminal size: %dx%d", cols, rows)
	}
	t.size = pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}
	return t.proc.Resize(t.size.Cols, t.size.Rows)
}

// Signal sends a signal to the process, if it is still running.
func (t *Terminal) Signal(sig syscall.Signal) error {
	if t.Exited() {
		return fmt.Errorf("%w: cannot signal an exited process", ErrTerminalClosed)
	}
	return t.proc.Signal(sig)
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

// ExitCode returns the process exit status once the process has exited and
// nil before that (or when the status is unavailable). 0 means a clean exit.
func (t *Terminal) ExitCode() *int {
	select {
	case <-t.exited:
	default:
		return nil
	}
	t.waitMu.Lock()
	defer t.waitMu.Unlock()
	return t.exitCode
}

// Wait blocks until the process has exited.
func (t *Terminal) Wait() error {
	<-t.exited
	t.waitMu.Lock()
	defer t.waitMu.Unlock()
	return t.waitErr
}

// Close asks the process to exit. It sends the close signal (SIGHUP by
// default) to the whole process group, then escalates to SIGKILL after
// closeTimeout. The PTY is closed once the process exits.
// Closing an already-exited terminal is a no-op.
//
// The process-group signal matters: the SIGHUP disposition (SIG_IGN)
// is inherited from parents started under nohup / non-interactive
// shells, so a plain per-process signal can be silently dropped and
// cmd.Wait() would block forever. The bounded SIGKILL escalation is
// therefore the safe default (see DefaultCloseTimeout).
func (t *Terminal) Close() error {
	if t.Exited() {
		return nil
	}

	if t.proc != nil {
		if err := t.proc.Signal(t.closeSignal); err != nil {
			return fmt.Errorf("failed to send signal %v: %w", t.closeSignal, err)
		}
	}

	if t.closeTimeout >= 0 {
		select {
		case <-t.exited:
			return nil
		case <-time.After(t.closeTimeout):
			// 优雅信号未生效(SIGHUP 被忽略等):SIGKILL 升级到整个进程组
			if err := t.proc.Kill(); err != nil {
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
		"pid":     t.proc.PID(),
	}
}
