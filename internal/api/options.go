package api

// Options configures the HTTP/WebSocket server.
type Options struct {
	Address     string `json:"address" flagName:"address" flagSName:"a" flagDescribe:"IP address to listen" default:"0.0.0.0"`
	Port        string `json:"port" flagName:"port" flagSName:"p" flagDescribe:"Port number to listen" default:"8080"`
	PermitWrite bool   `json:"permit_write" flagName:"permit-write" flagSName:"w" flagDescribe:"Permit clients to write to the TTY (BE CAREFUL)" default:"false"`
	Credential  string `json:"credential" flagName:"credential" flagSName:"c" flagDescribe:"Credential for Basic Authentication (ex: user:pass, default disabled)" default:""`

	TitleFormat     string `json:"title_format" flagName:"title-format" flagDescribe:"Title format of browser window" default:"GoTTY - {{ .command }}@{{ .hostname }}"`
	EnableReconnect bool   `json:"enable_reconnect" flagName:"reconnect" flagDescribe:"Enable reconnection" default:"false"`
	ReconnectTime   int    `json:"reconnect_time" flagName:"reconnect-time" flagDescribe:"Time to reconnect" default:"10"`

	MaxSession int `json:"max_session" flagName:"max-session" flagDescribe:"Maximum number of concurrent sessions (0 to disable)" default:"0"`
	Timeout    int `json:"timeout" flagName:"timeout" flagDescribe:"Idle timeout seconds for destroying unattached sessions (0 to disable)" default:"0"`

	Width    int    `json:"width" flagName:"width" flagDescribe:"Static width of the screen, 0(default) means dynamically resize" default:"0"`
	Height   int    `json:"height" flagName:"height" flagDescribe:"Static height of the screen, 0(default) means dynamically resize" default:"0"`
	WSOrigin string `json:"ws_origin" flagName:"ws-origin" flagDescribe:"A regular expression that matches origin URLs to be accepted by WebSocket" default:""`
	Term     string `json:"term" flagName:"term" flagDescribe:"Terminal name to use on the browser (xterm)" default:"xterm"`

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
