package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"sentinel/server/internal/ctlapi"
)

func newAuditCmd(c *cli) *cobra.Command {
	var limit int
	auditCmd := &cobra.Command{
		Use:   "audit",
		Short: "Nhật ký audit: ai đã làm gì, lúc nào, từ đâu",
		Long: `In các dòng audit mới nhất (mới nhất ở trên). Nhật ký chỉ ghi thêm, mỗi dòng nối hash với dòng trước;
"sentinelctl audit verify" kiểm tra chuỗi đó.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			records, err := api.Audit(cmd.Context(), limit)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), records)
			}
			printAudit(cmd.OutOrStdout(), records)
			return nil
		},
	}
	auditCmd.Flags().IntVarP(&limit, "limit", "n", 50, "số dòng tối đa (1 đến 1000)")
	auditCmd.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Kiểm tra chuỗi hash của nhật ký audit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			verified, err := api.VerifyAudit(cmd.Context())
			if err != nil {
				return explain(err)
			}
			if verified.Error != "" {
				return fmt.Errorf("chuỗi audit hỏng sau %d dòng đúng: %s", verified.Checked, verified.Error)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Chuỗi audit nguyên vẹn: %d dòng.\n", verified.Checked)
			return nil
		},
	})
	return auditCmd
}

func printAudit(out io.Writer, records []ctlapi.AuditRecord) {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "THỜI GIAN\tAI\tTỪ\tHÀNH ĐỘNG\tĐỐI TƯỢNG\tCHI TIẾT")
	for _, record := range records {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", record.At.Local().Format("2006-01-02 15:04:05"),
			record.Actor, dash(record.IP), record.Action, dash(record.Target), dash(record.Detail))
	}
	_ = table.Flush()
}

func dash(text string) string {
	if text == "" {
		return "-"
	}
	return text
}
