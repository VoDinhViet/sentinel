package main

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/format"
)

func newReportCmd(c *cli) *cobra.Command {
	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Báo cáo từ lịch sử: tình trạng máy và các lần backup",
		Long: `Báo cáo dựng từ lịch sử mà các server gửi lên (một mẫu của máy ít nhất 5 phút một lần, giữ 30 ngày; mỗi lần backup,
giữ 90 ngày). Thêm -o json để đọc bằng máy.`,
	}
	reportCmd.AddCommand(newReportHostCmd(c), newReportBackupsCmd(c))
	return reportCmd
}

func newReportHostCmd(c *cli) *cobra.Command {
	var hours int
	hostCmd := &cobra.Command{
		Use:     "host <server>",
		Short:   "CPU, RAM và ổ đĩa của một server theo thời gian",
		Example: "  sentinelctl report host web01 --hours 72",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			history, err := api.HostReport(cmd.Context(), args[0], hours)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), history)
			}
			printHostReport(cmd.OutOrStdout(), history)
			return nil
		},
	}
	hostCmd.Flags().IntVar(&hours, "hours", 24, "số giờ gần nhất (1 đến 720)")
	return hostCmd
}

// hostSeries is one value of the points, in a row.
func hostSeries(points []ctlapi.HostPoint, value func(ctlapi.HostPoint) float64) []float64 {
	series := make([]float64, 0, len(points))
	for _, point := range points {
		series = append(series, value(point))
	}
	return series
}

func printHostReport(out io.Writer, history ctlapi.HostReport) {
	if history.Summary.Samples == 0 {
		fmt.Fprintf(out, "%s chưa có mẫu nào trong %d giờ qua (server báo mỗi phút, mẫu được giữ 5 phút một lần).\n", history.Agent, history.Hours)
		return
	}
	summary := history.Summary
	fmt.Fprintf(out, "%s, %d giờ qua, %d mẫu, mỗi cột %s\n", history.Agent, history.Hours, summary.Samples, time.Duration(history.StepSeconds)*time.Second)
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "\tTRUNG BÌNH\tCAO NHẤT\tBIỂU ĐỒ")
	fmt.Fprintf(table, "CPU\t%.0f%%\t%.0f%%\t%s\n", summary.CPUAvg, summary.CPUMax, format.Sparkline(hostSeries(history.Points, func(p ctlapi.HostPoint) float64 { return p.CPUPercent }), 100))
	fmt.Fprintf(table, "RAM\t%.0f%%\t%.0f%%\t%s\n", summary.MemAvg, summary.MemMax, format.Sparkline(hostSeries(history.Points, func(p ctlapi.HostPoint) float64 { return p.MemPercent }), 100))
	fmt.Fprintf(table, "Ổ đĩa đầy nhất\t\t%.0f%%\t%s\n", summary.DiskMax, format.Sparkline(hostSeries(history.Points, func(p ctlapi.HostPoint) float64 { return p.DiskPercent }), 100))
	fmt.Fprintf(table, "Load 1 phút\t%.2f\t\t\n", summary.LoadAvg)
	_ = table.Flush()
}

func newReportBackupsCmd(c *cli) *cobra.Command {
	var serverName string
	var days int
	backupsCmd := &cobra.Command{
		Use:     "backups",
		Short:   "Các lần backup của một server hoặc của tất cả",
		Example: "  sentinelctl report backups --days 30\n  sentinelctl report backups --server web01 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			if days < 1 || days > 30 {
				return errors.New("--days là số ngày từ 1 đến 30")
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			history, err := api.BackupReport(cmd.Context(), serverName, days*24)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), history)
			}
			printBackupReport(cmd.OutOrStdout(), history, days, time.Now())
			return nil
		},
	}
	backupsCmd.Flags().StringVar(&serverName, "server", "", "chỉ server này")
	backupsCmd.Flags().IntVar(&days, "days", 7, "số ngày gần nhất (1 đến 30)")
	return backupsCmd
}

func printBackupReport(out io.Writer, history ctlapi.BackupReport, days int, now time.Time) {
	if len(history.Jobs) == 0 {
		fmt.Fprintf(out, "Không có lần backup nào trong %d ngày qua.\n", days)
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "SERVER\tJOB\tREPOSITORY\tSỐ LẦN\tLỖI\tTHÊM VÀO\tLẦN CUỐI\tTHÀNH CÔNG CUỐI\tKẾT QUẢ CUỐI")
	for _, job := range history.Jobs {
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n", job.Agent, job.Job, job.Repository, job.Runs, job.Failures, format.Size(uint64(max(job.BytesAdded, 0))),
			format.Since(job.LastFinishedAt, now), format.Ago(job.LastSuccessAt, now), job.LastResult)
	}
	_ = table.Flush()
	if len(history.Failed) == 0 {
		return
	}
	fmt.Fprintln(out, "\nCác lần lỗi, mới nhất trước:")
	for _, run := range history.Failed {
		fmt.Fprintf(out, "  %s  %s %s/%s: %s\n", run.FinishedAt.Local().Format("2006-01-02 15:04"), run.Agent, run.Job, run.Repository, run.Result)
	}
}
