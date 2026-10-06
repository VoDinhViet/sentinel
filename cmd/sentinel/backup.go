package main

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/backup"
	"sentinel/internal/config"
)

func (a *app) backupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Run and manage backups"}
	var repoFlag string

	// pick returns the job and the repositories a command should act on.
	pick := func(cfg *config.Config, jobName string) (*config.Backup, []string, error) {
		job, err := cfg.FindBackup(jobName)
		if err != nil {
			return nil, nil, err
		}
		if repoFlag == "" {
			return job, job.Repositories, nil
		}
		for _, r := range job.Repositories {
			if r == repoFlag {
				return job, []string{r}, nil
			}
		}
		return nil, nil, fmt.Errorf("job %q does not use repository %q", jobName, repoFlag)
	}

	initCmd := &cobra.Command{
		Use:   "init <job>",
		Short: "Initialize the restic repositories used by a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			_, repos, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			runner := &backup.Runner{Config: cfg}
			var errs []error
			for _, name := range repos {
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
		Short: "Run a backup job now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, repos, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			jobCopy := *job
			jobCopy.Repositories = repos
			runs := (&backup.Runner{Config: cfg, Store: st}).Run(cmd.Context(), &jobCopy)

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "REPOSITORY\tRESULT\tSNAPSHOT\tADDED\tDURATION")
			failed := 0
			for _, r := range runs {
				result := "ok"
				if !r.Success {
					result, failed = "FAILED: "+r.Error, failed+1
				}
				fmt.Fprintf(w, "%s\t%s\t%.8s\t%s\t%s\n", r.Repository, result, r.SnapshotID,
					humanBytes(r.BytesAdded), r.FinishedAt.Sub(r.StartedAt).Round(time.Second))
			}
			w.Flush()
			if failed > 0 {
				return fmt.Errorf("%d of %d repositories failed", failed, len(runs))
			}
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:   "list <job>",
		Short: "List a job's snapshots",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, repos, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			runner := &backup.Runner{Config: cfg}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "REPOSITORY\tSNAPSHOT\tTIME\tSIZE")
			var errs []error
			for _, name := range repos {
				rc, _ := runner.Restic(name)
				snaps, err := rc.Snapshots(cmd.Context(), backup.JobTags(job))
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
				for _, s := range snaps {
					size := ""
					if s.Summary != nil {
						size = humanBytes(s.Summary.TotalBytes)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, s.ShortID, s.Time.Local().Format("2006-01-02 15:04"), size)
				}
			}
			w.Flush()
			return errors.Join(errs...)
		},
	}

	var target string
	restoreCmd := &cobra.Command{
		Use:   "restore <job> <snapshot>",
		Short: "Restore a snapshot into a directory",
		Long: "Restore a snapshot into --target. Use \"latest\" for the newest snapshot.\n" +
			"Postgres jobs restore a <database>.dump file; load it with pg_restore.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			job, repos, err := pick(cfg, args[0])
			if err != nil {
				return err
			}
			if repoFlag == "" && len(repos) > 1 {
				repos = repos[:1]
			}
			rc, _ := (&backup.Runner{Config: cfg}).Restic(repos[0])
			snapshot := args[1]
			if snapshot == "latest" {
				snaps, err := rc.Snapshots(cmd.Context(), backup.JobTags(job))
				if err != nil {
					return err
				}
				if len(snaps) == 0 {
					return fmt.Errorf("no snapshots for job %q in %s", job.Name, repos[0])
				}
				snapshot = snaps[len(snaps)-1].ID
			}
			if err := rc.Restore(cmd.Context(), snapshot, target); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %.8s from %s into %s\n", snapshot, repos[0], target)
			if job.Type == config.BackupPostgres {
				fmt.Fprintf(cmd.OutOrStdout(), "load it with: pg_restore --clean --if-exists -d %s %s/%s.dump\n",
					job.Postgres.Database, target, job.Postgres.Database)
			}
			return nil
		},
	}
	restoreCmd.Flags().StringVar(&target, "target", "", "directory to restore into (required)")
	restoreCmd.MarkFlagRequired("target")

	for _, c := range []*cobra.Command{initCmd, runCmd, listCmd, restoreCmd} {
		c.Flags().StringVarP(&repoFlag, "repository", "r", "", "only this repository (default: all of the job's)")
		cmd.AddCommand(c)
	}
	return cmd
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
