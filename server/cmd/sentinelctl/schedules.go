package main

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/format"
)

func newScheduleCmd(c *cli) *cobra.Command {
	scheduleCmd := &cobra.Command{
		Use:   "schedule",
		Short: "Lịch giao việc tự động cho server (cron)",
		Long: `Control plane tự xếp một việc cho server theo lịch cron, qua đúng bảng action và hàng đợi như sentinelctl run.
Server vẫn kiểm tra allowlist của nó và có thể từ chối. Lịch có 5 trường: phút giờ ngày-trong-tháng tháng thứ,
hoặc @hourly, @daily, @weekly, @monthly, @yearly; thêm "CRON_TZ=Asia/Ho_Chi_Minh " ở đầu để chọn múi giờ (mặc định là
múi giờ của máy control plane). Control plane tắt lúc đến giờ thì lần chạy lỡ quá 10 phút bị bỏ qua, không chạy muộn.`,
	}
	scheduleCmd.AddCommand(newScheduleAddCmd(c), newScheduleListCmd(c), newScheduleRemoveCmd(c))
	return scheduleCmd
}

func newScheduleAddCmd(c *cli) *cobra.Command {
	var yes bool
	addCmd := &cobra.Command{
		Use:   "add <server> <lịch> <action> [tên=giá-trị …]",
		Short: "Thêm một lịch",
		Long: `Thêm một lịch cho server. Action và tham số như trong sentinelctl run (sentinelctl run --help liệt kê chúng).
Action đổi máy khó hoàn tác (os.apply_updates) hỏi gõ lại tên server để xác nhận, trừ khi có --yes.`,
		Example: `  sentinelctl schedule add web01 "0 3 * * *" backup.start job=db
  sentinelctl schedule add web01 "CRON_TZ=Asia/Ho_Chi_Minh 30 2 * * 0" os.check_updates
  sentinelctl schedule add web01 @hourly doctor.run`,
		Args: cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			params, err := parseParams(args[3:])
			if err != nil {
				return err
			}
			if agentapi.NeedsConfirmation(args[2]) && !yes {
				if err := c.confirmAction(args[0], args[2]); err != nil {
					return err
				}
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			made, err := api.AddSchedule(cmd.Context(), args[0], args[1], args[2], params)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), made)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Đã thêm lịch #%d: %s trên %s theo %q%s.\n", made.ID, made.Action, made.Agent, made.Spec, nextText(made))
			return nil
		},
	}
	addCmd.Flags().BoolVarP(&yes, "yes", "y", false, "không hỏi xác nhận (cho action đổi máy khó hoàn tác)")
	return addCmd
}

func nextText(one ctlapi.Schedule) string {
	if one.Next == nil {
		return " (lịch này không có lần chạy nào sắp tới)"
	}
	return ", lần tới " + one.Next.Local().Format("2006-01-02 15:04")
}

func newScheduleListCmd(c *cli) *cobra.Command {
	var serverName string
	listCmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Liệt kê các lịch",
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
			schedules, err := api.Schedules(cmd.Context(), serverName)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), schedules)
			}
			printSchedules(cmd.OutOrStdout(), schedules, time.Now())
			return nil
		},
	}
	listCmd.Flags().StringVar(&serverName, "server", "", "chỉ các lịch của server này")
	return listCmd
}

func printSchedules(out io.Writer, schedules []ctlapi.Schedule, now time.Time) {
	if len(schedules) == 0 {
		fmt.Fprintln(out, "Chưa có lịch nào: sentinelctl schedule add <server> <lịch> <action>")
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "SỐ\tSERVER\tLỊCH\tACTION\tTHAM SỐ\tLẦN TỚI\tLẦN CHẠY CUỐI")
	for _, one := range schedules {
		next := "-"
		if one.Next != nil {
			next = one.Next.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(table, "#%d\t%s\t%s\t%s\t%s\t%s\t%s\n", one.ID, one.Agent, one.Spec, one.Action, paramsText(one.Params), next, lastRunText(one, now))
	}
	_ = table.Flush()
}

func lastRunText(one ctlapi.Schedule, now time.Time) string {
	switch {
	case one.LastRunAt == nil:
		return "chưa chạy"
	case one.LastError != "":
		return format.Since(*one.LastRunAt, now) + ": không xếp được (" + one.LastError + ")"
	case one.LastTask != nil:
		return format.Since(*one.LastRunAt, now) + ": việc #" + strconv.FormatInt(*one.LastTask, 10)
	}
	return format.Since(*one.LastRunAt, now)
}

func newScheduleRemoveCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <số>",
		Aliases: []string{"remove"},
		Short:   "Xoá một lịch (việc nó đã xếp vẫn còn)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id < 1 {
				return fmt.Errorf("số của lịch phải là số nguyên dương, không phải %q", args[0])
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			if err := api.RemoveSchedule(cmd.Context(), id); err != nil {
				return explain(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Đã xoá lịch #%d.\n", id)
			return nil
		},
	}
}
