package capture

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// Options parameterizes one capture run.
type Options struct {
	// Command is the executable to run; for shell syntax wrap it in a shell
	// (see shellSyntaxHint: `sh -c` on unix, `cmd /c` on Windows).
	Command string
	Args    []string

	// Cols/Rows fix the terminal size (defaults 120x30 when unset).
	Cols int
	Rows int

	// CellW/CellH are the pixel size of one grid cell (defaults 9x18),
	// used to convert graphics-protocol placements into grid cells.
	CellW int
	CellH int

	// WaitMs captures the screen when output has been silent for at least
	// this many milliseconds (0 disables the quiet stop).
	WaitMs int

	// Timeout bounds the whole run; on expiry the current screen is
	// returned with TimedOut set (0 disables it — not recommended).
	Timeout time.Duration

	// Marker stops the run as soon as this string appears in the output
	// stream (searched across chunk boundaries).
	Marker string
}

// StopReason says why the snapshot was taken.
type StopReason string

const (
	// StopExit: the process exited.
	StopExit StopReason = "exit"
	// StopQuiet: output went silent for WaitMs.
	StopQuiet StopReason = "quiet"
	// StopMarker: the marker string appeared.
	StopMarker StopReason = "marker"
	// StopTimeout: Timeout elapsed before any other condition.
	StopTimeout StopReason = "timeout"
)

// Result is one capture run: the emulator state plus the stop condition.
type Result struct {
	Emulator   *Emulator
	ExitCode   *int // set when the process exited before the snapshot
	TimedOut   bool
	StopReason StopReason
	Duration   time.Duration
}

// markerOverlap is how many bytes of the previous read have to be kept so that
// a marker split across two reads is still matched: a marker of length n can
// straddle a boundary by at most n-1 bytes.
func markerOverlap(marker []byte) int {
	if len(marker) == 0 {
		return 0
	}
	return len(marker) - 1
}

// Run executes the command in a PTY of the requested size, feeds the
// output into the emulator and snapshots the screen when the stop
// condition is met (process exit, output silence, marker or timeout).
// On non-exit stops the process group is reaped (SIGHUP, then SIGKILL).
func Run(opts Options) (*Result, error) {
	cols, rows := opts.Cols, opts.Rows
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 30
	}
	if opts.WaitMs < 0 {
		opts.WaitMs = 0
	}

	emu := NewEmulator(cols, rows)
	emu.SetCellSize(opts.CellW, opts.CellH)
	term, err := terminal.New(opts.Command, opts.Args,
		terminal.WithInitialSize(cols, rows),
		terminal.WithCloseTimeout(2*time.Second),
		// raw:无回显/规范缓冲,查询应答写回程序而非回显到抓取侧
		terminal.WithRawMode(),
	)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("command %q not found: "+
				"use `gotty capture -- %s` for shell syntax (%w)",
				opts.Command, shellSyntaxHint, err)
		}
		return nil, err
	}

	start := time.Now()
	// 无论以何种条件停止,未退出的进程都要回收(进程组 SIGHUP → SIGKILL)。
	defer func() {
		if !term.Exited() {
			_ = term.Close()
		}
	}()

	var lastOutNs atomic.Int64
	lastOutNs.Store(start.UnixNano())
	var anyOutput atomic.Bool
	var markerHit atomic.Bool

	marker := []byte(opts.Marker)
	// overlap 保存上一次读到数据的末尾 len(marker)-1 字节:marker 被拆到两次
	// 读之间时靠它补齐,这个长度是充分必要的。
	// marker 命中后不再追踪(快照在下一个 poll 周期触发)。
	overlap := make([]byte, 0, markerOverlap(marker))
	// scan 是查找用的拼接缓冲,复用以免每次读都分配(读缓冲是 32KB)。
	scan := make([]byte, 0, 32*1024+len(marker))

	// readerDone 在进程退出时送达退出码;其他停止条件下进程仍活着。
	readerDone := make(chan int, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := term.Read(buf)
			if n > 0 {
				emu.Write(buf[:n])
				// 把仿真器生成的终端查询应答写回 PTY,否则 vim/htop
				// 等程序会阻塞在等待应答上。错误忽略(程序可能已退出)。
				if ans := emu.DrainAnswers(); len(ans) > 0 {
					_, _ = term.Write(ans)
				}
				lastOutNs.Store(time.Now().UnixNano())
				anyOutput.Store(true)
				if len(marker) > 0 && !markerHit.Load() {
					// 必须先查再截:"上一块尾巴 + 本块"整体参与查找。反过来
					// (先截到滑动窗口再查)会把本块里明明存在的 marker 截掉——
					// ConPTY 会在文本后追加较长的 OSC 标题/光标序列,窗口装不下
					// 尾部就漏检,实测 `echo abc` 配 marker "ab" 必漏。
					scan = append(scan[:0], overlap...)
					scan = append(scan, buf[:n]...)
					if bytes.Index(scan, marker) >= 0 {
						markerHit.Store(true)
					}
					// 只留最后 len(marker)-1 字节给下一次拼接。
					if keep := markerOverlap(marker); len(scan) > keep {
						scan = scan[len(scan)-keep:]
					}
					overlap = append(overlap[:0], scan...)
				}
			}
			if rerr != nil {
				// PTY 已关闭:进程退出。取退出码(信号退出为 -1)。
				code := 0
				waitErr := term.Wait()
				var ee *exec.ExitError
				if errors.As(waitErr, &ee) {
					code = ee.ExitCode()
				}
				readerDone <- code
				return
			}
		}
	}()

	var timeoutCh <-chan time.Time
	if opts.Timeout > 0 {
		timer := time.NewTimer(opts.Timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()

	result := &Result{Emulator: emu, StopReason: StopExit}
loop:
	for {
		select {
		case code := <-readerDone:
			result.ExitCode = &code
			result.StopReason = StopExit
			break loop
		case <-timeoutCh:
			result.TimedOut = true
			result.StopReason = StopTimeout
			break loop
		case <-poll.C:
			if markerHit.Load() {
				result.StopReason = StopMarker
				break loop
			}
			if opts.WaitMs > 0 && anyOutput.Load() &&
				time.Since(time.Unix(0, lastOutNs.Load())) >= time.Duration(opts.WaitMs)*time.Millisecond {
				result.StopReason = StopQuiet
				break loop
			}
		}
	}
	result.Duration = time.Since(start)
	return result, nil
}
