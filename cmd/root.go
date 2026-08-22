package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// Version and CommitID are injected at build time via -ldflags.
var (
	Version  = "unknown_version"
	CommitID = "unknown_commit"
)

var rootCmd = &cobra.Command{
	Use:           "gotty",
	Short:         "Share your terminal as a web application",
	Version:       Version + "+" + CommitID,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the CLI.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// --config is a persistent flag so it works in both
	// `gotty --config x serve` and `gotty serve --config x`.
	configPath := os.Getenv("GOTTY_CONFIG")
	if configPath == "" {
		// 默认配置文件位于配置目录下(与 logs/ 等共存)
		configPath = "~/.gotty/config.json"
	}
	rootCmd.PersistentFlags().String("config", configPath, "Config file path (GOTTY_CONFIG env var)")

	rootCmd.AddCommand(buildServeCmd())
}
