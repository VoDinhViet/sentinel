package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/format"
)

func newServerShowCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "show <tên-server>",
		Short: "Xem một server: máy, backup, uptime check và cảnh báo đang mở",
		Long: `In những gì server báo lần gần nhất: tình trạng máy (CPU, RAM, ổ đĩa, mạng, tiến trình), kết quả backup của
từng job, uptime check và các cảnh báo đang mở trên chính server đó. Server báo mỗi phút, nên số liệu cũ nhiều nhất chừng đó.`,
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
			detail, err := api.Agent(cmd.Context(), args[0])
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), detail)
			}
			printDetail(cmd.OutOrStdout(), detail, time.Now())
			return nil
		},
	}
}

func printDetail(out io.Writer, detail ctlapi.AgentDetail, now time.Time) {
	fmt.Fprintf(out, "%s  %s\n", detail.Name, format.Status(detail.Status, detail.State))
	fmt.Fprintf(out, "  Thấy lần cuối:     %s\n", format.Ago(detail.LastSeen, now))
	fmt.Fprintf(out, "  Phiên bản:         %s\n", format.Dash(detail.Version))
	fmt.Fprintf(out, "  Chứng chỉ hết hạn: %s\n", format.Day(detail.CertNotAfter))
	if detail.Report == nil {
		fmt.Fprintln(out, "\nChưa nhận báo cáo nào từ server này.")
		return
	}
	fmt.Fprintf(out, "  Báo cáo lúc:       %s\n", format.Ago(detail.ReportAt, now))
	printHost(out, detail.Report.Host)
	printBackups(out, detail.Report.Backups)
	printChecks(out, detail.Report.Checks)
	printAlerts(out, "Cảnh báo đang mở trên server", detail.Report.Alerts)
}

func printHost(out io.Writer, host agentapi.HostStatus) {
	fmt.Fprintf(out, "\nMáy: %s, %s, kernel %s\n", format.Dash(host.Hostname), format.Dash(host.OS), format.Dash(host.Kernel))
	fmt.Fprintf(out, "  Bật được:  %s   Load: %.2f %.2f %.2f   CPU: %.0f%% của %d nhân\n",
		format.Uptime(host.UptimeSeconds), host.Load[0], host.Load[1], host.Load[2], host.CPUPercent, host.CPUs)
	fmt.Fprintf(out, "  RAM:       %s trống / %s   Swap: %s trống / %s\n",
		format.Size(host.Memory.AvailableBytes), format.Size(host.Memory.TotalBytes), format.Size(host.Memory.SwapFreeBytes), format.Size(host.Memory.SwapTotalBytes))
	for _, disk := range host.Disks {
		fmt.Fprintf(out, "  Ổ đĩa %-12s %s trống / %s\n", disk.Mount, format.Size(disk.FreeBytes), format.Size(disk.TotalBytes))
	}
	for _, unit := range host.FailedServices {
		fmt.Fprintf(out, "  Unit lỗi:  %s (%s)\n", unit.Name, unit.Active)
	}
	for _, problem := range host.Errors {
		fmt.Fprintf(out, "  Không đọc được: %s\n", problem)
	}
}

func printBackups(out io.Writer, backups []agentapi.BackupStatus) {
	if len(backups) == 0 {
		return
	}
	fmt.Fprintln(out, "\nBackup lần gần nhất:")
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "  JOB\tREPOSITORY\tKẾT THÚC\tKẾT QUẢ")
	for _, backup := range backups {
		fmt.Fprintf(table, "  %s\t%s\t%s\t%s\n", backup.Job, backup.Repository, backup.FinishedAt.Local().Format("2006-01-02 15:04"), backup.Result)
	}
	_ = table.Flush()
}

func printChecks(out io.Writer, checks []agentapi.CheckStatus) {
	if len(checks) == 0 {
		return
	}
	fmt.Fprintln(out, "\nUptime check:")
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "  TÊN\tKẾT QUẢ\tĐỘ TRỄ\tLÚC")
	for _, check := range checks {
		result := "UP"
		if !check.OK {
			result = "DOWN: " + check.Error
		}
		fmt.Fprintf(table, "  %s\t%s\t%dms\t%s\n", check.Name, result, check.LatencyMs, check.At.Local().Format("15:04:05"))
	}
	_ = table.Flush()
}

func printAlerts(out io.Writer, title string, alerts []agentapi.AlertStatus) {
	if len(alerts) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s:\n", title)
	for _, open := range alerts {
		fmt.Fprintf(out, "  %s  %s  (từ %s)\n", open.Key, open.Message, open.Since.Local().Format("2006-01-02 15:04"))
	}
}

func newAlertCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "alert",
		Short: "Các cảnh báo đang mở của control plane: server mất liên lạc, chứng chỉ sắp hết hạn",
		Long: `In các cảnh báo control plane đang mở. Đây là cảnh báo chỉ control plane tự biết được (server không báo nữa,
chứng chỉ không gia hạn được); cảnh báo trên từng server (backup lỗi, đầy ổ đĩa) nằm ở "sentinelctl server show <tên>".`,
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
			alerts, err := api.Alerts(cmd.Context())
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), alerts)
			}
			if len(alerts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Không có cảnh báo nào đang mở.")
				return nil
			}
			shown := make([]agentapi.AlertStatus, 0, len(alerts))
			for _, open := range alerts {
				shown = append(shown, agentapi.AlertStatus{Key: open.Key, Message: open.Message, Since: open.Since})
			}
			printAlerts(cmd.OutOrStdout(), "Cảnh báo đang mở", shown)
			return nil
		},
	}
}
