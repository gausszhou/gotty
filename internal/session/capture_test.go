package session

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// M4 attached 引擎的会话层验收:请求/应答配对、分片装配、超时、无附着者、
// 并发占用与客户端报错。

// captureTestSession 起一个带 stub terminal 的会话,并把 net.Pipe 的客户端
// 侧交给 handle(每个服务端写入是一帧,net.Pipe 保证一次 Read 恰好一帧)。
func captureTestSession(t *testing.T, handle func(conn net.Conn)) (*Session, context.CancelFunc) {
	t.Helper()
	stub := newStubTerminal("mock")
	sess := New("cap-test", stub)
	t.Cleanup(func() { stub.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sess.Attach(ctx, server, AttachOptions{PermitWrite: true})
	}()

	go handle(client)

	eventually(t, func() bool { return sess.State() == StateRunning })
	t.Cleanup(func() {
		cancel()
		client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("attach did not return after cancel")
		}
	})
	return sess, cancel
}

// captureAnswerer 只处理 CaptureRequest 帧,按 chunks 分片应答。
func captureAnswerer(t *testing.T, chunks []string, cols, rows int) func(net.Conn) {
	t.Helper()
	return func(conn net.Conn) {
		buf := make([]byte, 1<<16)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if n == 0 || buf[0] != terminal.CaptureRequest {
				continue // 初始化帧(标题/偏好/握手)或其它帧
			}
			for seq, chunk := range chunks {
				frame, err := terminal.EncodeCaptureResult(terminal.CaptureResultFrame{
					Seq: seq, Total: len(chunks), Data: chunk, Cols: cols, Rows: rows,
				})
				if err != nil {
					t.Errorf("encode result: %v", err)
					return
				}
				if _, err := conn.Write(frame); err != nil {
					return
				}
			}
		}
	}
}

func TestCaptureRequiresAttachedClient(t *testing.T) {
	stub := newStubTerminal("mock")
	sess := New("cap-no-client", stub)
	t.Cleanup(func() { stub.Close() })

	_, err := sess.Capture(context.Background(), CaptureOptions{Timeout: 100 * time.Millisecond})
	if err != ErrNoClientAttached {
		t.Fatalf("Capture without a client = %v, want ErrNoClientAttached", err)
	}
}

func TestCaptureRoundTripChunked(t *testing.T) {
	png := []byte("fake-png-bytes")
	b64 := base64.StdEncoding.EncodeToString(png)
	// 人为切成 3 片,验证按 seq 顺序拼接(真实客户端按 24000 字符切片)。
	chunks := []string{b64[:4], b64[4:8], b64[8:]}

	sess, _ := captureTestSession(t, captureAnswerer(t, chunks, 120, 30))

	res, err := sess.Capture(context.Background(), CaptureOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if string(res.PNG) != string(png) {
		t.Errorf("PNG = %q, want %q", res.PNG, png)
	}
	if res.Cols != 120 || res.Rows != 30 {
		t.Errorf("geometry = %dx%d, want 120x30", res.Cols, res.Rows)
	}

	latest := sess.LatestCapture()
	if latest == nil || string(latest.PNG) != string(png) {
		t.Errorf("LatestCapture = %+v, want the round-tripped PNG", latest)
	}
}

func TestCaptureTimeout(t *testing.T) {
	// 客户端只读不应答:请求发出后必须超时,而不是永久挂住。
	sess, _ := captureTestSession(t, func(conn net.Conn) {
		buf := make([]byte, 1<<16)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	})

	start := time.Now()
	_, err := sess.Capture(context.Background(), CaptureOptions{Timeout: 150 * time.Millisecond})
	if err != ErrCaptureTimeout {
		t.Fatalf("Capture = %v, want ErrCaptureTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout took %v, want ~150ms", elapsed)
	}
}

func TestCaptureClientError(t *testing.T) {
	sess, _ := captureTestSession(t, func(conn net.Conn) {
		buf := make([]byte, 1<<16)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if n == 0 || buf[0] != terminal.CaptureRequest {
				continue
			}
			frame, _ := terminal.EncodeCaptureResult(terminal.CaptureResultFrame{Error: "no canvas"})
			_, _ = conn.Write(frame)
		}
	})

	_, err := sess.Capture(context.Background(), CaptureOptions{Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "no canvas") {
		t.Fatalf("Capture = %v, want the client's error message", err)
	}
}

func TestCaptureBusy(t *testing.T) {
	// 第一个请求挂住不应答,第二个必须立刻 ErrCaptureBusy 而不是互相覆盖。
	reqSeen := make(chan struct{}, 1)
	sess, _ := captureTestSession(t, func(conn net.Conn) {
		buf := make([]byte, 1<<16)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if n > 0 && buf[0] == terminal.CaptureRequest {
				select {
				case reqSeen <- struct{}{}:
				default:
				}
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := sess.Capture(ctx, CaptureOptions{Timeout: 5 * time.Second})
		first <- err
	}()

	select {
	case <-reqSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("capture request was never observed by the client")
	}

	if _, err := sess.Capture(context.Background(), CaptureOptions{Timeout: time.Second}); err != ErrCaptureBusy {
		t.Fatalf("second Capture = %v, want ErrCaptureBusy", err)
	}

	cancel()
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("first capture should fail after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first capture did not return after cancel")
	}
}
