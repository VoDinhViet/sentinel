package report

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/store"
)

var base = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, name := range []string{"web01", "db01"} {
		if _, err := st.DB.Exec(`INSERT INTO agents (name, state, created_at) VALUES (?, 'active', 1)`, name); err != nil {
			t.Fatal(err)
		}
	}
	service := New(st)
	service.Now = func() time.Time { return base }
	return service
}

func hostReport(cpu float64, memUsedPercent, diskUsedPercent uint64) agentapi.Report {
	return agentapi.Report{Host: agentapi.HostStatus{
		CPUPercent: cpu, Load: [3]float64{1.5, 1, 1},
		Memory: agentapi.MemoryStatus{TotalBytes: 100, AvailableBytes: 100 - memUsedPercent},
		Disks:  []agentapi.DiskStatus{{Mount: "/", TotalBytes: 100, FreeBytes: 100 - diskUsedPercent}, {Mount: "/data", TotalBytes: 100, FreeBytes: 90}},
	}}
}

func TestASampleIsKeptAtMostEveryFiveMinutes(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	for minute := range 12 { // a report every minute for twelve minutes
		if err := s.Record(ctx, "web01", hostReport(float64(minute), 50, 70), base.Add(time.Duration(minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	points, summary, _, err := s.HostHistory(ctx, "web01", 1, base.Add(12*time.Minute))
	if err != nil || summary.Samples != 3 { // minutes 0, 5 and 10
		t.Fatalf("%d samples, %v", summary.Samples, err)
	}
	if len(points) == 0 || points[0].MemPercent != 50 || points[0].DiskPercent != 70 {
		t.Errorf("%+v: memory is the used share, disk the fullest one", points)
	}
	if other, _, _, _ := s.HostHistory(ctx, "db01", 1, base.Add(12*time.Minute)); len(other) != 0 {
		t.Errorf("the history of another server: %+v", other)
	}
}

func TestWhatCameFromAnotherMachineIsKeptWithinRange(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	wild := hostReport(math.NaN(), 50, 70)
	wild.Host.Memory.AvailableBytes = 1000 // more than the total
	if err := s.Record(ctx, "web01", wild, base); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, "db01", hostReport(5000, 50, 70), base); err != nil {
		t.Fatal(err)
	}
	for agent, want := range map[string]float64{"web01": 0, "db01": 100} {
		points, _, _, _ := s.HostHistory(ctx, agent, 1, base.Add(time.Minute))
		if len(points) != 1 || points[0].CPUPercent != want || points[0].MemPercent < 0 || points[0].MemPercent > 100 {
			t.Errorf("%s: %+v, want cpu %v", agent, points, want)
		}
	}
	// A report with no memory figure is no sample: it would say the host is idle.
	if err := s.Record(ctx, "web01", agentapi.Report{}, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, summary, _, _ := s.HostHistory(ctx, "web01", 3, base.Add(time.Hour)); summary.Samples != 1 {
		t.Errorf("%d samples", summary.Samples)
	}
}

func TestALongSpanIsAveragedIntoAtMostMaxPoints(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	for i := range 2000 {
		if err := s.Record(ctx, "web01", hostReport(float64(i%100), 50, 70), base.Add(-time.Duration(2000-i)*SampleEvery)); err != nil {
			t.Fatal(err)
		}
	}
	points, summary, step, err := s.HostHistory(ctx, "web01", 24*30, base)
	if err != nil || summary.Samples != 2000 || len(points) > MaxPoints+1 || step <= SampleEvery {
		t.Errorf("%d points, %d samples, step %v, %v", len(points), summary.Samples, step, err)
	}
}

func TestEachBackupRunIsKeptOnce(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	run := func(job string, at time.Duration, ok bool, result string) agentapi.BackupStatus {
		return agentapi.BackupStatus{Job: job, Repository: "s3", FinishedAt: base.Add(at), Success: ok, Result: result, BytesAdded: 100}
	}
	report := agentapi.Report{Backups: []agentapi.BackupStatus{run("db", -3*time.Hour, true, "ok"), run("files", -2*time.Hour, false, "FAILED: no space")}}
	for range 3 { // the same report every minute until the job runs again
		if err := s.Record(ctx, "web01", report, base); err != nil {
			t.Fatal(err)
		}
	}
	report.Backups = append(report.Backups, run("db", -time.Hour, false, "FAILED: timeout"))
	if err := s.Record(ctx, "web01", report, base); err != nil {
		t.Fatal(err)
	}
	jobs, failed, err := s.Backups(ctx, "", 24, base)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("%+v %v", jobs, err)
	}
	db, files := jobs[0], jobs[1]
	if db.Job != "db" || db.Runs != 2 || db.Failures != 1 || db.LastResult != "FAILED: timeout" || db.LastSuccessAt == nil || !db.LastSuccessAt.Equal(base.Add(-3*time.Hour)) || db.BytesAdded != 200 {
		t.Errorf("db: %+v", db)
	}
	if files.Runs != 1 || files.Failures != 1 || files.LastSuccessAt != nil {
		t.Errorf("files: %+v", files)
	}
	if len(failed) != 2 || failed[0].Job != "db" || failed[1].Job != "files" {
		t.Errorf("failed runs, newest first: %+v", failed)
	}
	if one, _, _ := s.Backups(ctx, "db01", 24, base); len(one) != 0 {
		t.Errorf("runs of another server: %+v", one)
	}
	if short, _, _ := s.Backups(ctx, "", 1, base.Add(-30*time.Minute+time.Hour)); len(short) != 0 {
		t.Errorf("a span that starts after the runs: %+v", short)
	}
}

func TestBackupRunsWithImpossibleTimesAreNotKept(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	report := agentapi.Report{Backups: []agentapi.BackupStatus{
		{Job: "future", Repository: "r", FinishedAt: base.Add(48 * time.Hour), Success: true, Result: "ok"},
		{Job: "ancient", Repository: "r", FinishedAt: base.Add(-200 * 24 * time.Hour), Success: true, Result: "ok"},
		{Job: "unset", Repository: "r", Success: true, Result: "ok"},
		{Job: "negative", Repository: "r", FinishedAt: base.Add(-time.Minute), Success: true, Result: "ok", BytesAdded: -5},
	}}
	if err := s.Record(ctx, "web01", report, base); err != nil {
		t.Fatal(err)
	}
	jobs, _, _ := s.Backups(ctx, "", 24*60, base)
	if len(jobs) != 1 || jobs[0].Job != "negative" || jobs[0].BytesAdded != 0 {
		t.Errorf("%+v", jobs)
	}
}

func TestOldHistoryIsDeleted(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	old := base.Add(-SampleKeep - time.Hour)
	if _, err := s.store.DB.Exec(`INSERT INTO host_samples VALUES ('web01', ?, 1, 1, 1, 1)`, old.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO backup_runs VALUES ('web01', 'j', 'r', ?, 1, 'ok', 0)`, base.Add(-RunKeep-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, "web01", hostReport(1, 1, 1), base); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(ctx, base); err != nil {
		t.Fatal(err)
	}
	var samples, runs int
	_ = s.store.DB.QueryRow(`SELECT COUNT(*) FROM host_samples`).Scan(&samples)
	_ = s.store.DB.QueryRow(`SELECT COUNT(*) FROM backup_runs`).Scan(&runs)
	if samples != 1 || runs != 0 {
		t.Errorf("%d samples and %d runs left", samples, runs)
	}
}
