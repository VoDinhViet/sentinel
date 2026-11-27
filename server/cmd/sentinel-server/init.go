package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"sentinel/server/internal/app"
	"sentinel/server/internal/config"
)

func newInitCmd(c *cli) *cobra.Command {
	var hosts []string
	var listen, ctlListen, stateDir string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Tạo CA, chứng chỉ của server, database và mật khẩu đăng nhập",
		Long: `Chạy một lần trên máy control plane. Tạo CA riêng, chứng chỉ của server cho các host trong --host,
database và mật khẩu đăng nhập (hỏi hai lần), rồi in fingerprint của CA để đưa cho "sentinel agent join".
Từ chối chạy nếu thư mục state đã có CA, khoá hoặc database.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Default()
			cfg.Hosts = hosts
			if listen != "" {
				cfg.Listen = listen
			}
			if ctlListen != "" {
				cfg.CtlListen = ctlListen
			}
			if stateDir != "" {
				cfg.StateDir = stateDir
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			password, err := c.term.Password("Mật khẩu đăng nhập: ", true, passwordStdin)
			if err != nil {
				return err
			}
			result, err := app.Init(cmd.Context(), &cfg, password)
			if err != nil {
				return err
			}
			configNote := "Đã ghi " + c.configPath
			if err := config.Write(c.configPath, cfg); err != nil {
				configNote = fmt.Sprintf("Không ghi được %s (%v): tự tạo file đó từ các giá trị ở trên", c.configPath, err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Control plane đã sẵn sàng trong %s.\n%s.\n\n", cfg.StateDir, configNote)
			fmt.Fprintf(out, "Fingerprint của CA (đưa cho mỗi server khi join):\n  %s\n\n", result.Fingerprint)
			fmt.Fprintf(out, "Bước tiếp theo: sentinel-server enroll <tên-server>\n")
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "tên host hoặc IP mà agent dùng để kết nối (bắt buộc, lặp được)")
	cmd.Flags().StringVar(&listen, "listen", "", "địa chỉ cho agent kết nối (mặc định "+config.DefaultListen+")")
	cmd.Flags().StringVar(&ctlListen, "ctl-listen", "", "địa chỉ cho sentinelctl (mặc định "+config.DefaultCtlListen+")")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "thư mục lưu database và khoá (mặc định "+config.DefaultStateDir+")")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "đọc mật khẩu từ stdin thay vì hỏi")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}
