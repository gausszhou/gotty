package config

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

// sliceOpts is a minimal option struct exercising the []string flag path
// (`serve --env K=V`, repeatable).
type sliceOpts struct {
	Env  []string `json:"env" flagName:"env" flagDescribe:"extra environment variables"`
	Port string   `json:"port" flagName:"port" flagDescribe:"port" default:"9049"`
}

func newSliceCmd(t *testing.T, opts *sliceOpts) (*cobra.Command, map[string]string) {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	mappings, err := AttachFlags(cmd, opts)
	if err != nil {
		t.Fatalf("AttachFlags: %v", err)
	}
	return cmd, mappings
}

// TestAttachFlagsStringSlice 验证 []string 字段被注册成可重复 flag,
// 且重复出现时按序累积而不是互相覆盖。
func TestAttachFlagsStringSlice(t *testing.T) {
	opts := &sliceOpts{}
	cmd, mappings := newSliceCmd(t, opts)

	if _, ok := mappings["env"]; !ok {
		t.Fatal("env flag not registered in mappings")
	}
	if cmd.Flags().Lookup("env") == nil {
		t.Fatal("--env flag not registered")
	}

	if err := cmd.Flags().Parse([]string{"--env", "FOO=bar", "--env", "BAZ=qux"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ApplyFlags(cmd, mappings, opts)

	want := []string{"FOO=bar", "BAZ=qux"}
	if !reflect.DeepEqual(opts.Env, want) {
		t.Fatalf("Env = %v, want %v", opts.Env, want)
	}
}

// TestApplyEnvStringSlice 验证 GOTTY_ENV 的逗号分隔列表(带空格)能填进
// []string 字段。
func TestApplyEnvStringSlice(t *testing.T) {
	t.Setenv("GOTTY_ENV", "FOO=bar, BAZ=qux")

	opts := &sliceOpts{}
	cmd, mappings := newSliceCmd(t, opts)

	if err := ApplyEnv(cmd, mappings, opts); err != nil {
		t.Fatalf("ApplyEnv: %v", err)
	}

	want := []string{"FOO=bar", "BAZ=qux"}
	if !reflect.DeepEqual(opts.Env, want) {
		t.Fatalf("Env = %v, want %v", opts.Env, want)
	}
}

// TestApplyEnvDoesNotOverrideExplicitFlag 验证显式给的 flag 优先于同名环境变量
// (与既有标量字段的行为一致)。
func TestApplyEnvDoesNotOverrideExplicitFlag(t *testing.T) {
	t.Setenv("GOTTY_ENV", "FROM=env")

	opts := &sliceOpts{}
	cmd, mappings := newSliceCmd(t, opts)
	if err := cmd.Flags().Parse([]string{"--env", "FROM=flag"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if err := ApplyEnv(cmd, mappings, opts); err != nil {
		t.Fatalf("ApplyEnv: %v", err)
	}
	ApplyFlags(cmd, mappings, opts)

	want := []string{"FROM=flag"}
	if !reflect.DeepEqual(opts.Env, want) {
		t.Fatalf("Env = %v, want %v (flag must win over GOTTY_ENV)", opts.Env, want)
	}
}
