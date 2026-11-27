// Command sentinel-server is the control plane of Sentinel: the agents of many servers connect to it,
// and you use sentinelctl to see them and give them work. This command sets it up and
// looks after it from the machine it runs on.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"sentinel/server/internal/app"
	"sentinel/server/internal/config"
	"sentinel/server/internal/terminal"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type cli struct {
	configPath string
	term       terminal.Terminal
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRootCmd(&cli{term: terminal.Std()})
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Lỗi:", err)
		os.Exit(1)
	}
}

func newRootCmd(c *cli) *cobra.Command {
	root := &cobra.Command{
		Use:           "sentinel-server",
		Short:         "Control plane của Sentinel: nơi các agent kết nối về",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			// The audit entries of these commands are on the terminal's own screen already; the
			// long-running server logs them all (see the audit package).
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		},
	}
	root.PersistentFlags().StringVarP(&c.configPath, "config", "c", config.DefaultPath, "đường dẫn file server.yaml")
	root.AddCommand(newInitCmd(c), newEnrollCmd(c), newPasswdCmd(c), newAuditCmd(c), newServeCmd(c), newUnblockCmd(c))
	return root
}

// open loads the config and opens the control plane it describes.
func (c *cli) open(ctx context.Context) (*app.App, error) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return nil, fmt.Errorf("đọc %s: %w (chạy sentinel-server init trước?)", c.configPath, err)
	}
	return app.Open(ctx, cfg)
}
