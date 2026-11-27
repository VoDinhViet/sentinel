package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"sentinel/server/internal/client"
)

func newLoginCmd(c *cli) *cobra.Command {
	var url, fingerprint string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Đăng nhập control plane bằng mật khẩu đăng nhập",
		Long: `Hỏi mật khẩu đăng nhập (không hiện ký tự; script dùng --password-stdin), đăng nhập và lưu phiên (hết hạn sau 12 giờ,
hoặc sau 1 giờ không dùng) trong thư mục cấu hình, mode 0600. Địa chỉ và fingerprint của CA được nhớ lại ở lần sau.
Sai 5 lần liên tiếp thì địa chỉ của bạn bị chặn 15 phút.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := c.directory()
			if err != nil {
				return err
			}
			saved, _ := client.LoadConfig(dir) // a first login has none
			cfg := client.Config{URL: saved.URL, CAFingerprint: saved.CAFingerprint}
			if url != "" {
				cfg.URL = url
			}
			if fingerprint != "" {
				cfg.CAFingerprint = fingerprint
			}
			if cfg.URL == "" || cfg.CAFingerprint == "" {
				return errors.New("lần đầu cần --url và --ca-fingerprint (fingerprint do sentinel-server init in ra)")
			}
			api, err := client.New(cfg, "")
			if err != nil {
				return err
			}
			password, err := c.term.Password("Mật khẩu đăng nhập: ", false, passwordStdin)
			if err != nil {
				return err
			}
			session, err := api.Login(cmd.Context(), password)
			if err != nil {
				return err
			}
			if err := client.SaveConfig(dir, cfg); err != nil {
				return err
			}
			if err := client.SaveSession(dir, session.Token); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Đã đăng nhập %s. Phiên hết hạn lúc %s.\n", cfg.URL, session.ExpiresAt.Local().Format("2006-01-02 15:04"))
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "địa chỉ cổng sentinelctl, ví dụ https://127.0.0.1:9321")
	cmd.Flags().StringVar(&fingerprint, "ca-fingerprint", "", "fingerprint của CA (sha256:…) do sentinel-server init in ra")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "đọc mật khẩu từ stdin thay vì hỏi")
	return cmd
}

func newLogoutCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Kết thúc phiên đăng nhập",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, dir, err := c.connect()
			switch {
			case errors.Is(err, client.ErrNoSession):
				fmt.Fprintln(cmd.OutOrStdout(), "Chưa đăng nhập.")
				return nil
			case err != nil:
				return err
			}
			// A session that has already ended is what logging out wants; the file goes either way.
			if err := api.Logout(cmd.Context()); err != nil && !errors.Is(err, client.ErrSessionEnded) {
				return err
			}
			if err := client.DeleteSession(dir); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Đã đăng xuất.")
			return nil
		},
	}
}

func newPasswdCmd(c *cli) *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "passwd",
		Short: "Đổi mật khẩu đăng nhập",
		Long: `Hỏi mật khẩu hiện tại, rồi mật khẩu mới hai lần (ít nhất 12 ký tự). Mọi phiên đăng nhập đều kết thúc, kể cả phiên này.
Với --password-stdin đọc hai dòng: mật khẩu hiện tại, rồi mật khẩu mới.
Quên mật khẩu: chạy sentinel-server passwd bằng root trên máy control plane.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, dir, err := c.connect()
			if err != nil {
				return explain(err)
			}
			oldPassword, err := c.term.Password("Mật khẩu hiện tại: ", false, passwordStdin)
			if err != nil {
				return err
			}
			newPassword, err := c.term.Password("Mật khẩu mới: ", true, passwordStdin)
			if err != nil {
				return err
			}
			if err := api.Passwd(cmd.Context(), oldPassword, newPassword); err != nil {
				return explain(err)
			}
			if err := client.DeleteSession(dir); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Đã đổi mật khẩu. Mọi phiên đã kết thúc: chạy sentinelctl login.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "đọc mật khẩu hiện tại và mật khẩu mới từ stdin (hai dòng)")
	return cmd
}
