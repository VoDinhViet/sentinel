package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPasswdCmd(c *cli) *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "passwd",
		Short: "Đặt lại mật khẩu đăng nhập và kết thúc mọi phiên đăng nhập",
		Long: `Dành cho người có quyền root trên máy control plane, khi quên mật khẩu: không hỏi mật khẩu cũ,
vì ai đọc được database thì đã có toàn quyền. Mọi phiên đăng nhập của sentinelctl bị kết thúc.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := c.open(cmd.Context())
			if err != nil {
				return err
			}
			defer a.Close()
			password, err := c.term.Password("Mật khẩu đăng nhập mới: ", true, passwordStdin)
			if err != nil {
				return err
			}
			if err := a.Auth.SetPassword(cmd.Context(), password); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Đã đổi mật khẩu đăng nhập. Mọi phiên đăng nhập đã kết thúc.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "đọc mật khẩu từ stdin thay vì hỏi")
	return cmd
}
