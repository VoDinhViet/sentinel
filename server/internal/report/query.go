package report

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Point is the host at one moment, or the average of a step.
type Point struct {
	At          time.Time
	CPUPercent  float64
	Load1       float64
	MemPercent  float64
	DiskPercent float64
}

// Summary is a span of host samples in a few numbers.
type Summary struct {
	Samples                                 int
	CPUAvg, CPUMax, MemAvg, MemMax, LoadAvg float64
	DiskMax                                 float64
}

// HostHistory is the samples of an agent in the last hours, in steps of at least SampleEvery and at most
// MaxPoints points, each the average of the samples in it (the disk is the highest), with a summary of all.
func (s *Service) HostHistory(ctx context.Context, agent string, hours int, now time.Time) ([]Point, Summary, time.Duration, error) {
	span := time.Duration(hours) * time.Hour
	since := now.Add(-span)
	step := max(SampleEvery, span/MaxPoints/time.Second*time.Second)
	rows, err := s.store.DB.QueryContext(ctx, `SELECT (at / ?) * ? AS step_at, AVG(cpu_percent), AVG(load1), AVG(mem_percent), MAX(disk_percent)
		FROM host_samples WHERE agent = ? AND at >= ? GROUP BY step_at ORDER BY step_at`, int64(step/time.Second), int64(step/time.Second), agent, since.Unix())
	if err != nil {
		return nil, Summary{}, 0, fmt.Errorf("read host history: %w", err)
	}
	defer rows.Close()
	var points []Point
	for rows.Next() {
		var point Point
		var at int64
		if err := rows.Scan(&at, &point.CPUPercent, &point.Load1, &point.MemPercent, &point.DiskPercent); err != nil {
			return nil, Summary{}, 0, fmt.Errorf("read host sample: %w", err)
		}
		point.At = time.Unix(at, 0)
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, Summary{}, 0, fmt.Errorf("read host history: %w", err)
	}
	var summary Summary
	err = s.store.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(AVG(cpu_percent), 0), COALESCE(MAX(cpu_percent), 0), COALESCE(AVG(mem_percent), 0),
		COALESCE(MAX(mem_percent), 0), COALESCE(AVG(load1), 0), COALESCE(MAX(disk_percent), 0) FROM host_samples WHERE agent = ? AND at >= ?`, agent, since.Unix()).
		Scan(&summary.Samples, &summary.CPUAvg, &summary.CPUMax, &summary.MemAvg, &summary.MemMax, &summary.LoadAvg, &summary.DiskMax)
	if err != nil {
		return nil, Summary{}, 0, fmt.Errorf("summarize host history: %w", err)
	}
	return points, summary, step, nil
}

// Run is one finished backup, as an agent reported it.
type Run struct {
	Agent, Job, Repository string
	FinishedAt             time.Time
	Success                bool
	Result                 string
	BytesAdded             int64
}

// JobSummary is how one job of one agent went to one repository in a span.
type JobSummary struct {
	Agent, Job, Repository string
	Runs, Failures         int
	LastFinishedAt         time.Time
	LastSuccessAt          *time.Time
	LastResult             string
	BytesAdded             int64
}

// Backups summarizes the runs of the last hours, of one agent or (with "") of all, with the failed runs
// newest first. A job whose runs are all older than the span is not in it.
func (s *Service) Backups(ctx context.Context, agent string, hours int, now time.Time) ([]JobSummary, []Run, error) {
	since := now.Add(-time.Duration(hours) * time.Hour).Unix()
	until := now.Unix() // a run that says it ended later than now is not believed
	// The last success is looked up in all the runs kept, not only in the span: a job whose last good run is older than
	// the span did succeed, and "never" would say it did not.
	rows, err := s.store.DB.QueryContext(ctx, `SELECT agent, job, repository, COUNT(*), COALESCE(SUM(1 - success), 0), MAX(finished_at),
		(SELECT MAX(ok.finished_at) FROM backup_runs ok WHERE ok.agent = r.agent AND ok.job = r.job AND ok.repository = r.repository AND ok.success = 1 AND ok.finished_at <= ?),
		COALESCE(SUM(bytes_added), 0)
		FROM backup_runs r WHERE finished_at >= ? AND finished_at <= ? AND (? = '' OR agent = ?) GROUP BY agent, job, repository ORDER BY agent, job, repository`, until, since, until, agent, agent)
	if err != nil {
		return nil, nil, fmt.Errorf("read backup runs: %w", err)
	}
	defer rows.Close()
	var jobs []JobSummary
	for rows.Next() {
		var one JobSummary
		var last int64
		var lastSuccess sql.NullInt64
		if err := rows.Scan(&one.Agent, &one.Job, &one.Repository, &one.Runs, &one.Failures, &last, &lastSuccess, &one.BytesAdded); err != nil {
			return nil, nil, fmt.Errorf("read backup run: %w", err)
		}
		one.LastFinishedAt = time.Unix(last, 0)
		if lastSuccess.Valid {
			at := time.Unix(lastSuccess.Int64, 0)
			one.LastSuccessAt = &at
		}
		jobs = append(jobs, one)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read backup runs: %w", err)
	}
	for i := range jobs {
		err := s.store.DB.QueryRowContext(ctx, `SELECT result FROM backup_runs WHERE agent = ? AND job = ? AND repository = ? AND finished_at <= ? ORDER BY finished_at DESC LIMIT 1`,
			jobs[i].Agent, jobs[i].Job, jobs[i].Repository, until).Scan(&jobs[i].LastResult)
		if err != nil {
			return nil, nil, fmt.Errorf("read the last result: %w", err)
		}
	}
	failed, err := s.failedRuns(ctx, agent, since, until)
	return jobs, failed, err
}

// StaleJob is a job whose last good run is older than a span, or that has never had one.
type StaleJob struct {
	Agent, Job, Repository string
	LastSuccessAt          *time.Time
}

// Stale lists the jobs of active agents with no successful run in the last age, from all the runs kept. A job whose
// first run is younger than age and failed is new, not stale. A job an
// agent no longer reports is not here for ever: its runs leave the history after RunKeep.
func (s *Service) Stale(ctx context.Context, age time.Duration, now time.Time) ([]StaleJob, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT r.agent, r.job, r.repository, MAX(CASE WHEN r.success = 1 THEN r.finished_at END) AS last_ok
		FROM backup_runs r JOIN agents a ON a.name = r.agent WHERE a.state = 'active' AND r.finished_at <= ?
		GROUP BY r.agent, r.job, r.repository HAVING (last_ok IS NULL AND MIN(r.finished_at) < ?) OR last_ok < ? ORDER BY r.agent, r.job, r.repository LIMIT ?`,
		now.Unix(), now.Add(-age).Unix(), now.Add(-age).Unix(), maxFailures*5)
	if err != nil {
		return nil, fmt.Errorf("read stale jobs: %w", err)
	}
	defer rows.Close()
	var stale []StaleJob
	for rows.Next() {
		var one StaleJob
		var last sql.NullInt64
		if err := rows.Scan(&one.Agent, &one.Job, &one.Repository, &last); err != nil {
			return nil, fmt.Errorf("read stale job: %w", err)
		}
		if last.Valid {
			at := time.Unix(last.Int64, 0)
			one.LastSuccessAt = &at
		}
		stale = append(stale, one)
	}
	return stale, rows.Err()
}

func (s *Service) failedRuns(ctx context.Context, agent string, since, until int64) ([]Run, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT agent, job, repository, finished_at, result, bytes_added FROM backup_runs
		WHERE success = 0 AND finished_at >= ? AND finished_at <= ? AND (? = '' OR agent = ?) ORDER BY finished_at DESC LIMIT ?`, since, until, agent, agent, maxFailures)
	if err != nil {
		return nil, fmt.Errorf("read failed runs: %w", err)
	}
	defer rows.Close()
	var failed []Run
	for rows.Next() {
		var one Run
		var at int64
		if err := rows.Scan(&one.Agent, &one.Job, &one.Repository, &at, &one.Result, &one.BytesAdded); err != nil {
			return nil, fmt.Errorf("read failed run: %w", err)
		}
		one.FinishedAt = time.Unix(at, 0)
		failed = append(failed, one)
	}
	return failed, rows.Err()
}
