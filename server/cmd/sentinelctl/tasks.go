package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/client"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/format"
)

func newRunCmd(c *cli) *cobra.Command {
	var wait, follow bool
	runCmd := &cobra.Command{
		Use:   "run <server> <action> [tên=giá-trị …]",
		Short: "Giao một việc cho một server (backup, doctor …)",
		Long: `Xếp một action vào hàng đợi của server. Server (agent) tự lấy việc, kiểm tra allowlist trong config của nó
(control_plane.allow, mặc định tắt hết) rồi mới chạy, nên server có thể từ chối. Action nằm trong một bảng cố định,
không có action nào chạy lệnh shell hay nhận đường dẫn.

Action:
` + actionHelp() + `
Việc không được server lấy trong 10 phút sẽ hết hạn.
--wait đợi đến khi việc kết thúc; --follow cũng in log từng dòng khi nó chạy. Cả hai thoát với mã khác 0 nếu việc không thành công.`,
		Example: `  sentinelctl run web01 doctor.run --follow
  sentinelctl run web01 backup.start job=db
  sentinelctl run web01 backup.snapshots job=db repository=s3 --wait`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			params, err := parseParams(args[2:])
			if err != nil {
				return err
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			created, err := api.CreateTask(cmd.Context(), args[0], args[1], params)
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if !wait && !follow {
				if asJSON {
					return printJSON(out, created)
				}
				fmt.Fprintf(out, "Đã xếp việc #%d (%s) cho %s. Xem: sentinelctl task show %d\n", created.ID, created.Action, created.Agent, created.ID)
				return nil
			}
			ended, err := followTask(cmd, api, created, follow && !asJSON)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				if err := printJSON(out, ended); err != nil {
					return err
				}
			} else {
				printTaskEnd(out, ended)
			}
			if ended.State != agentapi.TaskSucceeded {
				return fmt.Errorf("việc #%d kết thúc với trạng thái %s", ended.ID, ended.State)
			}
			return nil
		},
	}
	runCmd.Flags().BoolVar(&wait, "wait", false, "đợi việc kết thúc rồi in kết quả")
	runCmd.Flags().BoolVar(&follow, "follow", false, "đợi và in log của việc khi nó chạy")
	return runCmd
}

// actionHelp lists the actions of the table with their parameters, for the help of run.
func actionHelp() string {
	var text strings.Builder
	for _, action := range agentapi.Actions() {
		required, optional := agentapi.Params(action)
		parts := []string{}
		for _, name := range required {
			parts = append(parts, name+"=…")
		}
		for _, name := range optional {
			parts = append(parts, "["+name+"=…]")
		}
		fmt.Fprintf(&text, "  %-18s %s\n", action, strings.Join(parts, " "))
	}
	return text.String()
}

// parseParams reads "name=value" arguments. The value may have '=' in it; a name given twice is an error.
func parseParams(args []string) (map[string]string, error) {
	params := map[string]string{}
	for _, arg := range args {
		name, value, found := strings.Cut(arg, "=")
		if !found || name == "" {
			return nil, fmt.Errorf("tham số %q phải có dạng tên=giá-trị", arg)
		}
		if _, repeated := params[name]; repeated {
			return nil, fmt.Errorf("tham số %q được cho hai lần", name)
		}
		params[name] = value
	}
	return params, nil
}

// followTask waits for a task to end, printing its log as it comes when show is set.
func followTask(cmd *cobra.Command, api *client.Client, created ctlapi.Task, show bool) (ctlapi.Task, error) {
	out := cmd.OutOrStdout()
	return api.FollowTask(cmd.Context(), created.ID, func(event client.TaskEvent) error {
		switch {
		case show && event.Kind == ctlapi.EventState:
			fmt.Fprintf(out, "[%s]\n", event.State)
		case show && event.Kind == ctlapi.EventLog:
			fmt.Fprintf(out, "  %s  %s\n", event.Line.At.Local().Format("15:04:05"), strings.ReplaceAll(event.Line.Text, "\n", "\n            "))
		}
		return nil
	})
}

// printTaskEnd is what a task ended with: the error, and the output.
func printTaskEnd(out io.Writer, task ctlapi.Task) {
	fmt.Fprintf(out, "Việc #%d: %s\n", task.ID, task.State)
	if task.Error != "" {
		fmt.Fprintf(out, "Lỗi: %s\n", task.Error)
	}
	if task.Output != "" {
		fmt.Fprintf(out, "Kết quả:\n%s\n", indent(task.Output, "  "))
	}
}

func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n"+prefix)
}

func newTaskCmd(c *cli) *cobra.Command {
	taskCmd := &cobra.Command{Use: "task", Short: "Các việc đã giao cho server"}
	taskCmd.AddCommand(newTaskListCmd(c), newTaskShowCmd(c), newTaskCancelCmd(c))
	return taskCmd
}

func newTaskListCmd(c *cli) *cobra.Command {
	var serverName string
	var limit int
	listCmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Liệt kê các việc mới nhất",
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
			tasks, err := api.Tasks(cmd.Context(), serverName, limit)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), tasks)
			}
			printTasks(cmd.OutOrStdout(), tasks, time.Now())
			return nil
		},
	}
	listCmd.Flags().StringVar(&serverName, "server", "", "chỉ các việc của server này")
	listCmd.Flags().IntVarP(&limit, "limit", "n", 20, "số việc tối đa (1 đến 200)")
	return listCmd
}

func printTasks(out io.Writer, tasks []ctlapi.Task, now time.Time) {
	if len(tasks) == 0 {
		fmt.Fprintln(out, "Chưa có việc nào: sentinelctl run <server> <action>")
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "SỐ\tSERVER\tACTION\tTHAM SỐ\tTRẠNG THÁI\tXẾP LÚC")
	for _, task := range tasks {
		fmt.Fprintf(table, "#%d\t%s\t%s\t%s\t%s\t%s\n", task.ID, task.Agent, task.Action, paramsText(task.Params), task.State, format.Since(task.CreatedAt, now))
	}
	_ = table.Flush()
}

// paramsText is "job=db repository=s3", in order of name, or "-".
func paramsText(params map[string]string) string {
	if len(params) == 0 {
		return "-"
	}
	parts := []string{}
	for _, name := range slices.Sorted(maps.Keys(params)) {
		parts = append(parts, name+"="+params[name])
	}
	return strings.Join(parts, " ")
}

func newTaskShowCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "show <số>",
		Short: "Xem một việc: trạng thái, log và kết quả",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskNumber(args[0])
			if err != nil {
				return err
			}
			asJSON, err := c.wantsJSON()
			if err != nil {
				return err
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			detail, err := api.Task(cmd.Context(), id)
			if err != nil {
				return explain(err)
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), detail)
			}
			printTaskDetail(cmd.OutOrStdout(), detail)
			return nil
		},
	}
}

// taskNumber reads "12" or "#12".
func taskNumber(text string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(text, "#"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%q không phải số của một việc", text)
	}
	return id, nil
}

func printTaskDetail(out io.Writer, detail ctlapi.TaskDetail) {
	fmt.Fprintf(out, "Việc #%d  %s  %s  %s\n", detail.ID, detail.Action, paramsText(detail.Params), detail.State)
	fmt.Fprintf(out, "  Server:    %s\n", detail.Agent)
	fmt.Fprintf(out, "  Xếp lúc:   %s, bởi %s\n", detail.CreatedAt.Local().Format("2006-01-02 15:04:05"), detail.CreatedBy)
	for _, step := range []struct {
		label string
		at    *time.Time
	}{{"Gửi lúc:", detail.SentAt}, {"Chạy lúc:", detail.StartedAt}, {"Xong lúc:", detail.FinishedAt}} {
		if step.at != nil {
			fmt.Fprintf(out, "  %-10s %s\n", step.label, step.at.Local().Format("2006-01-02 15:04:05"))
		}
	}
	if detail.Error != "" {
		fmt.Fprintf(out, "  Lỗi:       %s\n", detail.Error)
	}
	if len(detail.Log) > 0 {
		fmt.Fprintln(out, "\nLog:")
		for _, line := range detail.Log {
			fmt.Fprintf(out, "  %s  %s\n", line.At.Local().Format("15:04:05"), strings.ReplaceAll(line.Text, "\n", "\n            "))
		}
	}
	if detail.Output != "" {
		fmt.Fprintf(out, "\nKết quả:\n%s\n", indent(detail.Output, "  "))
	}
}

func newTaskCancelCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <số>",
		Short: "Rút lại một việc server chưa lấy",
		Long:  "Chỉ rút được việc còn đang chờ. Việc server đã lấy thì không dừng được từ đây; với backup đang chạy, dùng: sentinelctl run <server> backup.stop job=…",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskNumber(args[0])
			if err != nil {
				return err
			}
			api, _, err := c.connect()
			if err != nil {
				return explain(err)
			}
			if err := api.CancelTask(cmd.Context(), id); err != nil {
				var refused *client.Error
				if errors.As(err, &refused) && refused.Status == 409 {
					return fmt.Errorf("việc #%d không còn chờ nữa (server đã lấy hoặc đã kết thúc)", id)
				}
				return explain(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Đã rút việc #%d.\n", id)
			return nil
		},
	}
}
