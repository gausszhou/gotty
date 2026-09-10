package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 状态心跳(POST /api/sessions/status)不写访问日志,其余请求照写。
// 心跳日志曾经是日志体积的唯一大头(每 2s 一行,一个页面约 100KB/小时)。
func TestWrapLoggerSkipsStatusHeartbeat(t *testing.T) {
	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	server := &Server{}
	handler := server.wrapLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	heartbeat := httptest.NewRequest(http.MethodPost, "/api/sessions/status", nil)
	heartbeat.RemoteAddr = "127.0.0.1:12345"
	handler.ServeHTTP(httptest.NewRecorder(), heartbeat)
	if got := buf.String(); got != "" {
		t.Fatalf("heartbeat must not be access-logged, got %q", got)
	}

	create := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
	create.RemoteAddr = "127.0.0.1:12345"
	handler.ServeHTTP(httptest.NewRecorder(), create)

	got := buf.String()
	if !strings.Contains(got, "200 POST /api/sessions") {
		t.Errorf("normal request must still be access-logged, got %q", got)
	}
	if strings.Contains(got, "/status") {
		t.Errorf("heartbeat leaked into the access log: %q", got)
	}
}
