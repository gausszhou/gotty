package api

import (
	"bufio"
	"context"
	"log"
	"net"
	"net/http"
)

// RunOptions holds a set of configurations for Server.Run().
type RunOptions struct {
	gracefullCtx context.Context
}

// RunOption is an option of Server.Run().
type RunOption func(*RunOptions)

// WithGracefullContext accepts a context to shutdown a Server
// with care for existing client connections.
func WithGracefullContext(ctx context.Context) RunOption {
	return func(options *RunOptions) {
		options.gracefullCtx = ctx
	}
}

func (server *Server) wrapLogger(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &logResponseWriter{w, 200}
		handler.ServeHTTP(rw, r)
		if accessLogSkipped(r) {
			return
		}
		log.Printf("%s %d %s %s", r.RemoteAddr, rw.status, r.Method, r.URL.Path)
	})
}

// accessLogSkipped 标识"不值得写访问日志"的请求。
//
// 目前只有客户端的状态心跳 POST /api/sessions/status:它每 2s 一次、状态码
// 恒为 200、响应体只是各会话的存活位。实测一个页面开着就能写约 100KB/小时,
// 把真正有用的行(建/销毁会话、WS 连接、错误)全淹掉 —— 排查问题时最需要的
// 那几行反而被埋在心跳里。心跳的可观测性由浏览器侧的状态点承担,服务端不再
// 重复记一份。
func accessLogSkipped(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/sessions/status"
}

func (server *Server) wrapHeaders(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// todo add version
		w.Header().Set("Server", "GoTTY")
		handler.ServeHTTP(w, r)
	})
}

type logResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *logResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *logResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, _ := w.ResponseWriter.(http.Hijacker)
	w.status = http.StatusSwitchingProtocols
	return hj.Hijack()
}
