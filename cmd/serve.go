package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/gausszhou/gotty/internal/api"
	"github.com/gausszhou/gotty/internal/config"
	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
	"github.com/gausszhou/gotty/internal/utils"
)

var (
	appOptions      = &api.Options{}
	terminalOptions = &terminal.Options{}
	mappings        map[string]string
)

// buildServeCmd creates the `gotty serve` subcommand.
//
// Usage: gotty serve [flags] [command [<arguments...>]]
//
// When a command is given on the CLI, it becomes the default command for
// sessions created without an explicit one (empty "command" in
// POST /api/sessions). Without a command the server starts in a pure
// gateway mode and every session must specify its command via the REST API.
func buildServeCmd() *cobra.Command {
	serveCmd := &cobra.Command{
		Use:   "serve [flags] [command [<arguments...>]]",
		Short: "Start the terminal sharing server",
		Long: "Start the terminal sharing server.\n\n" +
			"Optionally provide a command to run in shared sessions;\n" +
			"without one, session commands must be given via the REST API.",
		Args: cobra.ArbitraryArgs,
		RunE: runServe,
	}

	// Static configuration: flag defaults come from the struct tags.
	if err := config.ApplyDefaultValues(appOptions); err != nil {
		panic(err)
	}
	if err := config.ApplyDefaultValues(terminalOptions); err != nil {
		panic(err)
	}

	// Build CLI flags from option structs. The --config flag is a
	// persistent flag on the root command (see root.go).
	var err error
	mappings, err = config.AttachFlags(serveCmd, appOptions, terminalOptions)
	if err != nil {
		panic(err)
	}

	return serveCmd
}

func runServe(cmd *cobra.Command, args []string) error {
	// Re-apply defaults for fresh option values
	if err := config.ApplyDefaultValues(appOptions); err != nil {
		return err
	}
	if err := config.ApplyDefaultValues(terminalOptions); err != nil {
		return err
	}

	// Apply configuration in precedence order:
	// env vars < config file < CLI flags
	if err := config.ApplyEnv(cmd, mappings, appOptions, terminalOptions); err != nil {
		return err
	}

	configFile, _ := cmd.Flags().GetString("config")
	_, statErr := os.Stat(utils.Expand(configFile))
	if configFile != "~/.gotty" || !os.IsNotExist(statErr) {
		if err := config.ApplyConfigFile(configFile, appOptions, terminalOptions); err != nil {
			return fmt.Errorf("failed to load config file: %w", err)
		}
	}

	config.ApplyFlags(cmd, mappings, appOptions, terminalOptions)

	hostname, _ := os.Hostname()
	var defaultCommand string
	defaultArgs := []string{}
	if len(args) > 0 {
		defaultCommand = args[0]
		defaultArgs = args[1:]
	}
	appOptions.TitleVariables = map[string]interface{}{
		"command":  defaultCommand,
		"argv":     defaultArgs,
		"hostname": hostname,
	}
	// Sessions created without an explicit command (REST API)
	// use the command given on the CLI, when provided.
	appOptions.DefaultCommand = defaultCommand
	appOptions.DefaultArgs = defaultArgs

	manager := session.NewManager(
		session.WithMaxSession(appOptions.MaxSession),
		session.WithIdleTimeout(time.Duration(appOptions.Timeout)*time.Second),
		session.WithTerminalOptions(*terminalOptions),
	)

	srv, err := api.New(manager, appOptions)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	gCtx, gCancel := context.WithCancel(context.Background())

	if len(args) > 0 {
		log.Printf("GoTTY is starting with command: %s", strings.Join(args, " "))
	} else {
		log.Printf("GoTTY is starting without a default command (use the REST API to create sessions)")
	}

	errs := make(chan error, 1)
	go func() {
		errs <- srv.Run(ctx, api.WithGracefullContext(gCtx))
	}()
	err = waitSignals(errs, cancel, gCancel)

	if err != nil && err != context.Canceled {
		return err
	}

	return nil
}

func waitSignals(errs chan error, cancel context.CancelFunc, gracefullCancel context.CancelFunc) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(
		sigChan,
		syscall.SIGINT,
		syscall.SIGTERM,
	)

	select {
	case err := <-errs:
		return err

	case s := <-sigChan:
		switch s {
		case syscall.SIGINT:
			gracefullCancel()
			fmt.Println("C-C to force close")
			select {
			case err := <-errs:
				return err
			case <-sigChan:
				fmt.Println("Force closing...")
				cancel()
				return <-errs
			}
		default:
			cancel()
			return <-errs
		}
	}
}
