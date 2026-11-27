package task

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// queue is a task service over a database with some agents: web01 and db01 are active, new01 has not joined, old01 was revoked.
type queue struct {
	*Service
	st *store.Store
}

func newQueue(t *testing.T) queue {
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
	return queue{New(st), st}
}

var ctl = audit.WithActor(context.Background(), audit.Actor{Name: "ctl", Session: "abcd1234", IP: "127.0.0.1"})

func (q queue) create(t *testing.T, agent, action string, params map[string]string) Task {
	t.Helper()
	created, err := q.Create(ctl, agent, action, params)
	if err != nil {
		t.Fatalf("create %s on %s: %v", action, agent, err)
	}
	return created
}

func (q queue) auditActions(t *testing.T) string {
	t.Helper()
	records, err := audit.List(context.Background(), q.st.DB, 100)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for i := len(records) - 1; i >= 0; i-- {
		actions = append(actions, records[i].Entry.Action)
	}
	if _, err := audit.Verify(context.Background(), q.st.DB); err != nil {
		t.Fatalf("the audit chain is broken: %v", err)
	}
	return strings.Join(actions, " ")
}

func TestCreateQueuesATaskForAnActiveAgentOnly(t *testing.T) {
	q := newQueue(t)
	created := q.create(t, "web01", agentapi.ActionBackupStart, map[string]string{"job": "db"})
	if created.State != agentapi.TaskQueued || created.Agent != "web01" || created.Params["job"] != "db" || created.CreatedBy != "ctl:abcd1234" {
		t.Errorf("created = %+v", created)
	}
	if created.ExpiresAt.Sub(created.CreatedAt) != QueueTTL {
		t.Errorf("expires after %s", created.ExpiresAt.Sub(created.CreatedAt))
	}
	for name, agent := range map[string]string{"a server that has not joined": "new01", "a revoked one": "old01", "one that does not exist": "ghost"} {
		if _, err := q.Create(ctl, agent, agentapi.ActionDoctorRun, nil); err == nil || !strings.Contains(err.Error(), ErrAgentNotReady.Error()) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, params := range map[string]map[string]string{"no job": nil, "a path": {"job": "db", "path": "/etc"}, "a line break": {"job": "a\nb"}} {
		if _, err := q.Create(ctl, "web01", agentapi.ActionBackupStart, params); err == nil {
			t.Errorf("%s was queued", name)
		}
	}
	if _, err := q.Create(ctl, "web01", "service.restart", map[string]string{"unit": "x"}); err == nil {
		t.Error("an action that is not in the table was queued")
	}
	if got := q.auditActions(t); got != "task.create" {
		t.Errorf("audit = %q: only the queued task is written", got)
	}
}

func TestAnAgentIsLimitedToMaxOpenTasks(t *testing.T) {
	q := newQueue(t)
	for range MaxOpen {
		q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	}
	if _, err := q.Create(ctl, "web01", agentapi.ActionDoctorRun, nil); err == nil || !strings.Contains(err.Error(), ErrTooMany.Error()) {
		t.Errorf("err = %v", err)
	}
	q.create(t, "db01", agentapi.ActionDoctorRun, nil) // another server has its own room
}

func TestPollSendsWaitingTasksOnceAndInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQueue(t)
		first := q.create(t, "web01", agentapi.ActionBackupStart, map[string]string{"job": "a"})
		second := q.create(t, "web01", agentapi.ActionBackupStop, map[string]string{"job": "a"})
		q.create(t, "db01", agentapi.ActionDoctorRun, nil)
		sent, err := q.Poll(ctl, "web01", time.Second)
		if err != nil || len(sent) != 2 || sent[0].ID != first.ID || sent[1].ID != second.ID {
			t.Fatalf("sent = %+v, err %v", sent, err)
		}
		if want := time.Now().Add(agentapi.Timeout(agentapi.ActionBackupStart) + agentapi.ResultGrace); !sent[0].ResultBy.Equal(want.Truncate(time.Second)) && sent[0].ResultBy.Sub(want).Abs() > time.Second {
			t.Errorf("result_by = %s, want about %s", sent[0].ResultBy, want)
		}
		if again, err := q.Poll(ctl, "web01", time.Second); err != nil || len(again) != 0 {
			t.Errorf("the same tasks were sent twice: %+v, %v", again, err)
		}
		if got, _ := q.Get(ctl, first.ID); got.State != agentapi.TaskSent || got.SentAt == nil {
			t.Errorf("state = %s", got.State)
		}
		if got := q.auditActions(t); got != "task.create task.create task.create task.send task.send" {
			t.Errorf("audit = %q", got)
		}
	})
}

func TestPollWakesAsSoonAsATaskIsQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQueue(t)
		var sent []agentapi.Task
		var waited time.Duration
		var group sync.WaitGroup
		group.Go(func() {
			start := time.Now()
			sent, _ = q.Poll(ctl, "web01", 25*time.Second)
			waited = time.Since(start)
		})
		time.Sleep(10 * time.Second)
		q.create(t, "web01", agentapi.ActionDoctorRun, nil)
		group.Wait()
		if len(sent) != 1 || waited != 10*time.Second {
			t.Errorf("sent %d tasks after %s, want 1 after 10s", len(sent), waited)
		}
	})
}

func TestAPollThatHearsNothingEndsEmptyAtItsLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQueue(t)
		start := time.Now()
		sent, err := q.Poll(ctl, "web01", 25*time.Second)
		if err != nil || sent != nil || time.Since(start) != 25*time.Second {
			t.Errorf("sent %v, err %v after %s", sent, err, time.Since(start))
		}
	})
}

func TestANewPollOfTheSameAgentEndsTheOldOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQueue(t)
		var oldEnded atomic.Int64
		var group sync.WaitGroup
		start := time.Now()
		group.Go(func() {
			_, _ = q.Poll(ctl, "web01", time.Hour)
			oldEnded.Store(int64(time.Since(start)))
		})
		time.Sleep(5 * time.Second)
		var fresh []agentapi.Task
		group.Go(func() { fresh, _ = q.Poll(ctl, "web01", time.Hour) })
		time.Sleep(time.Second)
		if got := time.Duration(oldEnded.Load()); got != 5*time.Second {
			t.Errorf("the old poll ended after %s, want 5s", got)
		}
		q.create(t, "web01", agentapi.ActionDoctorRun, nil)
		group.Wait()
		if len(fresh) != 1 {
			t.Errorf("the new poll got %d tasks, not the one queued", len(fresh))
		}
	})
}

func TestATaskThatPassedItsTimeIsNeverSentEvenBeforeExpireRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newQueue(t)
		q.create(t, "web01", agentapi.ActionDoctorRun, nil)
		time.Sleep(QueueTTL + time.Second)
		if sent, err := q.Poll(ctl, "web01", time.Second); err != nil || len(sent) != 0 {
			t.Errorf("a task of %s ago was sent: %+v, %v", QueueTTL, sent, err)
		}
	})
}

func TestResultIsKeptCleanedAndOnlyOnceFromTheAgentItWasSentTo(t *testing.T) {
	q := newQueue(t)
	created := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	good := agentapi.TaskResult{State: agentapi.TaskSucceeded, Output: "ok\x1b[31m\nsecond line", StartedAt: time.Now()}
	if err := q.Result(ctl, "web01", created.ID, good); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("a result for a task that was never sent: %v", err)
	}
	if _, err := q.Poll(ctl, "web01", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := q.Result(ctl, "db01", created.ID, good); !errors.Is(err, ErrNotFound) {
		t.Errorf("another agent's result: %v", err)
	}
	if err := q.Result(ctl, "web01", created.ID, agentapi.TaskResult{State: agentapi.TaskCanceled}); err == nil {
		t.Error("an agent set the state canceled")
	}
	if err := q.Result(ctl, "web01", created.ID, good); err != nil {
		t.Fatal(err)
	}
	if err := q.Result(ctl, "web01", created.ID, good); !errors.Is(err, ErrNotRunning) {
		t.Errorf("a second result: %v", err)
	}
	got, _ := q.Get(ctl, created.ID)
	if got.State != agentapi.TaskSucceeded || got.Output != "ok\nsecond line" || got.FinishedAt == nil || got.StartedAt == nil {
		t.Errorf("task = %+v", got)
	}
	if got := q.auditActions(t); got != "task.create task.send task.result" {
		t.Errorf("audit = %q", got)
	}
}

func TestAnAgentCannotMakeItsStartTimeLieBeyondTheTask(t *testing.T) {
	q := newQueue(t)
	created := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	_, _ = q.Poll(ctl, "web01", time.Second)
	if err := q.Result(ctl, "web01", created.ID, agentapi.TaskResult{State: agentapi.TaskFailed, StartedAt: time.Unix(5, 0), Error: "x"}); err != nil {
		t.Fatal(err)
	}
	got, _ := q.Get(ctl, created.ID)
	if got.StartedAt.Before(*got.SentAt) || got.StartedAt.After(*got.FinishedAt) {
		t.Errorf("started %s, sent %s, finished %s", got.StartedAt, got.SentAt, got.FinishedAt)
	}
}

func TestCancelTakesBackOnlyATaskThatIsWaiting(t *testing.T) {
	q := newQueue(t)
	waiting := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	running := q.create(t, "db01", agentapi.ActionDoctorRun, nil)
	_, _ = q.Poll(ctl, "db01", time.Second)
	if err := q.Cancel(ctl, waiting.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(ctl, waiting.ID); !errors.Is(err, ErrNotQueued) {
		t.Errorf("canceled twice: %v", err)
	}
	if err := q.Cancel(ctl, running.ID); !errors.Is(err, ErrNotQueued) {
		t.Errorf("a task that was sent: %v", err)
	}
	if err := q.Cancel(ctl, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a task that does not exist: %v", err)
	}
	if sent, _ := q.Poll(ctl, "web01", time.Second); len(sent) != 0 {
		t.Error("a canceled task was sent")
	}
	got, _ := q.Get(ctl, waiting.ID)
	if got.State != agentapi.TaskCanceled || got.FinishedAt == nil {
		t.Errorf("task = %+v", got)
	}
}

func TestExpireEndsWhatWaitedTooLongAndWhatWasNeverAnswered(t *testing.T) {
	q := newQueue(t)
	waiting := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	lost := q.create(t, "db01", agentapi.ActionBackupStop, map[string]string{"job": "x"})
	_, _ = q.Poll(ctl, "db01", time.Second)
	fresh := q.create(t, "db01", agentapi.ActionDoctorRun, nil)

	if err := q.Expire(ctl, time.Now().Add(QueueTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Get(ctl, waiting.ID); got.State != agentapi.TaskQueued {
		t.Errorf("a task expired early: %s", got.State)
	}
	later := time.Now().Add(QueueTTL + time.Minute)
	if err := q.Expire(ctl, later); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{waiting.ID: "did not take", lost.ID: "no result", fresh.ID: "did not take"} {
		got, _ := q.Get(ctl, id)
		if got.State != agentapi.TaskExpired || !strings.Contains(got.Error, want) || got.FinishedAt == nil {
			t.Errorf("task %d = %s %q", id, got.State, got.Error)
		}
	}
	if err := q.Result(ctl, "db01", lost.ID, agentapi.TaskResult{State: agentapi.TaskSucceeded}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("a result that came after the task expired: %v", err)
	}
	if got := q.auditActions(t); !strings.HasSuffix(got, "task.expire task.expire task.expire") {
		t.Errorf("audit = %q", got)
	}
	if err := q.Expire(ctl, later); err != nil {
		t.Fatal(err)
	}
	if got := q.auditActions(t); strings.Count(got, "task.expire") != 3 {
		t.Errorf("a second Expire wrote again: %q", got)
	}
}

func TestLogLinesComeInOrderAndTheLogIsCapped(t *testing.T) {
	q := newQueue(t)
	created := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	if err := q.AppendLog(ctl, "web01", created.ID, "too early"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("a log for a task that was not sent: %v", err)
	}
	_, _ = q.Poll(ctl, "web01", time.Second)
	for _, text := range []string{"one", "two\x1b[2J", "  \n", "three"} {
		if err := q.AppendLog(ctl, "web01", created.ID, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.AppendLog(ctl, "db01", created.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another agent's log: %v", err)
	}
	lines, err := q.Log(ctl, created.ID, 0)
	if err != nil || len(lines) != 3 || lines[0].Text != "one" || lines[1].Text != "two" || lines[2].Seq != 3 {
		t.Fatalf("lines = %+v, err %v", lines, err)
	}
	if rest, _ := q.Log(ctl, created.ID, 2); len(rest) != 1 || rest[0].Text != "three" {
		t.Errorf("after 2 = %+v", rest)
	}
	chunk := strings.Repeat("x", agentapi.MaxTaskLogChunk)
	var full error
	for range agentapi.MaxTaskLog/agentapi.MaxTaskLogChunk + 1 {
		if full = q.AppendLog(ctl, "web01", created.ID, chunk); full != nil {
			break
		}
	}
	if !errors.Is(full, ErrLogFull) {
		t.Errorf("a log without end: %v", full)
	}
}

func TestPruneDeletesOldFinishedTasksWithTheirLogsAndKeepsTheRest(t *testing.T) {
	q := newQueue(t)
	old := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	_, _ = q.Poll(ctl, "web01", time.Second)
	_ = q.AppendLog(ctl, "web01", old.ID, "line")
	_ = q.Result(ctl, "web01", old.ID, agentapi.TaskResult{State: agentapi.TaskSucceeded})
	open := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	if err := q.Prune(ctl, time.Now().Add(Retention-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Get(ctl, old.ID); err != nil {
		t.Errorf("a task of less than the retention was deleted: %v", err)
	}
	if err := q.Prune(ctl, time.Now().Add(Retention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Get(ctl, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("an old task stayed: %v", err)
	}
	if lines, _ := q.Log(ctl, old.ID, 0); len(lines) != 0 {
		t.Error("the log of a deleted task stayed")
	}
	if _, err := q.Get(ctl, open.ID); err != nil {
		t.Errorf("a task that has not ended was deleted: %v", err)
	}
}

func TestListIsNewestFirstAndCanBeLimitedToOneServer(t *testing.T) {
	q := newQueue(t)
	a := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	b := q.create(t, "db01", agentapi.ActionDoctorRun, nil)
	c := q.create(t, "web01", agentapi.ActionDoctorRun, nil)
	all, _ := q.List(ctl, "", 10)
	web, _ := q.List(ctl, "web01", 10)
	one, _ := q.List(ctl, "", 1)
	if len(all) != 3 || all[0].ID != c.ID || all[2].ID != a.ID || len(web) != 2 || web[1].ID != a.ID || len(one) != 1 || one[0].ID != c.ID {
		t.Errorf("all %v, web %v, one %v (b = %d)", ids(all), ids(web), ids(one), b.ID)
	}
}

func ids(tasks []Task) []int64 {
	var found []int64
	for _, task := range tasks {
		found = append(found, task.ID)
	}
	return found
}
