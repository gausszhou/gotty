package utils

import (
	"fmt"
	"os"
	"sync"
)

// RotatingWriter 是按大小轮转的日志写入器:一个当前文件 + 最多 maxBackups 个
// 历史备份(path.1 最新 … path.N 最旧)。
//
// 为什么需要:服务端日志是纯追加的,而客户端每 2s 一次的状态心跳会持续产生
// 访问日志 —— 一个页面开着就能写 ~100KB/小时,不轮转就是无限增长。
//
// 轮转时机在"写入之前":若 当前大小 + 本次写入 会超过 maxSize,就先把
// path.N 丢弃,再把 path.N-1…path.1 依次后移,最后 path → path.1,
// 然后以截断方式重开 path。
//
// 两个边界:
//   - maxSize <= 0 或 maxBackups <= 0:只写不轮转(留一个关掉轮转的开关)。
//   - 单次写入本身就大于 maxSize:直接写入、不轮转。否则每次写入都会先触发
//     轮转,日志里将一条都留不下(死循环式的自我清空)。
//
// 并发:log 包自身对输出加了锁,这里再加一层,防止有人绕过 log 直接写。
type RotatingWriter struct {
	path       string
	maxSize    int64
	maxBackups int

	mu   sync.Mutex
	file *os.File
	size int64
}

// NewRotatingWriter 以追加模式打开 path(不存在则创建)。父目录由调用方保证
// (setupLogFile 已经 MkdirAll,那样报错信息更具体)。
func NewRotatingWriter(path string, maxSize int64, maxBackups int) (*RotatingWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &RotatingWriter{
		path:       path,
		maxSize:    maxSize,
		maxBackups: maxBackups,
		file:       file,
		size:       info.Size(),
	}, nil
}

// Write 实现 io.Writer:必要时先轮转再写。
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, os.ErrClosed
	}
	// w.size > 0 的额外判断:空文件不因"单条太长"而反复轮转。
	if w.rotates() && w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关闭当前文件;之后再调用 Write 返回 os.ErrClosed。
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// BackupPath 返回第 i 个历史文件的路径(i 从 1 开始)。
func (w *RotatingWriter) BackupPath(i int) string {
	return fmt.Sprintf("%s.%d", w.path, i)
}

func (w *RotatingWriter) rotates() bool {
	return w.maxSize > 0 && w.maxBackups > 0
}

// rotate 关闭当前文件、把备份整体后移一位(path.N 丢弃),再截断重开 path。
func (w *RotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil

	// 最旧的一份直接丢弃;不存在时的错误可以忽略。
	_ = os.Remove(w.BackupPath(w.maxBackups))

	for i := w.maxBackups - 1; i >= 1; i-- {
		src := w.BackupPath(i)
		if _, err := os.Stat(src); err != nil {
			continue // 备份还没攒够这么多份
		}
		if err := os.Rename(src, w.BackupPath(i+1)); err != nil {
			return err
		}
	}
	if err := os.Rename(w.path, w.BackupPath(1)); err != nil && !os.IsNotExist(err) {
		return err
	}

	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file = file
	w.size = 0
	return nil
}
