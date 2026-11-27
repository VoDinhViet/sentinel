package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
	"sentinel/server/internal/task"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

var ctl = audit.WithActor(context.Background(), audit.Actor{Name: "ctl", Session: "abcd1234", IP: "127.0.0.1"})

type plant struct {
	*Service
	tasks *task.Service
	st    *store.Store
	now   time.Time
}

// newPlant is a scheduler over a database with web01 and db01 active, new01 pending and old01 revoked. Its
// clock is plant.now, which a test moves.
func newPlant(t *testing.T) *plant {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for name, state := range map[string]string{"web01": "active", "db01": "active", "new01": "pending", "old01": "revoked"} {
		if _, err := st.DB.Exec(`INSERT INTO agents (name, state, created_at) VALUES (?, ?, 1)`, name, state); err != nil {
			t.Fatal(err)
		}
	}
	tasks := task.New(st)
	p := &plant{Service: New(st, tasks), tasks: tasks, st: st, now: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)}
	p.Now = func() time.Time { return p.now }
	return p
}

func (p *plant) auditActions(t *testing.T) string {
	t.Helper()
	records, err := audit.List(context.Background(), p.st.DB, 100)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for i := len(records) - 1; i >= 0; i-- {
		actions = append(actions, records[i].Entry.Action+" "+records[i].Entry.Detail)
	}
	if _, err := audit.Verify(context.Background(), p.st.DB); err != nil {
		t.Fatalf("the audit chain is broken: %v", err)
	}
	return strings.Join(actions, "\n")
}

func TestAScheduleQueuesItsTaskWhenItIsDueAndNotBefore(t *testing.T) {
	p := newPlant(t)
	one, err := p.AddSchedule(ctl, "web01", "*/10 * * * *", agentapi.ActionBackupStart, map[string]string{"job": "db"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Tick(ctl, p.now); err != nil { // the minute it was made: nothing is due yet, or due only if it names this minute
		t.Fatal(err)
	}
	p.now = p.now.Add(11 * time.Minute)
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	queued, err := p.tasks.List(ctl, "web01", 10)
	if err != nil || len(queued) != 1 || queued[0].Action != agentapi.ActionBackupStart || queued[0].Params["job"] != "db" {
		t.Fatalf("queued %+v, %v", queued, err)
	}
	if queued[0].CreatedBy != "schedule:"+"1" {
		t.Errorf("created by %q", queued[0].CreatedBy)
	}
	again, _ := p.Schedule(ctl, one.ID)
	if again.LastTask == nil || *again.LastTask != queued[0].ID || again.LastRunAt == nil || again.LastError != "" {
		t.Errorf("the schedule kept %+v", again)
	}
	if err := p.Tick(ctl, p.now.Add(time.Minute)); err != nil { // the same run is not queued twice
		t.Fatal(err)
	}
	if queued, _ = p.tasks.List(ctl, "web01", 10); len(queued) != 1 {
		t.Errorf("%d tasks after a second look", len(queued))
	}
	// A run that queued its task is told by the task: one line in the audit log, not two.
	if actions := p.auditActions(t); !strings.Contains(actions, "schedule.create") || !strings.Contains(actions, "task.create") || strings.Contains(actions, "schedule.run") {
		t.Errorf("audit:\n%s", actions)
	}
}

func TestAScheduleThatCannotQueueKeepsTheReasonAndTriesAtTheNextRun(t *testing.T) {
	p := newPlant(t)
	one, err := p.AddSchedule(ctl, "web01", "@hourly", agentapi.ActionDoctorRun, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range task.MaxOpen { // people have filled the queue of the server
		if _, err := p.tasks.Create(ctl, "web01", agentapi.ActionDoctorRun, nil); err != nil {
			t.Fatal(err)
		}
	}
	p.now = p.now.Add(61 * time.Minute)
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	got, _ := p.Schedule(ctl, one.ID)
	if got.LastTask != nil || !strings.Contains(got.LastError, "too many") || got.LastRunAt == nil {
		t.Errorf("%+v", got)
	}
	if actions := p.auditActions(t); !strings.Contains(actions, "schedule.run #1 not queued") {
		t.Errorf("audit:\n%s", actions)
	}
	// The next run is tried when it is due, and the reason goes when it works.
	if _, err := p.st.DB.Exec(`UPDATE tasks SET state = 'canceled', finished_at = 1`); err != nil {
		t.Fatal(err)
	}
	p.now = p.now.Add(time.Hour)
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	if got, _ = p.Schedule(ctl, one.ID); got.LastTask == nil || got.LastError != "" {
		t.Errorf("after it worked: %+v", got)
	}
}

func TestAScheduleDoesNotPileUpTasksWhileItsLastOneIsStillOpen(t *testing.T) {
	p := newPlant(t)
	one, _ := p.AddSchedule(ctl, "web01", "* * * * *", agentapi.ActionDoctorRun, nil)
	for range 5 { // the server is down: nobody takes the task
		p.now = p.now.Add(time.Minute)
		if err := p.Tick(ctl, p.now); err != nil {
			t.Fatal(err)
		}
	}
	if queued, _ := p.tasks.List(ctl, "web01", 50); len(queued) != 1 {
		t.Fatalf("%d tasks are waiting for a server that is down", len(queued))
	}
	if _, err := p.st.DB.Exec(`UPDATE tasks SET state = 'succeeded', finished_at = 1`); err != nil {
		t.Fatal(err)
	}
	p.now = p.now.Add(time.Minute)
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	if queued, _ := p.tasks.List(ctl, "web01", 50); len(queued) != 2 {
		t.Errorf("%d tasks after the first one ended", len(queued))
	}
	if got, _ := p.Schedule(ctl, one.ID); got.LastTask == nil || *got.LastTask != 2 {
		t.Errorf("%+v", got)
	}
}

func TestARunIsClaimedBeforeItsTaskIsQueuedSoItIsNeverDoubled(t *testing.T) {
	p := newPlant(t)
	one, _ := p.AddSchedule(ctl, "web01", "@hourly", agentapi.ActionDoctorRun, nil)
	p.now = p.now.Add(61 * time.Minute)
	due := p.now.Truncate(time.Hour)
	if won, err := p.claim(ctl, one.ID, due); err != nil || !won { // something else took this run, and stopped before it queued anything
		t.Fatalf("%v %v", won, err)
	}
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	if queued, _ := p.tasks.List(ctl, "web01", 10); len(queued) != 0 {
		t.Errorf("a run that was claimed was queued again: %+v", queued)
	}
	if won, _ := p.claim(ctl, one.ID, due); won {
		t.Error("a run was claimed twice")
	}
}

func TestASchedulePassesOverAServerThatIsRevokedWithoutANoise(t *testing.T) {
	p := newPlant(t)
	one, _ := p.AddSchedule(ctl, "web01", "* * * * *", agentapi.ActionDoctorRun, nil)
	if _, err := p.st.DB.Exec(`UPDATE agents SET state = 'revoked' WHERE name = 'web01'`); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		p.now = p.now.Add(time.Minute)
		if err := p.Tick(ctl, p.now); err != nil {
			t.Fatal(err)
		}
	}
	if actions := p.auditActions(t); strings.Contains(actions, "schedule.run") || strings.Contains(actions, "task.create") {
		t.Errorf("a revoked server was written about:\n%s", actions)
	}
	if got, _ := p.Schedule(ctl, one.ID); got.LastRunAt != nil {
		t.Errorf("%+v", got)
	}
}

func TestAScheduleThatWasMissedIsNotStartedLate(t *testing.T) {
	p := newPlant(t)
	if _, err := p.AddSchedule(ctl, "web01", "CRON_TZ=UTC 0 3 * * *", agentapi.ActionDoctorRun, nil); err != nil {
		t.Fatal(err)
	}
	p.now = p.now.AddDate(0, 0, 3) // the control plane was down for three days
	p.now = time.Date(p.now.Year(), p.now.Month(), p.now.Day(), 3, 30, 0, 0, time.UTC)
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	if queued, _ := p.tasks.List(ctl, "web01", 10); len(queued) != 0 {
		t.Errorf("a run missed by half an hour was queued: %+v", queued)
	}
}

func TestAScheduleIsCheckedWhenItIsMade(t *testing.T) {
	p := newPlant(t)
	for name, tc := range map[string]struct {
		agent, spec, action string
		params              map[string]string
		want                error
	}{
		"a timetable that is not one":    {"web01", "every day", agentapi.ActionDoctorRun, nil, ErrInvalid},
		"an action that is not in table": {"web01", "@daily", "shell.run", map[string]string{"cmd": "id"}, ErrInvalid},
		"a parameter the action lacks":   {"web01", "@daily", agentapi.ActionDoctorRun, map[string]string{"path": "/"}, ErrInvalid},
		"a parameter that is missing":    {"web01", "@daily", agentapi.ActionBackupStart, nil, ErrInvalid},
		"a server that does not exist":   {"nobody", "@daily", agentapi.ActionDoctorRun, nil, ErrNoAgent},
		"a server that was revoked":      {"old01", "@daily", agentapi.ActionDoctorRun, nil, ErrNoAgent},
	} {
		if _, err := p.AddSchedule(ctl, tc.agent, tc.spec, tc.action, tc.params); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if found, _ := p.Schedules(ctl, ""); len(found) != 0 {
		t.Errorf("a refused schedule was kept: %+v", found)
	}
	// A server that has not joined yet may be given one: its first tasks wait for it.
	if _, err := p.AddSchedule(ctl, "new01", "@daily", agentapi.ActionDoctorRun, nil); err != nil {
		t.Errorf("a pending server: %v", err)
	}
}

func TestSchedulesAreLimitedPerServer(t *testing.T) {
	p := newPlant(t)
	for range MaxPerAgent {
		if _, err := p.AddSchedule(ctl, "web01", "@daily", agentapi.ActionDoctorRun, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.AddSchedule(ctl, "web01", "@daily", agentapi.ActionDoctorRun, nil); !errors.Is(err, ErrTooMany) {
		t.Errorf("one more: %v", err)
	}
	if _, err := p.AddSchedule(ctl, "db01", "@daily", agentapi.ActionDoctorRun, nil); err != nil {
		t.Errorf("another server: %v", err)
	}
}

func TestRemovingAScheduleStopsItAndKeepsTheTasksItQueued(t *testing.T) {
	p := newPlant(t)
	one, _ := p.AddSchedule(ctl, "web01", "* * * * *", agentapi.ActionDoctorRun, nil)
	p.now = p.now.Add(2 * time.Minute)
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveSchedule(ctl, one.ID); err != nil {
		t.Fatal(err)
	}
	p.now = p.now.Add(2 * time.Minute)
	if err := p.Tick(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	if queued, _ := p.tasks.List(ctl, "web01", 10); len(queued) != 1 {
		t.Errorf("%d tasks", len(queued))
	}
	if err := p.RemoveSchedule(ctl, one.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
	if !strings.Contains(p.auditActions(t), "schedule.delete") {
		t.Error("no schedule.delete in the audit log")
	}
}

func TestAWindowCoversItsServerOnlyWhileItIsOpen(t *testing.T) {
	p := newPlant(t)
	start, end := p.now.Add(time.Hour), p.now.Add(3*time.Hour)
	if _, err := p.AddWindow(ctl, "web01", start, end, "kernel\nupdate"); err != nil {
		t.Fatal(err)
	}
	cover := func(at time.Time, agent string) bool {
		active, err := p.ActiveAt(ctl, at)
		if err != nil {
			t.Fatal(err)
		}
		return active.Covers(agent)
	}
	if cover(p.now, "web01") || !cover(start, "web01") || !cover(end.Add(-time.Second), "web01") || cover(end, "web01") {
		t.Error("the window does not cover [start, end)")
	}
	if cover(start, "db01") {
		t.Error("a window of one server covered another")
	}
	if _, err := p.AddWindow(ctl, "", start, end, ""); err != nil {
		t.Fatal(err)
	}
	if !cover(start, "db01") {
		t.Error("a window for all did not cover db01")
	}
	windows, _ := p.Windows(ctl, p.now)
	if len(windows) != 2 || windows[0].Reason != "kernel update" {
		t.Errorf("%+v", windows)
	}
	if err := p.RemoveWindow(ctl, windows[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveWindow(ctl, windows[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
}

func TestAWindowIsCheckedWhenItIsMade(t *testing.T) {
	p := newPlant(t)
	for name, tc := range map[string]struct {
		agent      string
		start, end time.Time
		want       error
	}{
		"ends before it starts": {"web01", p.now.Add(time.Hour), p.now, ErrInvalid},
		"is over already":       {"web01", p.now.Add(-2 * time.Hour), p.now.Add(-time.Hour), ErrInvalid},
		"is too long":           {"web01", p.now, p.now.Add(MaxWindow + time.Hour), ErrInvalid},
		"names no server":       {"nobody", p.now, p.now.Add(time.Hour), ErrNoAgent},
		"names a revoked one":   {"old01", p.now, p.now.Add(time.Hour), ErrNoAgent},
	} {
		if _, err := p.AddWindow(ctl, tc.agent, tc.start, tc.end, ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if _, err := p.AddWindow(ctl, "web01", p.now.Add(MaxAhead+time.Hour), p.now.Add(MaxAhead+2*time.Hour), ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("a window that starts in years: %v", err)
	}
	long := strings.Repeat("é", 500)
	made, err := p.AddWindow(ctl, "web01", p.now, p.now.Add(time.Hour), long)
	if err != nil || len([]rune(made.Reason)) > MaxReason {
		t.Errorf("a long note: %d characters, %v", len([]rune(made.Reason)), err)
	}
}

func TestOldWindowsAreDeleted(t *testing.T) {
	p := newPlant(t)
	if _, err := p.st.DB.Exec(`INSERT INTO maintenance (agent, starts_at, ends_at, reason, created_at, created_by) VALUES (NULL, 1, 2, '', 1, 'ctl')`); err != nil {
		t.Fatal(err)
	}
	if err := p.pruneWindows(ctl, p.now); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := p.st.DB.QueryRow(`SELECT COUNT(*) FROM maintenance`).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d windows left, %v", left, err)
	}
}
