package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"sentinel/internal/config"
	"sentinel/internal/tui"
)

// runInitForm shows the init form and tells what was written.
func (a *cliApp) runInitForm(cmd *cobra.Command, force bool) error {
	root := os.Geteuid() == 0
	path := config.DefaultLocations(a.configFlag).WritePath(root)
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s đã có: sửa bằng `sentinel manage`, hoặc dùng --force để thay (file cũ được giữ thành %s.old)", path, path)
	}
	outcome, err := tui.RunInit(cmd.Context(), path, root, force)
	if err != nil {
		return err
	}
	if outcome == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "Chưa ghi gì cả.")
		return nil
	}
	printInitOutcome(cmd.OutOrStdout(), outcome)
	return nil
}

func (a *cliApp) initCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Tạo file config đầu tiên bằng cách điền một form",
		Long: "Hỏi nơi lưu backup và cần backup gì, rồi ghi một file config ngắn.\n" +
			"Root ghi /etc/sentinel/config.yaml (và mật khẩu kho ngẫu nhiên vào sentinel.env);\n" +
			"người dùng thường ghi ~/.config/sentinel/config.yaml. Dùng --config hoặc $SENTINEL_CONFIG để chọn file khác.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runInitForm(cmd, force) },
	}
	cmd.Flags().BoolVar(&force, "force", false, "ghi đè file config đã có (file cũ được giữ thành .old)")
	return cmd
}

func printInitOutcome(w io.Writer, outcome *tui.InitOutcome) {
	fmt.Fprintf(w, "Đã ghi %s\n\n", outcome.Path)
	switch {
	case outcome.SecretWritten:
		fmt.Fprintf(w, "Đã ghi mật khẩu repository ngẫu nhiên vào %s (mode 0600) với tên %s.\n"+
			"Hãy giữ một bản sao ở nơi khác: mất mật khẩu là mất mọi backup.\n\n", outcome.EnvFile, outcome.PasswordEnv)
		fmt.Fprintf(w, "Tiếp:\n  set -a; . %s; set +a\n", outcome.EnvFile)
	case os.Getenv(outcome.PasswordEnv) == "":
		fmt.Fprintf(w, "Tiếp: sentinel sẽ hỏi %s lần đầu cần dùng, và có thể lưu vào %s.\n"+
			"Dùng passphrase dài và giữ một bản sao ở nơi khác: mất mật khẩu là mất backup.\n",
			outcome.PasswordEnv, filepath.Join(filepath.Dir(outcome.Path), config.EnvFileName))
	default:
		fmt.Fprintln(w, "Tiếp:")
	}
	fmt.Fprintf(w, "  sentinel backup init %s     # tạo repository, chỉ một lần\n", outcome.JobName)
	fmt.Fprintf(w, "  sentinel backup run %s      # backup ngay\n", outcome.JobName)
	if outcome.NotifierKind != "" {
		envFile := outcome.EnvFile
		if envFile == "" {
			envFile = filepath.Join(filepath.Dir(outcome.Path), config.EnvFileName)
		}
		fmt.Fprintf(w, "  # cảnh báo qua %s: thêm %s vào %s (mode 0600), rồi thử:\n", outcome.NotifierKind, strings.Join(outcome.NotifierVars, ", ")+"=<giá trị>", envFile)
		fmt.Fprintln(w, "  sentinel notify test")
	}
	fmt.Fprintln(w, "  sentinel doctor")
	fmt.Fprintln(w, "  sentinel manage             # sửa mọi thứ ngay trong terminal")
}
