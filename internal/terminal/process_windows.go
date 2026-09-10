//go:build windows

package terminal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// conptyProcess runs the session command on a Windows pseudoconsole
// (ConPTY, Windows 10 1809+): a pseudoconsole handle plus the process
// created attached to it.
//
// Windows needs its own implementation because creack/pty — the library
// behind the unix path — has no Windows backend at all: its
// start_windows.go returns ErrUnsupported from StartWithSize, so every
// session creation on Windows failed with "failed to start command `...`:
// unsupported". The pieces below are the ConPTY contract from Microsoft's
// "Creating a Pseudoconsole session": CreatePseudoConsole over a pair of
// pipes, the pseudoconsole handle passed through the
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE process attribute, then CreateProcess.
type conptyProcess struct {
	console windows.Handle // HPCON; closing it ends the attached process tree
	proc    *os.Process    // raw CreateProcess handle wrapped for Wait/exit code
	in      *os.File       // app → pseudoconsole (the child's stdin)
	out     *os.File       // pseudoconsole → app (the child's stdout/stderr)

	// The three teardowns below race (the exit path, a destroy, an idle
	// timeout), and each must happen at most once: closing the same handle
	// twice can free an unrelated recycled handle.
	consoleOnce sync.Once
	inOnce      sync.Once
	outOnce     sync.Once
}

// defaultCols/defaultRows size the pseudoconsole screen buffer when the
// caller did not ask for a size. gotty's default is "dynamic, resized on
// attach" (Width/Height 0), but CreatePseudoConsole requires a real size to
// allocate its buffer, so sessions start at the classic 80x24 and the
// browser's first fit() resizes them.
const (
	defaultCols = 80
	defaultRows = 24
)

// The three ConPTY entry points, resolved explicitly so they can be probed
// before use (see requireConPTY).
var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procCreatePseudoConsole = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole  = kernel32.NewProc("ClosePseudoConsole")
)

// requireConPTY reports a clear error when this Windows predates ConPTY
// (introduced in Windows 10 1809). The probe has to happen before any ConPTY
// call: x/sys resolves these procedures lazily and *panics* when they are
// missing (LazyProc.Addr calls mustFind), so on an older Windows 10 the process
// would die with a panic instead of telling the user what is wrong.
func requireConPTY() error {
	for _, proc := range []*windows.LazyProc{
		procCreatePseudoConsole,
		procResizePseudoConsole,
		procClosePseudoConsole,
	} {
		if err := proc.Find(); err != nil {
			return fmt.Errorf("ConPTY is unavailable on this Windows version "+
				"(requires Windows 10 1809 or newer): %w", err)
		}
	}
	return nil
}

// startProcess starts command on a new pseudoconsole (see ptyProcess).
//
// rawMode is a termios concept (ECHO/ICANON on the slave) that ConPTY does
// not expose — the console host owns line discipline and echo here.
func startProcess(
	command string,
	args, env []string,
	dir string,
	cols, rows uint16,
	rawMode bool,
) (ptyProcess, error) {
	_ = rawMode

	if err := requireConPTY(); err != nil {
		return nil, err
	}

	if cols == 0 || rows == 0 {
		cols, rows = defaultCols, defaultRows
	}
	size := windows.Coord{X: int16(cols), Y: int16(rows)}

	// 通信管道。CreatePseudoConsole 拿到的是"输入管道的读端"和"输出管道
	// 的写端";本进程保留另一端(input 写、output 读)与子进程通信。
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("failed to create the pty input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		closeHandles(inputRead, inputWrite)
		return nil, fmt.Errorf("failed to create the pty output pipe: %w", err)
	}

	var console windows.Handle
	if err := windows.CreatePseudoConsole(size, inputRead, outputWrite, 0, &console); err != nil {
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		if errors.Is(err, windows.ERROR_PROC_NOT_FOUND) {
			return nil, fmt.Errorf("ConPTY is unavailable on this Windows version "+
				"(requires Windows 10 1809 or newer): %w", err)
		}
		return nil, fmt.Errorf("failed to create the pseudoconsole: %w", err)
	}

	proc, err := startAttached(command, args, env, dir, console)
	// 交出去的句柄归 pseudoconsole 持有,本进程的副本必须在这里释放:
	// 否则我们仍握着输出管道的写端,子进程退出时读端看不到 broken pipe
	// (官方文档:"the handles given during creation should be freed from
	// this process")。
	closeHandles(inputRead, outputWrite)
	if err != nil {
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, err
	}

	// pi 的进程句柄换成 os.Process:Wait 需要它来等待退出并取退出码
	// (openProcess 请求 SYNCHRONIZE,足以 WaitForSingleObject)。
	handle, err := os.FindProcess(int(proc.ProcessId))
	closeHandles(proc.Process, proc.Thread)
	if err != nil {
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, fmt.Errorf("failed to open the started process: %w", err)
	}

	return &conptyProcess{
		console: console,
		proc:    handle,
		in:      os.NewFile(uintptr(inputWrite), "conpty-input"),
		out:     os.NewFile(uintptr(outputRead), "conpty-output"),
	}, nil
}

// startAttached creates the child process attached to console through the
// pseudoconsole process attribute. Extracted from startProcess to keep the
// handle bookkeeping in one place.
func startAttached(
	command string,
	args, env []string,
	dir string,
	console windows.Handle,
) (windows.ProcessInformation, error) {
	var pi windows.ProcessInformation

	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{command}, args...)))
	if err != nil {
		return pi, err
	}

	var workDir *uint16
	if dir != "" {
		if workDir, err = windows.UTF16PtrFromString(dir); err != nil {
			return pi, err
		}
	}

	envBlock, err := newEnvBlock(env)
	if err != nil {
		return pi, err
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return pi, fmt.Errorf("failed to allocate the process attribute list: %w", err)
	}
	defer attrs.Delete()

	// 属性表存的是 pseudoconsole 句柄——注意 PROC_THREAD_ATTRIBUTE_
	// PSEUDOCONSOLE 是"取值型"属性:UpdateProcThreadAttribute 的 lpValue
	// 就是 HPCON 本身,而不是句柄的指针(对比 HANDLE_LIST 这类"指针型"
	// 属性,lpValue 才是数组地址)。传句柄的地址会让子进程拿到一个栈
	// 地址当 pseudoconsole,进程立刻以 0xc0000142 退出。
	if err := attrs.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		pseudoConsoleAttribute(console),
		unsafe.Sizeof(console),
	); err != nil {
		return pi, fmt.Errorf("failed to set the pseudoconsole process attribute: %w", err)
	}

	si := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			// 关键:CreateProcess 默认把父进程的 std 句柄复制给子进程
			// (bInheritHandles 只管其他句柄的继承)。gotty 常从终端里
			// 启动,那些句柄有效,于是 cmd.exe 会把 banner/提示符写进
			// gotty 自己的控制台,而不是 pseudoconsole —— 浏览器里只剩
			// 一串光标/模式控制序列。置位 STARTF_USESTDHANDLES 且三个
			// 句柄留空,子进程就拿不到继承句柄,控制台应用改用它所附加
			// 的 pseudoconsole(与 Windows Terminal 的情形一致)。
			Flags: windows.STARTF_USESTDHANDLES,
		},
		ProcThreadAttributeList: attrs.List(),
	}

	// inheritHandles=false:子进程的 std 句柄由 pseudoconsole 建立,
	// 不通过继承传递(官方示例同样为 FALSE)。
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT)
	if envBlock != nil {
		flags |= uint32(windows.CREATE_UNICODE_ENVIRONMENT)
	}
	if err := windows.CreateProcess(
		nil,
		cmdline,
		nil,
		nil,
		false,
		flags,
		envBlock,
		workDir,
		&si.StartupInfo,
		&pi,
	); err != nil {
		// "找不到可执行文件" 归一为 exec.ErrNotFound:unix 侧的 exec.LookPath
		// 就是这么报的,调用方(如 capture 的 "not found + 提示 shell 语法"
		// 分支)按 exec.ErrNotFound 判断。CreateProcess 直接给出 Win32 errno,
		// 不归一的话这条友好提示在 Windows 上永远不会触发——实测如此
		// (用户只看到 "The system cannot find the file specified.")。
		// 多重 %w 保留原始 errno,路径/权限等原因仍可读。
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
			errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return pi, fmt.Errorf("failed to start the process on the pseudoconsole: %w (%w)",
				exec.ErrNotFound, err)
		}
		return pi, fmt.Errorf("failed to start the process on the pseudoconsole: %w", err)
	}

	return pi, nil
}

// pseudoConsoleAttribute reinterprets the pseudoconsole handle as the
// pointer UpdateProcThreadAttribute expects for a value-type attribute.
//
// The handle goes through a *unsafe.Pointer rather than the forbidden
// uintptr-to-unsafe.Pointer conversion; the resulting pointer is only ever
// handed to the API as an opaque attribute value and is never dereferenced.
func pseudoConsoleAttribute(hpc windows.Handle) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&hpc))
}

// newEnvBlock renders env as the UTF-16, double-NUL-terminated environment
// block CreateProcess expects with CREATE_UNICODE_ENVIRONMENT. A nil block
// means "inherit the parent environment".
//
// Entries are deduplicated case-insensitively (the later entry wins, which
// is the contract buildEnv documents for overrides) and sorted, as the
// environment block is required to be: Windows itself tolerates an unsorted
// block, but a shell that binary-searches it does not.
func newEnvBlock(env []string) (*uint16, error) {
	if len(env) == 0 {
		return nil, nil
	}

	at := make(map[string]int, len(env))
	deduped := make([]string, 0, len(env))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		upper := strings.ToUpper(key)
		if prev, ok := at[upper]; ok {
			deduped[prev] = kv
			continue
		}
		at[upper] = len(deduped)
		deduped = append(deduped, kv)
	}
	sort.Slice(deduped, func(i, j int) bool {
		return strings.ToUpper(deduped[i]) < strings.ToUpper(deduped[j])
	})

	block := utf16.Encode([]rune(strings.Join(deduped, "\x00") + "\x00\x00"))
	if len(block) == 0 {
		return nil, nil
	}
	return &block[0], nil
}

// Read reads terminal output. The channel only breaks once the process has
// exited *and* releaseAfterExit closed the pseudoconsole (the console host
// holds its own copy of the pipe), so a broken channel means the session is
// over: the error is normalized to io.EOF and the read end is released here,
// because the reader will not call Read again.
func (p *conptyProcess) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err != nil {
		p.closeOut()
		if isChannelClosed(err) {
			return n, io.EOF
		}
		return n, err
	}
	return n, nil
}

func (p *conptyProcess) Write(b []byte) (int, error) {
	n, err := p.in.Write(b)
	if err != nil && isChannelClosed(err) {
		p.closeIn()
	}
	return n, err
}

func (p *conptyProcess) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("invalid terminal size: %dx%d", cols, rows)
	}
	if err := windows.ResizePseudoConsole(p.console, windows.Coord{
		X: int16(cols),
		Y: int16(rows),
	}); err != nil {
		return fmt.Errorf("failed to resize the pseudoconsole: %w", err)
	}
	return nil
}

func (p *conptyProcess) PID() int { return p.proc.Pid }

// Signal degrades to termination: Windows has no process-group signals, and
// ConPTY has no "ask politely" step — the closest thing to what the unix
// path achieves with SIGHUP-then-SIGKILL is closing the pseudoconsole, which
// by contract terminates the attached process and everything else in its
// tree. Any configured close signal therefore ends the session immediately
// instead of being delivered.
func (p *conptyProcess) Signal(_ syscall.Signal) error { return p.terminate() }

func (p *conptyProcess) Kill() error { return p.terminate() }

func (p *conptyProcess) Close() error { return p.terminate() }

// Wait blocks until the process exits. A non-zero exit is reported as
// *exec.ExitError so callers (capture/driver.go) can read the code.
func (p *conptyProcess) Wait() error {
	state, err := p.proc.Wait()
	if err != nil {
		return err
	}
	if state.ExitCode() != 0 {
		return &exec.ExitError{ProcessState: state}
	}
	return nil
}

// releaseAfterExit closes the pseudoconsole so the output channel breaks and
// the reader can drain the tail and finish. Closing the pseudoconsole also
// terminates whatever is still attached to it (a shell killed while a child
// ran leaves that child behind otherwise).
func (p *conptyProcess) releaseAfterExit() {
	p.closeConsole()
	p.closeIn()
}

// terminate ends the session: the root process is terminated and the
// pseudoconsole is closed, which also takes down the rest of the tree
// (children attached to the console). Closing the pipes afterwards releases
// the read end even if no reader is draining it — a reader that is draining
// gets the channel break first, because the console is closed before the
// handles here. Idempotent.
func (p *conptyProcess) terminate() error {
	// 进程已退出(自然退出后又被 Close/空闲淘汰调用)是正常的重复
	// terminate,不算错误;Kill 的失败仍然如实返回,与 unix 侧
	// "信号发送失败即 Close 失败" 的口径一致。
	killErr := p.proc.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	p.closeConsole()
	p.closeIn()
	p.closeOut()
	return killErr
}

func (p *conptyProcess) closeConsole() {
	p.consoleOnce.Do(func() { windows.ClosePseudoConsole(p.console) })
}
func (p *conptyProcess) closeIn()  { p.inOnce.Do(func() { _ = p.in.Close() }) }
func (p *conptyProcess) closeOut() { p.outOnce.Do(func() { _ = p.out.Close() }) }

// isChannelClosed reports whether err means the pipe is gone (the process
// exited, the pseudoconsole was closed, or the handle was released under a
// blocked read) rather than a real I/O failure worth surfacing.
func isChannelClosed(err error) bool {
	for _, closed := range []error{
		io.EOF,
		io.ErrClosedPipe,
		os.ErrClosed,
		windows.ERROR_BROKEN_PIPE,
		windows.ERROR_HANDLE_EOF,
		windows.ERROR_OPERATION_ABORTED,
		windows.ERROR_INVALID_HANDLE,
		windows.ERROR_PIPE_NOT_CONNECTED,
	} {
		if errors.Is(err, closed) {
			return true
		}
	}
	return false
}

// closeHandles releases handles, reporting the first failure. InvalidHandle
// entries (a pipe creation that never happened) are skipped.
func closeHandles(handles ...windows.Handle) error {
	var first error
	for _, h := range handles {
		if h == 0 || h == windows.InvalidHandle {
			continue
		}
		if err := windows.CloseHandle(h); err != nil && first == nil {
			first = err
		}
	}
	return first
}
