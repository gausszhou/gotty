package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedRunRequest is what the fake server saw.
type capturedRunRequest struct {
	method string
	path   string
	body   map[string]interface{}
}

// newCreateSessionServer returns a fake POST /api/sessions endpoint.
func newCreateSessionServer(t *testing.T, status int, response string, got *capturedRunRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &got.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
}

func executeRunCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := buildAgentRunCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestRunPostsFullSessionRequest(t *testing.T) {
	var got capturedRunRequest
	srv := newCreateSessionServer(t, http.StatusCreated,
		`{"id":"abcdefghij012345","state":"running"}`, &got)
	defer srv.Close()

	out, err := executeRunCmd(t, "--server", srv.URL,
		"--width", "120", "--height", "40",
		"--env", "FOO=bar", "--env", "BAZ=qux",
		"--cwd", "/tmp", "--term", "xterm-256color", "--scrollback", "50",
		"--", "sh", "-c", "echo hi")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if got.method != http.MethodPost || got.path != "/api/sessions" {
		t.Errorf("request = %s %s, want POST /api/sessions", got.method, got.path)
	}
	if out != "abcdefghij012345\n" {
		t.Errorf("stdout = %q, want the bare session id", out)
	}

	want := map[string]interface{}{
		"command":    "sh",
		"width":      float64(120),
		"height":     float64(40),
		"cwd":        "/tmp",
		"term":       "xterm-256color",
		"scrollback": float64(50),
	}
	for k, v := range want {
		if got.body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, got.body[k], v)
		}
	}
	args, _ := got.body["args"].([]interface{})
	if len(args) != 2 || args[0] != "-c" || args[1] != "echo hi" {
		t.Errorf("body[args] = %v, want [-c echo hi]", got.body["args"])
	}
	env, _ := got.body["env"].([]interface{})
	if len(env) != 2 || env[0] != "FOO=bar" || env[1] != "BAZ=qux" {
		t.Errorf("body[env] = %v, want [FOO=bar BAZ=qux]", got.body["env"])
	}
	if _, ok := got.body["id"]; ok {
		t.Errorf("body has an id although --id was not passed: %v", got.body["id"])
	}
}

func TestRunOmitsDefaults(t *testing.T) {
	var got capturedRunRequest
	srv := newCreateSessionServer(t, http.StatusOK,
		`{"id":"abcdefghij012345","state":"running"}`, &got)
	defer srv.Close()

	if _, err := executeRunCmd(t, "--server", srv.URL); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	for _, k := range []string{"command", "width", "height", "env", "cwd", "term", "scrollback"} {
		if _, ok := got.body[k]; ok {
			t.Errorf("body[%q] should be omitted, got %v", k, got.body[k])
		}
	}
}

func TestRunJSONPrintsFullRecord(t *testing.T) {
	var got capturedRunRequest
	srv := newCreateSessionServer(t, http.StatusCreated,
		`{"id":"abcdefghij012345", "state":"running", "pid":4242}`, &got)
	defer srv.Close()

	out, err := executeRunCmd(t, "--server", srv.URL, "--json", "--", "sh")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &record); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", out, err)
	}
	if record["id"] != "abcdefghij012345" || record["pid"] != float64(4242) {
		t.Errorf("record = %v, want the full session record", record)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("--json must be a single line, got %q", out)
	}
}

func TestRunPassesID(t *testing.T) {
	var got capturedRunRequest
	srv := newCreateSessionServer(t, http.StatusOK,
		`{"id":"abcdefghij012345"}`, &got)
	defer srv.Close()

	if _, err := executeRunCmd(t, "--server", srv.URL, "--id", "abcdefghij012345"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got.body["id"] != "abcdefghij012345" {
		t.Errorf("body[id] = %v, want the client-chosen id", got.body["id"])
	}
}

func TestRunKeepsFlagsAfterCommand(t *testing.T) {
	var got capturedRunRequest
	srv := newCreateSessionServer(t, http.StatusCreated,
		`{"id":"abcdefghij012345"}`, &got)
	defer srv.Close()

	if _, err := executeRunCmd(t, "--server", srv.URL, "--", "./tui", "--help", "-x"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	args, _ := got.body["args"].([]interface{})
	if len(args) != 2 || args[0] != "--help" || args[1] != "-x" {
		t.Errorf("body[args] = %v, want [--help -x] (the command's own flags)", got.body["args"])
	}
}

func TestRunServerErrorIsReadable(t *testing.T) {
	var got capturedRunRequest
	srv := newCreateSessionServer(t, http.StatusServiceUnavailable,
		`{"error":"too many sessions"}`, &got)
	defer srv.Close()

	_, err := executeRunCmd(t, "--server", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "too many sessions") {
		t.Fatalf("error = %v, want the server's message", err)
	}
}

func TestRunValidatesFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"width without height", []string{"--width", "80"}, "--width and --height"},
		{"negative width", []string{"--width=-1", "--height", "10"}, ">= 0"},
		{"negative scrollback", []string{"--scrollback", "-2"}, ">= 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executeRunCmd(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestRunRegisteredAtRootAndUnderAgent(t *testing.T) {
	// Registered both ways so `gotty run` and `gotty agent run` work.
	for _, path := range [][]string{{"run"}, {"agent", "run"}} {
		found, _, err := rootCmd.Find(path)
		if err != nil || found == nil || found.Name() != "run" {
			t.Errorf("rootCmd.Find(%v) = %v, %v; want the run command", path, found, err)
		}
	}
}
