package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKillDeletesSession(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cmd := buildAgentKillCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--server", srv.URL, "--session", "abcdefghij012345"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("kill failed: %v", err)
	}

	if gotMethod != http.MethodDelete || gotPath != "/api/sessions/abcdefghij012345" {
		t.Errorf("request = %s %s, want DELETE /api/sessions/abcdefghij012345", gotMethod, gotPath)
	}
	var res map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", out.String(), err)
	}
	if res["session_id"] != "abcdefghij012345" || res["destroyed"] != true {
		t.Errorf("result = %v, want the destroyed session", res)
	}
}

func TestKillServerErrorIsReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"session not found"}`)
	}))
	defer srv.Close()

	cmd := buildAgentKillCmd()
	cmd.SetOut(new(strings.Builder))
	cmd.SetArgs([]string{"--server", srv.URL, "--session", "abcdefghij012345"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("error = %v, want the server's message", err)
	}
}

func TestKillRequiresSession(t *testing.T) {
	t.Setenv("GOTTY_SESSION", "")
	cmd := buildAgentKillCmd()
	cmd.SetOut(new(strings.Builder))
	cmd.SetArgs([]string{"--server", "http://127.0.0.1:1"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no session id") {
		t.Fatalf("error = %v, want a missing-session message", err)
	}
}

func TestKillRegisteredAtRootAndUnderAgent(t *testing.T) {
	for _, path := range [][]string{{"kill"}, {"agent", "kill"}} {
		found, _, err := rootCmd.Find(path)
		if err != nil || found == nil || found.Name() != "kill" {
			t.Errorf("rootCmd.Find(%v) = %v, %v; want the kill command", path, found, err)
		}
	}
}
