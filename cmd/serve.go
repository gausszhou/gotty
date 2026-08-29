package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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

// setupLogFile writes the server log to path (append mode) in addition
// to the console. Empty path keeps the console-only behavior.
func setupLogFile(path string) error {
	if path == "" {
		return nil
	}

	logPath := utils.Expand(path)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("failed to create log directory `%s`: %w", filepath.Dir(logPath), err)
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open log file `%s`: %w", logPath, err)
	}

	log.SetOutput(io.MultiWriter(os.Stderr, file))
	log.Printf("Server log file: %s", logPath)
	return nil
}

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
// POST /api/sessions). Without a command, the user's login shell
// ($SHELL, falling back to /bin/sh) is used, so that opening the page
// always yields a usable session.
func buildServeCmd() *cobra.Command {
	serveCmd := &cobra.Command{
		Use:   "serve [flags] [command [<arguments...>]]",
		Short: "Start the terminal sharing server",
		Long: "Start the terminal sharing server.\n\n" +
			"Provide a command (e.g. `gotty serve top`) to run in shared\n" +
			"sessions; without one, the login shell ($SHELL) is used.",
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
	if cmd.Flags().Changed("config") || !os.IsNotExist(statErr) {
		// 显式 --config 时严格加载(不存在/目录均报错);
		// 默认路径(~/.gotty/config.json)仅在其存在时加载。
		if err := config.ApplyConfigFile(configFile, appOptions, terminalOptions); err != nil {
			return fmt.Errorf("failed to load config file: %w", err)
		}
	}

	config.ApplyFlags(cmd, mappings, appOptions, terminalOptions)

	// 服务端日志落盘(默认 ~/.gotty/logs/gotty.log,文件 + 控制台双写)
	// 必须在任何日志输出之前初始化
	if err := setupLogFile(appOptions.LogFile); err != nil {
		return err
	}

	hostname, _ := os.Hostname()
	var defaultCommand string
	defaultArgs := []string{}
	if len(args) > 0 {
		defaultCommand = args[0]
		defaultArgs = args[1:]
	} else if shell := os.Getenv("SHELL"); shell != "" {
		// 无命令时回退到登录 shell,保证页面打开即有可用会话
		log.Printf("No command given, using the login shell: %s", shell)
		defaultCommand = shell
	} else {
		log.Printf("No command given and $SHELL is unset, using: /bin/sh")
		defaultCommand = "/bin/sh"
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

	var store session.Store = session.NewMemoryStore()
	if appOptions.SessionFile != "" {
		storePath := utils.Expand(appOptions.SessionFile)
		fileStore, err := session.NewFileStore(storePath)
		if err != nil {
			return fmt.Errorf("failed to load session history: %w", err)
		}
		log.Printf("Session history file: %s", storePath)
		store = fileStore
	}

	manager := session.NewManager(
		session.WithMaxSession(appOptions.MaxSession),
		session.WithIdleTimeout(time.Duration(appOptions.Timeout)*time.Second),
		session.WithTerminalOptions(*terminalOptions),
		session.WithStore(store),
		session.WithMirrorFactory(api.MirrorFactory(appOptions.Mirror)),
	)

	srv, err := api.New(manager, appOptions)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	gCtx, gCancel := context.WithCancel(context.Background())

	log.Printf("GoTTY is starting with command: %s", strings.Join(append([]string{defaultCommand}, defaultArgs...), " "))

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
