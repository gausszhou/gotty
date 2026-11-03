package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gottytuitest "github.com/gausszhou/gotty/skills/gotty-tui-test"
)

func executeSkillCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := buildSkillCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestSkillPrintsEmbeddedSkill(t *testing.T) {
	out, err := executeSkillCmd(t)
	if err != nil {
		t.Fatalf("skill failed: %v", err)
	}
	if out != gottytuitest.Content {
		t.Fatalf("stdout does not match the embedded skill (%d vs %d bytes)", len(out), len(gottytuitest.Content))
	}
	if !strings.HasPrefix(out, "---\nname: gotty-tui-test\n") {
		t.Errorf("skill does not start with the expected front matter:\n%.80s", out)
	}
}

func TestSkillInstallWritesFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gotty-tui-test")
	out, err := executeSkillCmd(t, "--install", dir)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}
	want := filepath.Join(dir, "SKILL.md")
	if strings.TrimSpace(out) != want {
		t.Errorf("printed path = %q, want %q", strings.TrimSpace(out), want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if string(data) != gottytuitest.Content {
		t.Error("installed content differs from the embedded skill")
	}
}

func TestSkillInstallRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := executeSkillCmd(t, "--install", dir); err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	if _, err := executeSkillCmd(t, "--install", dir); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second install error = %v, want a --force hint", err)
	}
}

func TestSkillInstallForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := executeSkillCmd(t, "--install", dir, "--force"); err != nil {
		t.Fatalf("force install failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != gottytuitest.Content {
		t.Error("--force did not replace the file")
	}
}

func TestSkillRegisteredAtRoot(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"skill"})
	if err != nil || found == nil || found.Name() != "skill" {
		t.Fatalf("rootCmd.Find(skill) = %v, %v; want the skill command", found, err)
	}
}
