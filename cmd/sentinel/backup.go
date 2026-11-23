package main

import (
	"errors"
	"fmt"
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

	runCmd := &cobra.Command{
		Use:   "run <job>",
		Short: "Chạy một job backup ngay",
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
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			runs := (&backup.Runner{Config: cfg, Store: st}).Run(cmd.Context(), job)

			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(table, "REPOSITORY\tRESULT\tSNAPSHOT\tADDED\tDURATION")
			failed := 0
			for _, run := range runs {
				summary := run.Summary()
				if !run.Success {
					failed++
				}
				fmt.Fprintf(table, "%s\t%s\t%.8s\t%s\t%s\n", run.Repository, summary, run.SnapshotID,
					humanBytes(run.BytesAdded), run.FinishedAt.Sub(run.StartedAt).Round(time.Second))
			}
			table.Flush()
			if failed > 0 {
				return fmt.Errorf("%d of %d repositories failed", failed, len(runs))
			}
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:   "list <job>",
		Short: "Liệt kê các snapshot của job",
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
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(table, "REPOSITORY\tSNAPSHOT\tTIME\tSIZE")
			var errs []error
			for _, name := range job.Repositories {
				rc, _ := runner.Restic(name)
				snaps, err := rc.Snapshots(cmd.Context(), backup.JobTags(job))
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
				for _, snap := range snaps {
					size := ""
					if snap.Summary != nil {
						size = humanBytes(snap.Summary.TotalBytes)
					}
					fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", name, snap.ShortID, snap.Time.Local().Format("2006-01-02 15:04"), size)
				}
			}
			table.Flush()
			return errors.Join(errs...)
		},
	}

	var target string
	restoreCmd := &cobra.Command{
		Use:   "restore <job> <snapshot>",
		Short: "Khôi phục một snapshot vào một thư mục",
		Long: "Khôi phục snapshot vào --target. Dùng \"latest\" cho snapshot mới nhất.\n" +
			"Job database khôi phục ra file dump; lệnh để nạp lại file đó sẽ được in ra.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			repoName := job.Repositories[0] // a restore reads one repository: the first unless --repository
			snapID, loadHint, err := (&backup.Runner{Config: cfg}).Restore(cmd.Context(), job, repoName, args[1], target)
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
	restoreCmd.MarkFlagRequired("target")

	for _, sub := range []*cobra.Command{initCmd, runCmd, listCmd, restoreCmd} {
		sub.Flags().StringVarP(&repoFlag, "repository", "r", "", "chỉ kho này (mặc định: mọi kho của job)")
		cmd.AddCommand(sub)
	}
	return cmd
}

func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for scaled := size / unit; scaled >= unit; scaled /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
