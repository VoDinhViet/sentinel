package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/server/internal/ctlapi"
)

func newMaintenanceCmd(c *cli) *cobra.Command {
	maintenanceCmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Cửa sổ bảo trì: server vắng mặt có chủ ý",
		Long: `Trong cửa sổ bảo trì control plane không báo alert "server offline" của server đó (alert đã mở từ trước thì giữ đến khi
server báo lại) và sentinelctl hiện trạng thái MAINTENANCE. Alert chứng chỉ và alert mà chính server tự gửi không bị ảnh hưởng.`,
	}
	maintenanceCmd.AddCommand(newMaintenanceAddCmd(c), newMaintenanceListCmd(c), newMaintenanceRemoveCmd(c))
	return maintenanceCmd
}

// parseStart reads the start of a window: RFC 3339, or "2006-01-02 15:04" in the local zone.
func parseStart(text string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", text, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("--from phải có dạng %q (giờ máy này) hoặc RFC 3339, không phải %q", "2006-01-02 15:04", text)
	}
	return parsed, nil
}

func newMaintenanceAddCmd(c *cli) *cobra.Command {
	var all bool
	var length time.Duration
	var from, reason string
	addCmd := &cobra.Command{
		Use:   "add [server] --for <thời gian>",
		Short: "Mở một cửa sổ bảo trì cho một server, hoặc cho tất cả (--all)",
		Long:  "Mở cửa sổ từ bây giờ (hoặc từ --from) trong --for, tối đa 30 ngày. Dùng sentinelctl maintenance rm để kết thúc sớm.",
		Example: `  sentinelctl maintenance add web01 --for 2h --reason "nâng kernel"
  sentinelctl maintenance add --all --for 30m
  sentinelctl maintenance add db01 --from "2026-12-01 02:00" --for 90m`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			if all == (len(args) == 1) {
				return errors.New("cho một server (sentinelctl maintenance add <server>) hoặc cho tất cả (--all), không phải cả hai hay không cái nào")
			}
			if length <= 0 {
				return errors.New("--for là thời gian của cửa sổ, ví dụ 2h hoặc 90m")
			}
			start := time.Now()
			var startPointer *time.Time
			if from != "" {
				if start, err = parseStart(from); err != nil {
					return err
				}
				startPointer = &start
			}
			agent := ""
			if len(args) == 1 {
				if agent = args[0]; agent == "" {
					return errors.New("tên server trống (biến shell chưa đặt?): một cửa sổ cho mọi server phải ghi rõ --all")
				}
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			made, err := api.AddWindow(cmd.Context(), agent, startPointer, start.Add(length), reason)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), made)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Đã mở cửa sổ bảo trì #%d cho %s, từ %s đến %s.\n", made.ID, windowTarget(made), made.StartsAt.Local().Format("2006-01-02 15:04"), made.EndsAt.Local().Format("2006-01-02 15:04"))
			return nil
		},
	}
	addCmd.Flags().BoolVar(&all, "all", false, "cho mọi server")
	addCmd.Flags().DurationVar(&length, "for", 0, "thời gian của cửa sổ (2h, 90m …)")
	addCmd.Flags().StringVar(&from, "from", "", `giờ bắt đầu ("2026-12-01 02:00" giờ máy này, hoặc RFC 3339); mặc định là bây giờ`)
	addCmd.Flags().StringVar(&reason, "reason", "", "ghi chú")
	return addCmd
}

func windowTarget(one ctlapi.Window) string {
	if one.Agent == "" {
		return "mọi server"
	}
	return one.Agent
}

func newMaintenanceListCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Liệt kê các cửa sổ đang mở hoặc sắp tới",
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
			windows, err := api.Windows(cmd.Context())
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), windows)
			}
			printWindows(cmd.OutOrStdout(), windows)
			return nil
		},
	}
}

func printWindows(out io.Writer, windows []ctlapi.Window) {
	if len(windows) == 0 {
		fmt.Fprintln(out, "Không có cửa sổ bảo trì nào đang mở hay sắp tới: sentinelctl maintenance add <server> --for 2h")
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "SỐ\tSERVER\tTỪ\tĐẾN\tTRẠNG THÁI\tGHI CHÚ")
	for _, one := range windows {
		state := "sắp tới"
		if one.Open {
			state = "đang mở"
		}
		fmt.Fprintf(table, "#%d\t%s\t%s\t%s\t%s\t%s\n", one.ID, windowTarget(one), one.StartsAt.Local().Format("2006-01-02 15:04"), one.EndsAt.Local().Format("2006-01-02 15:04"), state, one.Reason)
	}
	_ = table.Flush()
}

func newMaintenanceRemoveCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <số>",
		Aliases: []string{"remove"},
		Short:   "Xoá một cửa sổ (đang mở thì kết thúc ngay)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id < 1 {
				return fmt.Errorf("số của cửa sổ phải là số nguyên dương, không phải %q", args[0])
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			if err := api.RemoveWindow(cmd.Context(), id); err != nil {
				return explain(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Đã xoá cửa sổ bảo trì #%d.\n", id)
			return nil
		},
	}
}
