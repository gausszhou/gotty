package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func findCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func TestServeSubcommandRegistered(t *testing.T) {
	serve := findCommand(rootCmd, "serve")
	if serve == nil {
		t.Fatal("serve subcommand not registered")
	}

	// --config is a persistent flag on the root, inherited by serve.
	if serve.Flags().Lookup("config") == nil && serve.InheritedFlags().Lookup("config") == nil {
		t.Fatal("--config flag missing on serve")
	}
	if rootCmd.PersistentFlags().Lookup("config") == nil {
		t.Fatal("--config must be a persistent flag on the root command")
	}

	for _, name := range []string{
		"port", "address", "permit-write", "credential",
		"title-format", "reconnect", "reconnect-time",
		"max-session", "timeout", "width", "height",
		"ws-origin", "term", "tls", "tls-crt", "tls-key",
		"close-signal", "close-timeout",
	} {
		if serve.Flags().Lookup(name) == nil {
			t.Fatalf("flag --%s missing on serve", name)
		}
	}
}

func TestRootVersionFlag(t *testing.T) {
	if rootCmd.Version == "" {
		t.Fatal("root command must expose --version")
	}
}
