// Command sentinel is a small server maintenance tool: backups via restic,
// uptime checks and alerting.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
		Short:         "Server maintenance: backups, uptime checks, alerts",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
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
	root.PersistentFlags().StringVarP(&cli.configFlag, "config", "c", "", "config file (default: $SENTINEL_CONFIG, /etc/sentinel/config.yaml or ~/.config/sentinel/config.yaml, the first that exists)")
	root.AddCommand(cli.notifyCmd(), cli.backupCmd(), cli.doctorCmd(), cli.runCmd(), cli.checkCmd(), cli.statusCmd(), cli.manageCmd(), cli.initCmd())
	root.PersistentFlags().StringVar(&cli.logLevel, "log-level", "info", "debug, info, warn or error")
	return root
}

func (a *app) openStore(cfg *config.Config) (*store.Store, error) {
	st, err := store.Open(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", cfg.StorePath, err)
	}
	return st, nil
}
