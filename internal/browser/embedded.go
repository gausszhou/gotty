package browser

import (
	"fmt"
	"net"
	"net/http"

	"github.com/gausszhou/gotty/internal/api"
	"github.com/gausszhou/gotty/internal/session"
)

// NewEmbeddedServer boots an in-process gotty server bound to
// 127.0.0.1:0 and returns its base URL plus a shutdown function. It is
// used by the browser engine and its e2e tests, which need a real gotty
// page without running the full Run loop — and without the manager's
// idle sweep, because the caller manages session lifetime explicitly (a
// browser-engine session may be attached long after the command exited;
// the sweep would remove it and the page would 404).
func NewEmbeddedServer(opts *api.Options) (base string, shutdown func(), err error) {
	if opts == nil {
		opts = &api.Options{
			Address:     "127.0.0.1",
			Port:        "0",
			PermitWrite: false,
			TitleFormat: "GoTTY",
		}
	}
	mgr := session.NewManager()
	srv, err := api.New(mgr, opts)
	if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	httpSrv := &http.Server{Handler: srv.SetupHandlers()}
	go func() { _ = httpSrv.Serve(listener) }()
	return fmt.Sprintf("http://127.0.0.1:%d", port), func() {
		_ = httpSrv.Close()
		_ = listener.Close()
	}, nil
}
