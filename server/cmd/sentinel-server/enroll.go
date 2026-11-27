package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"sentinel/server/internal/enroll"
)

func newEnrollCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "enroll <tên-server>",
		Short: "Thêm một server và in lệnh để nó join",
		Long: `Tạo join token cho một server (dùng một lần, hết hạn sau 24 giờ) và in lệnh "sentinel agent join".
Chạy lại cho cùng một tên (khi server chưa join) thì cấp token mới và huỷ token cũ.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := c.open(cmd.Context())
			if err != nil {
				return err
			}
			defer a.Close()
			added, err := a.AddAgent(cmd.Context(), args[0])
			if err != nil {
				return explainEnroll(args[0], err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Join token của %s (dùng một lần, hết hạn %s):\n\n", added.Name, added.Expires.Local().Format("2006-01-02 15:04"))
			fmt.Fprintf(out, "Trên server đó, chạy:\n\n  sentinel agent join --url %s --token %s --ca-fingerprint %s\n\n",
				added.JoinURL, added.Token, added.Fingerprint)
			fmt.Fprintln(out, "Token chỉ hiện một lần và không lưu lại: mất thì chạy lại lệnh này.")
			return nil
		},
	}
}

func explainEnroll(name string, err error) error {
	if errors.Is(err, enroll.ErrExists) {
		return fmt.Errorf("server %q đã có (đã join hoặc đã bị thu hồi): mỗi tên chỉ dùng một lần, chọn tên khác", name)
	}
	return err
}
