package api

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"regexp"
	"sync"
	noesctmpl "text/template"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/coder/websocket"

	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/utils"
)

// Server provides the HTTP REST API, the WebSocket attach endpoint and
// the static terminal page.
type Server struct {
	manager *session.Manager
	options *Options

	indexTemplate *template.Template
	titleTemplate *noesctmpl.Template

	wsOriginMatcher *regexp.Regexp

	activeConns sync.Map // *websocket.Conn -> struct{}
	wsWG        sync.WaitGroup
}

// New creates a Server. It parses the embedded index page and the
// window title template; both are part of the configuration.
func New(manager *session.Manager, options *Options) (*Server, error) {
	indexData, err := fs.ReadFile(staticFiles, "static/index.html")
	if err != nil {
		return nil, fmt.Errorf("index template not found in embedded static files: %w", err)
	}
	indexTemplate, err := template.New("index").Parse(string(indexData))
	if err != nil {
		return nil, fmt.Errorf("failed to parse index template: %w", err)
	}

	titleTemplate, err := noesctmpl.New("title").Parse(options.TitleFormat)
	if err != nil {
		return nil, fmt.Errorf("failed to parse window title format `%s`: %w", options.TitleFormat, err)
	}

	var originMatcher *regexp.Regexp
	if options.WSOrigin != "" {
		matcher, err := regexp.Compile(options.WSOrigin)
		if err != nil {
			return nil, fmt.Errorf("failed to compile regular expression of Websocket Origin `%s`: %w", options.WSOrigin, err)
		}
		originMatcher = matcher
	}

	return &Server{
		manager:         manager,
		options:         options,
		indexTemplate:   indexTemplate,
		titleTemplate:   titleTemplate,
		wsOriginMatcher: originMatcher,
	}, nil
}

// Run starts the HTTP server. It blocks until ctx is canceled
// (immediate shutdown) or the graceful context is done
// (drain active connections first).
func (server *Server) Run(ctx context.Context, options ...RunOption) error {
	opts := &RunOptions{gracefullCtx: context.Background()}
	for _, opt := range options {
		opt(opts)
	}

	server.manager.Start(ctx)

	srv := &http.Server{Handler: server.setupHandlers()}

	if server.options.PermitWrite {
		log.Printf("Permitting clients to write input to the PTY.")
	}
	if server.options.Port == "0" {
		log.Printf("Port number configured to `0`, choosing a random port")
	}

	hostPort := net.JoinHostPort(server.options.Address, server.options.Port)
	listener, err := net.Listen("tcp", hostPort)
	if err != nil {
		return fmt.Errorf("failed to listen at `%s`: %w", hostPort, err)
	}

	scheme := "http"
	if server.options.EnableTLS {
		scheme = "https"
	}
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	log.Printf("HTTP server is listening at: %s", scheme+"://"+host+":"+port)
	if server.options.Address == "0.0.0.0" {
		for _, address := range listAddresses() {
			log.Printf("Alternative URL: %s", scheme+"://"+address+":"+port)
		}
	}

	srvErr := make(chan error, 1)
	go func() {
		if server.options.EnableTLS {
			crtFile := utils.Expand(server.options.TLSCrtFile)
			keyFile := utils.Expand(server.options.TLSKeyFile)
			log.Printf("TLS crt file: %s", crtFile)
			log.Printf("TLS key file: %s", keyFile)
			err = srv.ServeTLS(listener, crtFile, keyFile)
		} else {
			err = srv.Serve(listener)
		}
		if err != nil {
			srvErr <- err
		}
	}()

	// Graceful shutdown: drain request handlers, then force-close
	// hijacked WebSocket connections so that attach loops unwind.
	go func() {
		select {
		case <-opts.gracefullCtx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = srv.Shutdown(shutdownCtx)
			cancel()
			server.activeConns.Range(func(key, _ interface{}) bool {
				conn := key.(*websocket.Conn)
				_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
				return true
			})
			server.wsWG.Wait()
		case <-ctx.Done():
			_ = srv.Close()
		}
	}()

	select {
	case err = <-srvErr:
		if err == http.ErrServerClosed { // by graceful ctx
			err = nil
		} else {
			log.Printf("HTTP server error: %s", err)
		}
	case <-ctx.Done():
		_ = srv.Close()
		err = ctx.Err()
	}

	return err
}

// setupHandlers wires the route table:
//
//	GET  /                    terminal page (index template)
//	GET  /js/*,/css/*,favicon static assets
//	GET  /auth_token.js       auth token for the page
//	GET  /config.js           client config for the page
//	POST /api/sessions        create session
//	GET  /api/sessions        list sessions
//	GET  /api/sessions/{id}   session detail
//	DELETE /api/sessions/{id} destroy session
//	POST /api/sessions/{id}/resize
//	POST /api/sessions/{id}/signal
//	GET  /ws                  attach to ?session_id=xxx (WebSocket)
func (server *Server) setupHandlers() http.Handler {
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("failed to create static filesystem sub-root")
	}
	staticFileHandler := http.FileServer(http.FS(staticFS))

	apiMux := http.NewServeMux()

	// REST API — session management
	apiMux.HandleFunc("POST /api/sessions", server.handleCreateSession)
	apiMux.HandleFunc("GET /api/sessions", server.handleListSessions)
	apiMux.HandleFunc("GET /api/sessions/{id}", server.handleGetSession)
	apiMux.HandleFunc("DELETE /api/sessions/{id}", server.handleDeleteSession)
	apiMux.HandleFunc("POST /api/sessions/{id}/resize", server.handleResizeSession)
	apiMux.HandleFunc("POST /api/sessions/{id}/signal", server.handleSignalSession)

	// Site
	apiMux.HandleFunc("GET /", server.handleIndex)
	apiMux.Handle("GET /js/", http.StripPrefix("/", staticFileHandler))
	apiMux.Handle("GET /css/", http.StripPrefix("/", staticFileHandler))
	apiMux.Handle("GET /favicon.png", http.StripPrefix("/", staticFileHandler))
	apiMux.HandleFunc("GET /auth_token.js", server.handleAuthToken)
	apiMux.HandleFunc("GET /config.js", server.handleConfig)

	var siteHandler http.Handler = apiMux
	if server.options.Credential != "" {
		log.Printf("Using Basic Authentication")
		siteHandler = server.wrapBasicAuth(siteHandler, server.options.Credential)
	}
	siteHandler = gziphandler.GzipHandler(server.wrapHeaders(siteHandler))
	siteHandler = server.wrapLogger(siteHandler)

	mux := http.NewServeMux()
	mux.Handle("/", siteHandler)
	mux.Handle("GET /ws", server.wrapLogger(http.HandlerFunc(server.handleWS)))

	return mux
}

// handleIndex renders the terminal page with the window title.
func (server *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	titleVars := server.titleVariables(
		[]string{"server", "master"},
		map[string]map[string]interface{}{
			"server": server.options.TitleVariables,
			"master": {
				"remote_addr": r.RemoteAddr,
			},
		},
	)

	titleBuf := new(bytes.Buffer)
	if err := server.titleTemplate.Execute(titleBuf, titleVars); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	indexBuf := new(bytes.Buffer)
	if err := server.indexTemplate.Execute(indexBuf, map[string]interface{}{
		"title": titleBuf.String(),
	}); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Write(indexBuf.Bytes())
}

// attachWindowTitle renders the window title for an attached session.
func (server *Server) attachWindowTitle(sess *session.Session, remoteAddr string) []byte {
	titleVars := server.titleVariables(
		[]string{"server", "master", "session"},
		map[string]map[string]interface{}{
			"server": server.options.TitleVariables,
			"master": {
				"remote_addr": remoteAddr,
			},
			"session": {
				"id":      sess.ID(),
				"command": sess.Command(),
				"argv":    sess.Args(),
				"pid":     sess.PID(),
			},
		},
	)

	titleBuf := new(bytes.Buffer)
	if err := server.titleTemplate.Execute(titleBuf, titleVars); err != nil {
		log.Printf("Failed to fill window title template: %s", err)
		return nil
	}
	return titleBuf.Bytes()
}

func (server *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write([]byte("var gotty_auth_token = '" + server.options.Credential + "';"))
}

func (server *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write([]byte("var gotty_term = '" + server.options.Term + "';"))
}

// titleVariables merges maps in a specified order.
// varUnits are name-keyed maps, whose names will be iterated using order.
func (server *Server) titleVariables(order []string, varUnits map[string]map[string]interface{}) map[string]interface{} {
	titleVars := map[string]interface{}{}

	for _, name := range order {
		vars, ok := varUnits[name]
		if !ok {
			panic("title variable name error")
		}
		for key, val := range vars {
			titleVars[key] = val
		}
	}

	// safe net for conflicted keys
	for _, name := range order {
		titleVars[name] = varUnits[name]
	}

	return titleVars
}

// listAddresses enumerates the addresses of all network interfaces.
func listAddresses() (addresses []string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{}
	}

	addresses = make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		ifAddrs, _ := iface.Addrs()
		for _, ifAddr := range ifAddrs {
			switch v := ifAddr.(type) {
			case *net.IPNet:
				addresses = append(addresses, v.IP.String())
			case *net.IPAddr:
				addresses = append(addresses, v.IP.String())
			}
		}
	}
	return addresses
}
