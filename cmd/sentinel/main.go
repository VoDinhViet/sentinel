// Command sentinel is a small server maintenance tool: backups via restic,
// uptime checks and alerting.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type app struct {
	configFlag string // --config as given
	configPath string // the file actually used, resolved before each command runs
	configErr  error  // why no config file could be chosen
	logLevel   string
	term       terminalIO
}

func main() {
	limitCPU()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cli := &app{term: defaultTerminal()}
	root := &cobra.Command{
		Use:           "sentinel",
		Short:         "Bảo trì máy chủ: backup, uptime check, cảnh báo",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cli.runDefault(cmd) },
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			var level slog.Level
			if err := level.UnmarshalText([]byte(cli.logLevel)); err != nil {
				return fmt.Errorf("--log-level: %w", err)
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
			cli.configPath, cli.configErr = config.DefaultLocations(cli.configFlag).Resolve()
			return nil
		},
	}
	root.PersistentFlags().StringVarP(&cli.configFlag, "config", "c", "", "file config (mặc định: $SENTINEL_CONFIG, /etc/sentinel/config.yaml hoặc ~/.config/sentinel/config.yaml, file nào có trước)")
	root.AddCommand(cli.notifyCmd(), cli.backupCmd(), cli.doctorCmd(), cli.runCmd(), cli.checkCmd(), cli.statusCmd(), cli.manageCmd(), cli.initCmd())
	root.PersistentFlags().StringVar(&cli.logLevel, "log-level", "info", "debug, info, warn hoặc error")
	localizeHelp(root)
	return root
}

// runDefault is `sentinel` with no command: on a terminal it opens the form that creates the
// config when there is none, and the manager when there is one; otherwise it prints the help.
func (a *app) runDefault(cmd *cobra.Command) error {
	if !a.term.isTerminal() || !stdoutIsTerminal() {
		return cmd.Help()
	}
	if _, err := os.Stat(a.configPath); a.configErr != nil || err != nil {
		return a.runInitForm(cmd, false)
	}
	return a.runManage(cmd)
}

func (a *app) openStore(cfg *config.Config) (*store.Store, error) {
	st, err := store.Open(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", cfg.StorePath, err)
	}
	return st, nil
}

// limitCPU keeps sentinel itself to two cores: it only supervises restic and the dump tools,
// which are separate processes, so more would take CPU from the server for nothing.
// GOMAXPROCS in the environment still wins.
func limitCPU() {
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(min(runtime.NumCPU(), 2))
	}
}
