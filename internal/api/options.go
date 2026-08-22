package api

// Options configures the HTTP/WebSocket server.
type Options struct {
	Address     string `json:"address" flagName:"address" flagSName:"a" flagDescribe:"IP address to listen" default:"0.0.0.0"`
	Port        string `json:"port" flagName:"port" flagSName:"p" flagDescribe:"Port number to listen" default:"8080"`
	PermitWrite bool   `json:"permit_write" flagName:"permit-write" flagSName:"w" flagDescribe:"Permit clients to write to the TTY (BE CAREFUL)" default:"true"`

	TitleFormat     string `json:"title_format" flagName:"title-format" flagDescribe:"Title format of browser window" default:"GoTTY - {{ .command }}@{{ .hostname }}"`
	EnableReconnect bool   `json:"enable_reconnect" flagName:"reconnect" flagDescribe:"Enable reconnection" default:"false"`
	ReconnectTime   int    `json:"reconnect_time" flagName:"reconnect-time" flagDescribe:"Time to reconnect" default:"10"`

	MaxSession int `json:"max_session" flagName:"max-session" flagDescribe:"Maximum number of concurrent sessions (0 to disable)" default:"0"`
	// Timeout 是会话淘汰策略:超过该秒数没有任何客户端附着(浏览器全关/
	// 断连)即销毁 PTY 进程;会话记录保留,可凭 id 重新运行。默认 900s。
	Timeout int `json:"timeout" flagName:"timeout" flagDescribe:"Idle timeout seconds for destroying unattached sessions (0 to disable)" default:"900"`

	// SessionFile persists the session history (restart-safe). Empty disables it.
	SessionFile string `json:"session_file" flagName:"session-file" flagDescribe:"File path to persist session history (empty disables, default: ~/.gotty.sessions.json)" default:"~/.gotty.sessions.json"`

	// LogFile writes the server log to a file (in addition to the console).
	// Empty means console only.
	LogFile string `json:"log_file" flagName:"log-file" flagDescribe:"Server log file path (empty = console only, default: ~/.gotty/logs/gotty.log)" default:"~/.gotty/logs/gotty.log"`

	Width    int    `json:"width" flagName:"width" flagDescribe:"Static width of the screen, 0(default) means dynamically resize" default:"0"`
	Height   int    `json:"height" flagName:"height" flagDescribe:"Static height of the screen, 0(default) means dynamically resize" default:"0"`
	WSOrigin string `json:"ws_origin" flagName:"ws-origin" flagDescribe:"A regular expression that matches origin URLs to be accepted by WebSocket" default:""`
	Term     string `json:"term" flagName:"term" flagDescribe:"Terminal name to use on the browser (xterm)" default:"xterm-256color"`

	EnableTLS  bool   `json:"enable_tls" flagName:"tls" flagSName:"t" flagDescribe:"Enable TLS/SSL" default:"false"`
	TLSCrtFile string `json:"tls_crt_file" flagName:"tls-crt" flagDescribe:"TLS/SSL certificate file path" default:"~/.gotty.crt"`
	TLSKeyFile string `json:"tls_key_file" flagName:"tls-key" flagDescribe:"TLS/SSL key file path" default:"~/.gotty.key"`

	// TitleVariables fills out the window title template (set by main).
	TitleVariables map[string]interface{} `json:"-"`
	// Preferences is sent to the client as a SetPreferences frame.
	Preferences map[string]interface{} `json:"preferences"`
	// DefaultCommand is used when a session is created without an explicit command.
	DefaultCommand string   `json:"-"`
	DefaultArgs    []string `json:"-"`
}
