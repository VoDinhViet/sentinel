package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fillHistory writes perName results for each of names, oldest first, and a backup run history of
// the same size for each (job, repository) pair, in one transaction.
func fillHistory(t *testing.T, s *Store, names []string, perName int, start time.Time) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	for i := range perName {
		at := start.Add(time.Duration(i) * time.Minute).Unix()
		for _, name := range names {
			if _, err := tx.Exec(`INSERT INTO check_results (name, at, ok, latency_ms, error) VALUES (?, ?, ?, 5, '')`, name, at, i%7 != 0); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO backup_runs (job, repository, started_at, finished_at, success) VALUES (?, 'main', ?, ?, ?)`, name, at, at, i%7 != 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusQueriesStayFastOnALongHistory(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	names := []string{"api", "db", "web", "mail", "cache"}
	const perName = 10000 // 50k rows in each table: a month of one check every minute
	fillHistory(t, s, names, perName, time.Unix(1_700_000_000, 0))

	began := time.Now()
	latest, err := s.LatestChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := s.RecentChecks(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	latestRuns, err := s.LatestBackups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recentRuns, err := s.RecentBackups(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 3*time.Second { // the correlated queries took minutes
		t.Errorf("the four status queries took %v on 50k rows", took)
	}

	if len(latest) != len(names) || len(latestRuns) != len(names) {
		t.Fatalf("latest: %d checks, %d runs, want %d each", len(latest), len(latestRuns), len(names))
	}
	for _, check := range latest {
		if want := time.Unix(1_700_000_000, 0).Add((perName - 1) * time.Minute); !check.At.Equal(want) {
			t.Errorf("latest %s is at %v, want the newest %v", check.Name, check.At, want)
		}
	}
	if len(recent) != 8*len(names) || len(recentRuns) != 8*len(names) {
		t.Fatalf("recent: %d checks, %d runs, want %d each", len(recent), len(recentRuns), 8*len(names))
	}
	for i := 1; i < len(recent); i++ { // by name, and oldest first within a name
		if before, after := recent[i-1], recent[i]; before.Name > after.Name || (before.Name == after.Name && before.ID >= after.ID) {
			t.Fatalf("recent checks out of order at %d: %+v then %+v", i, before, after)
		}
	}
}

func TestStatusQueriesUseTheirIndexes(t *testing.T) {
	s := openTemp(t)
	fillHistory(t, s, []string{"api"}, 10, time.Unix(1_700_000_000, 0))
	for _, tc := range []struct{ query, index string }{
		{`SELECT id FROM check_results WHERE name = 'api' ORDER BY id DESC LIMIT 8`, "check_results_name_id"},
		{`SELECT id FROM backup_runs WHERE job = 'api' AND repository = 'main' ORDER BY id DESC LIMIT 8`, "backup_runs_job_repository_id"},
	} {
		plan := queryPlan(t, s, tc.query)
		if !strings.Contains(plan, tc.index) {
			t.Errorf("%s\nplan: %s\nwant it to use %s", tc.query, plan, tc.index)
		}
	}
}

func queryPlan(t *testing.T, s *Store, query string) string {
	t.Helper()
	planRows, err := s.db.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatal(err)
	}
	defer planRows.Close()
	var plan []string
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	return strings.Join(plan, "; ")
}

func TestPruneDeletesEveryBatch(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	old := time.Unix(1_600_000_000, 0)
	recent := time.Unix(1_700_000_000, 0)
	fillHistory(t, s, []string{"web", "api"}, pruneBatch+500, old) // two names: more than two batches of old rows
	fillHistory(t, s, []string{"web"}, 3, recent)

	if err := s.Prune(ctx, recent.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM check_results`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 3 {
		t.Errorf("%d results left after the prune, want only the 3 recent ones", left)
	}
	if err := s.Prune(ctx, recent.Add(-time.Hour)); err != nil {
		t.Errorf("a second prune of nothing: %v", err)
	}
}
