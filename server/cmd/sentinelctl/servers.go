package main

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/server/internal/ctlapi"
)

// Words for the states of an agent.
var stateText = map[string]string{"pending": "chờ join", "active": "hoạt động", "revoked": "đã thu hồi"}

func newServerCmd(c *cli) *cobra.Command {
	serverCmd := &cobra.Command{Use: "server", Short: "Các server đã thêm vào control plane"}
	serverCmd.AddCommand(newServerListCmd(c), newServerAddCmd(c), newServerRevokeCmd(c))
	return serverCmd
}

func newServerListCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Liệt kê các server",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			agents, err := api.Agents(cmd.Context())
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), agents)
			}
			printAgents(cmd.OutOrStdout(), agents)
			return nil
		},
	}
}

func printAgents(out io.Writer, agents []ctlapi.Agent) {
	if len(agents) == 0 {
		fmt.Fprintln(out, "Chưa có server nào: sentinelctl server add <tên>")
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "TÊN\tTRẠNG THÁI\tJOIN LÚC\tCHỨNG CHỈ HẾT HẠN")
	for _, agent := range agents {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", agent.Name, stateText[agent.State], dayOrDash(agent.EnrolledAt), dayOrDash(agent.CertNotAfter))
	}
	_ = table.Flush()
}

func dayOrDash(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02")
}

func newServerAddCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "add <tên-server>",
		Short: "Thêm một server và in lệnh để nó join",
		Long: `Tạo join token cho một server (dùng một lần, hết hạn sau 24 giờ) và in lệnh "sentinel agent join".
Tên dùng chữ, số, '.', '_' và '-' (tối đa 63 ký tự). Chạy lại cho một tên chưa join thì cấp token mới và huỷ token cũ.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			added, err := api.AddAgent(cmd.Context(), args[0])
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSON(out, added)
			}
			fmt.Fprintf(out, "Join token của %s (dùng một lần, hết hạn %s):\n\n", added.Name, added.ExpiresAt.Local().Format("2006-01-02 15:04"))
			fmt.Fprintf(out, "Trên server đó, chạy:\n\n  sentinel agent join --url %s --token %s --ca-fingerprint %s\n\n", added.JoinURL, added.Token, added.CAFingerprint)
			fmt.Fprintln(out, "Token chỉ hiện một lần và không lưu lại: mất thì chạy lại lệnh này.")
			return nil
		},
	}
}

func newServerRevokeCmd(c *cli) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "revoke <tên-server>",
		Short: "Thu hồi một server: chứng chỉ của nó bị từ chối ngay",
		Long: `Thu hồi chứng chỉ của server và huỷ join token chưa dùng. Có hiệu lực ngay ở request kế tiếp của nó, không hoàn tác được:
server muốn quay lại phải join lại với một tên khác. Hỏi gõ lại tên để xác nhận, trừ khi có --yes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			if !yes {
				typed, err := c.term.Line(fmt.Sprintf("Gõ lại tên %q để thu hồi: ", args[0]))
				if err != nil {
					return err
				}
				if typed != args[0] {
					return errors.New("tên không khớp: không thu hồi gì cả")
				}
			}
			if err := api.Revoke(cmd.Context(), args[0]); err != nil {
				return explain(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Đã thu hồi %s.\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "không hỏi xác nhận")
	return cmd
}
