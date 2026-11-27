package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"

	"sentinel/pkg/notify"
	"sentinel/pkg/pki"
	"sentinel/server/internal/app"
	"sentinel/server/internal/httpapi"
)

// pruneEvery is how often sessions that ended and old failure counts are deleted, and the certificate is looked at.
const pruneEvery = 10 * time.Minute

func newServeCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Chạy control plane: nghe agent và sentinelctl",
		Long: `Chạy control plane cho đến khi nhận SIGINT hoặc SIGTERM. Nghe hai cổng, đều TLS 1.3:
  listen        cho agent (mutual TLS) — mở cổng này cho các server của bạn và không ai khác
  ctl_listen  cho sentinelctl (đăng nhập bằng mật khẩu), mặc định chỉ trên máy này: vào qua SSH tunnel
Nhật ký (kể cả bản sao của audit) ghi ra stderr, systemd đưa vào journal.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
			a, err := c.open(cmd.Context())
			if err != nil {
				return err
			}
			defer a.Close()
			return serve(cmd.Context(), a)
		},
	}
}

func serve(ctx context.Context, a *app.App) error {
	source, err := a.NewServerCertSource(ctx, time.Now())
	if err != nil {
		return err
	}
	cert, _ := source.Get(nil) // only to build the configs: they present source's certificate
	agentLn, err := listenTLS(a.Config.Listen, source.Use(pki.ServerTLSConfig(*cert, a.CA.Pool())))
	if err != nil {
		return fmt.Errorf("listen (agents): %w", err)
	}
	ctlLn, err := listenTLS(a.Config.CtlListen, source.Use(pki.CtlTLSConfig(*cert)))
	if err != nil {
		agentLn.Close()
		return fmt.Errorf("ctl_listen: %w", err)
	}
	if !isLoopback(a.Config.CtlListen) {
		slog.Warn("ctl_listen is not on this machine only: anybody who can reach it may try the password", "ctl_listen", a.Config.CtlListen)
	}
	slog.Info("serving", "agents", a.Config.Listen, "ctl", a.Config.CtlListen, "ca", a.CA.Fingerprint())

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	server := httpapi.New(a)
	runners := []func(context.Context) error{
		func(ctx context.Context) error { return httpapi.Serve(ctx, agentLn, server.AgentHandler()) },
		func(ctx context.Context) error { return httpapi.Serve(ctx, ctlLn, server.CtlHandler()) },
		func(ctx context.Context) error { return maintain(ctx, a, source) },
		a.Watchdog.Run,
		a.Tasks.Run,
		a.Scheduler.Run,
	}
	if a.Config.Heartbeat.URL != "" {
		runners = append(runners, func(ctx context.Context) error { return heartbeat(ctx, a.Config.Heartbeat) })
	}
	a.Watchdog.Started = time.Now() // agents need a little while to find a control plane that has just started
	ended := make(chan error, len(runners))
	for _, run := range runners {
		go func() { ended <- run(ctx) }()
	}
	err = <-ended // the first to stop is the reason; the signal gives nil
	stop()
	for range len(runners) - 1 {
		if stopped := <-ended; stopped != nil {
			slog.Warn("stopped", "err", stopped)
		}
	}
	return err
}

// heartbeat requests the heartbeat URL now and then every settings.Every, until ctx is done. A failed
// request is logged: the service on the other end raises the alarm.
func heartbeat(ctx context.Context, settings notify.HeartbeatSettings) error {
	ticker := time.NewTicker(settings.Every)
	defer ticker.Stop()
	for {
		if err := notify.Heartbeat(ctx, settings.URL); err != nil && ctx.Err() == nil {
			slog.Warn("heartbeat failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func listenTLS(address string, config *tls.Config) (net.Listener, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, config), nil
}

// isLoopback says the address is on this machine only (an empty host means every interface).
func isLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// maintain deletes what has ended and renews the server's certificate when it is near its end, until ctx is done.
func maintain(ctx context.Context, a *app.App, source *app.ServerCertSource) error {
	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if err := a.Auth.Prune(ctx); err != nil {
				slog.Error("prune", "err", err)
			}
			if err := source.Refresh(ctx, now); err != nil {
				slog.Error("renew the server certificate", "err", err)
			}
		}
	}
}
