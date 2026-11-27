package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"sentinel/server/internal/audit"
)

func newAuditCmd(c *cli) *cobra.Command {
	auditCmd := &cobra.Command{Use: "audit", Short: "Nhật ký audit của control plane"}
	auditCmd.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Kiểm tra chuỗi hash của nhật ký audit",
		Long: `Đọc toàn bộ nhật ký audit và kiểm tra mỗi dòng nối đúng với dòng trước. Báo dòng đầu tiên bị sửa,
bị xoá hoặc bị chèn thêm. Thoát với mã khác 0 nếu chuỗi hỏng.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := c.open(cmd.Context())
			if err != nil {
				return err
			}
			defer a.Close()
			checked, err := audit.Verify(cmd.Context(), a.Store.DB)
			if err != nil {
				return fmt.Errorf("chuỗi audit hỏng sau %d dòng đúng: %w", checked, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Chuỗi audit nguyên vẹn: %d dòng.\n", checked)
			return nil
		},
	})
	return auditCmd
}
