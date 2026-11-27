package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newUnblockCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "unblock",
		Short: "Gỡ chặn đăng nhập: xoá mọi lần sai mật khẩu đã đếm",
		Long: `Dành cho người có quyền root trên máy control plane. Cổng của sentinelctl mặc định chỉ nghe trên 127.0.0.1, nên qua SSH tunnel
mọi client có cùng địa chỉ: ai trên máy này gửi sai mật khẩu 5 lần là chặn cả chủ mật khẩu 15 phút. Lệnh này gỡ chặn ngay mà
không đổi mật khẩu. Mật khẩu và các phiên đăng nhập giữ nguyên.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := c.open(cmd.Context())
			if err != nil {
				return err
			}
			defer a.Close()
			if err := a.Auth.Unblock(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Đã gỡ chặn đăng nhập.")
			return nil
		},
	}
}
