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
	// Command is the executable to run (use `sh -c "..."` for shell syntax).
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
	)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("command %q not found: "+
				"use `gotty capture -- sh -c \"...\"` for shell syntax (%w)",
				opts.Command, err)
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
	// tail 保留最近 len(marker)+31 字节,保证跨块查找 marker。
	// marker 命中后不再追踪(快照在下一个 poll 周期触发)。
	const markerSlack = 31
	tail := make([]byte, 0, len(marker)+markerSlack)

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
					tail = append(tail, buf[:n]...)
					if len(tail) > len(marker)+markerSlack {
						tail = tail[len(tail)-len(marker)-markerSlack:]
					}
					if bytes.Index(tail, marker) >= 0 {
						markerHit.Store(true)
					}
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
