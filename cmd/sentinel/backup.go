package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/restic"
)

func (a *app) backupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Chạy và quản lý backup"}
	var repoFlag string

	// pick returns the job a command should act on: all of its repositories, or only
	// --repository when given.
	pick := func(cfg *config.Config, jobName string) (*config.Job, error) {
		job, err := cfg.FindJob(jobName)
		if err != nil {
			return nil, err
		}
		if repoFlag == "" {
			return job, nil
		}
		return job.OnlyRepo(repoFlag)
	}

	initCmd := &cobra.Command{
		Use:   "init <job>",
		Short: "Khởi tạo các kho restic mà job dùng",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			runner := &backup.Runner{Config: cfg}
			var errs []error
			for _, name := range job.Repositories {
				rc, _ := runner.Restic(name)
				existed, err := rc.Init(cmd.Context())
				switch {
				case err != nil:
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					fmt.Fprintf(cmd.OutOrStdout(), "%-12s FAILED: %v\n", name, err)
				case existed:
					fmt.Fprintf(cmd.OutOrStdout(), "%-12s already initialized\n", name)
				default:
					fmt.Fprintf(cmd.OutOrStdout(), "%-12s initialized\n", name)
				}
			}
			return errors.Join(errs...)
		},
	}

	var detach bool
	var runOutput string
	runCmd := &cobra.Command{
		Use:   "run <job>",
		Short: "Chạy một job backup ngay",
		Long: "Chạy backup của job vào mọi kho của nó (hoặc --repository), và in tiến độ.\n" +
			"Với --detach, backup chạy ở một tiến trình riêng tách khỏi terminal: đóng terminal hay mất SSH\n" +
			"không làm nó dừng. Xem tiến độ bằng `sentinel status` hoặc `sentinel manage`, dừng bằng `sentinel backup stop`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(runOutput); err != nil {
				return err
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			if detach {
				pid, logPath, err := startDetachedBackup(cfg, a.configPath, job.Name, repoFlag)
				if err != nil {
					return err
				}
				if runOutput == outputJSON {
					return writeJSON(cmd.OutOrStdout(), detachedReport{Job: job.Name, PID: pid, Log: logPath})
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "backup of %s started in the background (pid %d)\nlog:    %s\nwatch:  sentinel status   (or sentinel manage)\nstop:   sentinel backup stop %s\n", job.Name, pid, logPath, job.Name)
				return nil
			}
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			printProgress := newProgressPrinter(cmd.ErrOrStderr(), stderrIsTerminal(), time.Now)
			runs := (&backup.Runner{Config: cfg, Store: st, OnProgress: printProgress.show}).Run(cmd.Context(), job)
			printProgress.done()

			failed := 0
			for _, run := range runs {
				if !run.Success {
					failed++
				}
			}
			if runOutput == outputJSON {
				if err := writeJSON(cmd.OutOrStdout(), newBackupRunReport(job.Name, runs)); err != nil {
					return err
				}
			} else {
				table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(table, "REPOSITORY\tRESULT\tSNAPSHOT\tADDED\tDURATION")
				for _, run := range runs {
					fmt.Fprintf(table, "%s\t%s\t%.8s\t%s\t%s\n", run.Repository, run.Summary(), run.SnapshotID,
						backup.HumanBytes(run.BytesAdded), run.FinishedAt.Sub(run.StartedAt).Round(time.Second))
				}
				table.Flush()
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d repositories failed", failed, len(runs))
			}
			return nil
		},
	}

	runCmd.Flags().BoolVarP(&detach, "detach", "d", false, "chạy nền ở tiến trình riêng, không dừng khi đóng terminal")
	addOutputFlag(runCmd, &runOutput)

	stopCmd := &cobra.Command{
		Use:   "stop <job>",
		Short: "Dừng backup đang chạy của một job",
		Long: "Yêu cầu backup đang chạy của job dừng (dù do daemon, `backup run` hay `backup run --detach` chạy).\n" +
			"Backup bị dừng không để lại snapshot dở và không gây cảnh báo; dump database phải làm lại từ đầu lần sau.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, err := cfg.FindJob(args[0])
			if err != nil {
				return err
			}
			stopped, err := stopBackup(cfg, job.Name, 30*time.Second)
			if err != nil {
				return err
			}
			if !stopped {
				fmt.Fprintf(cmd.OutOrStdout(), "asked the backup of %s to stop; it is still shutting down (restic can take a moment)\n", job.Name)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "backup of %s stopped\n", job.Name)
			return nil
		},
	}

	var listOutput string
	listCmd := &cobra.Command{
		Use:   "list <job>",
		Short: "Liệt kê các snapshot của job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(listOutput); err != nil {
				return err
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			runner := &backup.Runner{Config: cfg}
			listing := snapListing{Job: job.Name, Repositories: make([]repoSnaps, 0, len(job.Repositories))}
			var errs []error
			for _, name := range job.Repositories {
				rc, _ := runner.Restic(name)
				snaps, err := rc.Snapshots(cmd.Context(), backup.JobTags(job))
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
				}
				listing.Repositories = append(listing.Repositories, newRepoSnaps(name, snaps, err))
			}
			if listOutput == outputJSON {
				if err := writeJSON(cmd.OutOrStdout(), listing); err != nil {
					return err
				}
			} else {
				printSnapListing(cmd.OutOrStdout(), listing)
			}
			return errors.Join(errs...)
		},
	}
	addOutputFlag(listCmd, &listOutput)

	var target string
	var force bool
	restoreCmd := &cobra.Command{
		Use:   "restore <job> <snapshot>",
		Short: "Khôi phục một snapshot vào một thư mục",
		Long: "Khôi phục snapshot vào --target. Dùng \"latest\" cho snapshot mới nhất.\n" +
			"Job database khôi phục ra file dump; lệnh để nạp lại file đó sẽ được in ra.\n" +
			"Thư mục đích chưa có sẽ được tạo với quyền 0700; thư mục có sẵn phải thuộc user đang chạy, không phải symlink\n" +
			"và không cho người khác ghi. Đích là thư mục hệ thống (/, /etc, /var...) hoặc một mount NFS/CIFS không đạt các điều kiện đó cần thêm --force.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if backup.IsSystemRestoreTarget(target) && !force {
				return fmt.Errorf("--target %s is a system folder: restoring there overwrites live files; pick another folder, or add --force if you mean it", target)
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			repoName := job.Repositories[0] // a restore reads one repository: the first unless --repository
			snapID, loadHint, err := (&backup.Runner{Config: cfg, TrustRestoreTarget: force}).Restore(cmd.Context(), job, repoName, args[1], target)
			if errors.Is(err, restic.ErrForeignSnapshot) {
				return fmt.Errorf("snapshot %s is not a snapshot of job %q in %s (see `sentinel backup list %s`)", args[1], job.Name, repoName, job.Name)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %.8s from %s into %s\n", snapID, repoName, target)
			if loadHint != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "load it with: %s\n", loadHint)
				fmt.Fprintln(cmd.OutOrStdout(), "this replaces existing data: try it on a scratch database or server first")
			}
			return nil
		},
	}
	restoreCmd.Flags().StringVar(&target, "target", "", "thư mục khôi phục vào (bắt buộc)")
	restoreCmd.Flags().BoolVar(&force, "force", false, "cho phép khôi phục vào thư mục hệ thống như / hoặc /etc, và bỏ kiểm tra chủ sở hữu/quyền của thư mục đích")
	restoreCmd.MarkFlagRequired("target")

	for _, sub := range []*cobra.Command{initCmd, runCmd, listCmd, restoreCmd} {
		sub.Flags().StringVarP(&repoFlag, "repository", "r", "", "chỉ kho này (mặc định: mọi kho của job)")
		cmd.AddCommand(sub)
	}
	cmd.AddCommand(stopCmd)
	return cmd
}

func stderrIsTerminal() bool {
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// progressInterval is how often a progress line goes to a log or a pipe; a terminal gets one
// line that is rewritten in place every time restic reports.
const progressInterval = 30 * time.Second

// progressPrinter shows how far a backup has got on stderr: one line rewritten in place on a
// terminal, a new line every 30 seconds (and at each phase) anywhere else, such as a log file.
type progressPrinter struct {
	out   io.Writer
	tty   bool
	now   func() time.Time
	mu    sync.Mutex
	last  time.Time
	phase string
	shown bool
}

func newProgressPrinter(out io.Writer, tty bool, now func() time.Time) *progressPrinter {
	return &progressPrinter{out: out, tty: tty, now: now}
}

func (p *progressPrinter) show(progress backup.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := fmt.Sprintf("%s → %s: %s", progress.Job, progress.Repository, progress.Summary())
	if p.tty {
		fmt.Fprintf(p.out, "\r\033[K%s", line)
		p.shown = true
		return
	}
	if now := p.now(); progress.Phase == p.phase && now.Sub(p.last) < progressInterval {
		return
	}
	p.last, p.phase = p.now(), progress.Phase
	fmt.Fprintln(p.out, line)
}

// done ends the rewritten line so the result table starts on a line of its own.
func (p *progressPrinter) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty && p.shown {
		fmt.Fprint(p.out, "\r\033[K")
	}
}
