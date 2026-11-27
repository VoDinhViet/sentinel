// Command sentinelctl is how you use the control plane of Sentinel from a terminal: sign
// in, see the servers, add or revoke one, read the audit log. It only calls the control plane's
// API of the control plane; nothing is read from its database.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"sentinel/server/internal/client"
	"sentinel/server/internal/terminal"
	"sentinel/server/internal/tui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type cli struct {
	dir    string
	output string
	term   terminal.Terminal
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd(&cli{term: terminal.Std()}).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Lỗi:", err)
		os.Exit(1)
	}
}

func newRootCmd(c *cli) *cobra.Command {
	root := &cobra.Command{
		Use:           "sentinelctl",
		Short:         "Quản trị các server qua control plane của Sentinel",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `sentinelctl nói chuyện với sentinel-server qua TLS 1.3, chỉ tin CA có fingerprint bạn đã đưa lúc đăng nhập.
Chỉ có một mật khẩu đăng nhập: đăng nhập là có toàn quyền; quyền thật sự của mỗi server nằm ở allowlist local của agent trên đó.

Lần đầu:  sentinelctl login --url https://127.0.0.1:9321 --ca-fingerprint sha256:…
(sentinel-server init đã in fingerprint; cổng sentinelctl mặc định chỉ mở trên máy control plane: dùng ssh -L 9321:127.0.0.1:9321.)

Không có lệnh con thì mở màn hình terminal: các server, cảnh báo và chi tiết từng server (chỉ đọc).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return c.screen(cmd) },
	}
	root.PersistentFlags().StringVar(&c.dir, "dir", "", "thư mục lưu ctl.yaml và phiên đăng nhập (mặc định ~/.config/sentinel)")
	root.PersistentFlags().StringVarP(&c.output, "output", "o", "text", "định dạng đầu ra: text hoặc json")
	root.AddCommand(newLoginCmd(c), newLogoutCmd(c), newPasswdCmd(c), newServerCmd(c), newAlertCmd(c), newRunCmd(c), newTaskCmd(c), newScheduleCmd(c), newMaintenanceCmd(c), newAuditCmd(c))
	return root
}

// directory is where sentinelctl keeps its files.
func (c *cli) directory() (string, error) {
	if c.dir != "" {
		return c.dir, nil
	}
	return client.DefaultDir()
}

// connect makes the client for the saved control plane and session.
func (c *cli) connect() (*client.Client, string, error) {
	dir, err := c.directory()
	if err != nil {
		return nil, "", err
	}
	cfg, err := client.LoadConfig(dir)
	if err != nil {
		return nil, dir, err
	}
	token, err := client.LoadSession(dir)
	if err != nil {
		return nil, dir, err
	}
	api, err := client.New(cfg, token)
	return api, dir, err
}

func (c *cli) wantsJSON() (bool, error) {
	switch c.output {
	case "text":
		return false, nil
	case "json":
		return true, nil
	}
	return false, fmt.Errorf("-o %q: dùng text hoặc json", c.output)
}

func printJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// explain turns the error of a call into words for the person at the terminal.
func explain(err error) error {
	switch {
	case errors.Is(err, client.ErrSessionEnded):
		return errors.New("phiên đăng nhập đã hết hạn hoặc đã bị kết thúc: chạy sentinelctl login")
	case errors.Is(err, client.ErrNoSession):
		return errors.New("chưa đăng nhập: chạy sentinelctl login")
	}
	return err
}

// screen opens the terminal screen. It needs a terminal on both sides: a script that wants the servers
// uses "server ls -o json".
func (c *cli) screen(cmd *cobra.Command) error {
	if !c.term.IsTTY || !isTerminal(cmd.OutOrStdout()) {
		return cmd.Help()
	}
	api, _, err := c.connect()
	if err != nil {
		return explain(err)
	}
	return explain(tui.Run(cmd.Context(), api))
}

// isTerminal says out is a terminal.
func isTerminal(out io.Writer) bool {
	file, ok := out.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
