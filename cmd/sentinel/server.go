package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"sentinel/internal/alert"
	"sentinel/internal/config"
	"sentinel/internal/notify"
	"sentinel/internal/server"
	"sentinel/internal/server/service"
)

func (a *app) serverCmd() *cobra.Command {
	var serverConfig string
	cmd := &cobra.Command{Use: "server", Short: "Central server with the web UI"}
	cmd.PersistentFlags().StringVar(&serverConfig, "server-config", "/etc/sentinel/server.yaml", "server config file")

	openService := func() (*config.ServerConfig, *service.Service, error) {
		cfg, err := config.LoadServer(serverConfig)
		if err != nil {
			return nil, nil, err
		}
		svc, err := service.Open(cfg.DB)
		if err != nil {
			return nil, nil, fmt.Errorf("open %s: %w", cfg.DB, err)
		}
		return cfg, svc, nil
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Serve the web UI and the agent API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, svc, err := openService()
			if err != nil {
				return err
			}
			defer svc.Close()

			srv := server.New(cfg, svc)
			srv.Version = version
			wd := &server.Watchdog{Server: srv, Alerts: &alert.Manager{
				Sender:   &notify.Multi{Notifiers: notify.FromConfig(cfg.Notifiers)},
				Server:   "sentinel-server",
				Cooldown: cfg.Alert.Cooldown,
			}}
			go wd.Run(ctx, 30*time.Second)

			hs := &http.Server{
				Addr:              cfg.Listen,
				Handler:           srv,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       60 * time.Second,
				WriteTimeout:      60 * time.Second, // agent long-polls last 25s
				IdleTimeout:       120 * time.Second,
			}
			errc := make(chan error, 1)
			go func() {
				slog.Info("sentinel server listening", "addr", cfg.Listen, "tls", cfg.TLS != nil)
				if cfg.TLS != nil {
					errc <- hs.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
				} else {
					errc <- hs.ListenAndServe()
				}
			}()
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
			}
			sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return hs.Shutdown(sctx)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "add-agent <name>",
		Short: "Register a server and print its agent token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, svc, err := openService()
			if err != nil {
				return err
			}
			defer svc.Close()
			ag, token, err := svc.CreateAgent(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "created agent %q (id %d). Put this in its sentinel.env as SENTINEL_TOKEN:\n", ag.Name, ag.ID)
			fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "hash-password",
		Short: "Read a password from stdin and print its bcrypt hash for server.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprint(cmd.ErrOrStderr(), "password: ")
			line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if err != nil && line == "" {
				return err
			}
			pw := strings.TrimRight(line, "\r\n")
			if len(pw) < 10 {
				return errors.New("use at least 10 characters")
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(hash))
			return nil
		},
	})
	return cmd
}
