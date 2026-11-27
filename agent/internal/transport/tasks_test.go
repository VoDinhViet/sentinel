package transport

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"sentinel/pkg/agentapi"
)

// fakeTasks is a control plane that hands out tasks and records what the worker sends back.
type fakeTasks struct {
	mu        sync.Mutex
	waiting   [][]agentapi.Task // what each poll answers; a poll with nothing left holds for PollWait
	pollErrs  []error
	resultErr []error // the answer to each SendResult, in turn (nil when they run out)
	polls     int
	results   map[int64]agentapi.TaskResult
	attempts  map[int64]int
	logs      map[int64][]string
	reloads   int
}

func newFakeTasks(batches ...[]agentapi.Task) *fakeTasks {
	return &fakeTasks{waiting: batches, results: map[int64]agentapi.TaskResult{}, attempts: map[int64]int{}, logs: map[int64][]string{}}
}

func (f *fakeTasks) Reload() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	return nil
}

func (f *fakeTasks) Poll(ctx context.Context) ([]agentapi.Task, error) {
	f.mu.Lock()
	f.polls++
	if len(f.pollErrs) > 0 {
		err := f.pollErrs[0]
		f.pollErrs = f.pollErrs[1:]
		f.mu.Unlock()
		return nil, err
	}
	if len(f.waiting) > 0 {
		batch := f.waiting[0]
		f.waiting = f.waiting[1:]
		f.mu.Unlock()
		return batch, nil
	}
	f.mu.Unlock()
	select { // nothing to give: hold, as the control plane does
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(PollWait):
		return nil, nil
	}
}

func (f *fakeTasks) SendResult(_ context.Context, id int64, result agentapi.TaskResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[id]++
	if len(f.resultErr) > 0 {
		err := f.resultErr[0]
		f.resultErr = f.resultErr[1:]
		if err != nil {
			return err
		}
	}
	f.results[id] = result
	return nil
}

func (f *fakeTasks) SendLog(_ context.Context, id int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs[id] = append(f.logs[id], text)
	return nil
}

func (f *fakeTasks) result(id int64) (agentapi.TaskResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.results[id]
	return result, ok
}

// runnerFunc is a Runner from a function.
type runnerFunc func(ctx context.Context, task agentapi.Task, log func(string)) agentapi.TaskResult

func (r runnerFunc) Run(ctx context.Context, task agentapi.Task, log func(string)) agentapi.TaskResult {
	return r(ctx, task, log)
}

var done = runnerFunc(func(_ context.Context, task agentapi.Task, log func(string)) agentapi.TaskResult {
	log("working on " + task.Action)
	return agentapi.TaskResult{State: agentapi.TaskSucceeded, Output: "did " + task.Action}
})

func statusOf(code int) error { return &statusError{status: code, message: "x"} }

func TestTheWorkerRunsEveryTaskInOrderAndSendsTheLogAndTheResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks([]agentapi.Task{{ID: 1, Action: "doctor.run"}, {ID: 2, Action: "backup.stop"}})
		var order []int64
		runner := runnerFunc(func(ctx context.Context, task agentapi.Task, log func(string)) agentapi.TaskResult {
			order = append(order, task.ID)
			return done(ctx, task, log)
		})
		ctx, cancel := context.WithCancel(context.Background())
		worker := &Worker{Client: control, Runner: runner}
		finished := make(chan struct{})
		go func() { worker.Loop(ctx); close(finished) }()
		synctest.Wait()
		cancel()
		<-finished
		first, ok1 := control.result(1)
		second, ok2 := control.result(2)
		if !ok1 || !ok2 || first.Output != "did doctor.run" || second.Output != "did backup.stop" {
			t.Fatalf("results = %+v %+v", control.results, control.attempts)
		}
		if len(order) != 2 || order[0] != 1 || order[1] != 2 {
			t.Errorf("order = %v", order)
		}
		if len(control.logs[1]) != 1 || control.logs[1][0] != "working on doctor.run" {
			t.Errorf("log = %v", control.logs)
		}
	})
}

func TestAResultThatCannotBeSentIsTriedAgainUntilItGoesThrough(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks([]agentapi.Task{{ID: 1, Action: "doctor.run", ResultBy: time.Now().Add(10 * time.Minute)}})
		control.resultErr = []error{errors.New("connection refused"), statusOf(502), nil}
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: done}).Loop(ctx); close(finished) }()
		time.Sleep(time.Minute)
		synctest.Wait()
		cancel()
		<-finished
		if _, ok := control.result(1); !ok || control.attempts[1] != 3 {
			t.Errorf("sent after %d attempts, results %v", control.attempts[1], control.results)
		}
	})
}

func TestAResultTheControlPlaneNoLongerWaitsForIsNotSentAgain(t *testing.T) {
	for _, code := range []int{404, 409} {
		synctest.Test(t, func(t *testing.T) {
			control := newFakeTasks([]agentapi.Task{{ID: 1, Action: "doctor.run"}})
			control.resultErr = []error{statusOf(code)}
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan struct{})
			go func() { (&Worker{Client: control, Runner: done}).Loop(ctx); close(finished) }()
			time.Sleep(time.Minute)
			synctest.Wait()
			cancel()
			<-finished
			if control.attempts[1] != 1 {
				t.Errorf("status %d: %d attempts, want 1", code, control.attempts[1])
			}
		})
	}
}

func TestAResultIsGivenUpWhenTheControlPlaneStoppedWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks([]agentapi.Task{{ID: 1, Action: "doctor.run", ResultBy: time.Now().Add(time.Minute)}})
		for range 100 {
			control.resultErr = append(control.resultErr, errors.New("down"))
		}
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: done}).Loop(ctx); close(finished) }()
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		cancel()
		<-finished
		if _, ok := control.result(1); ok {
			t.Error("a result was delivered that the control plane had given up on")
		}
		if control.attempts[1] < 2 || control.attempts[1] > 10 {
			t.Errorf("%d attempts: it must try a few times and then stop", control.attempts[1])
		}
	})
}

func TestAPollThatFailsIsTriedAgainWithALongerPauseEachTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks()
		control.pollErrs = []error{errors.New("down"), errors.New("down"), errors.New("down"), errors.New("down")}
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: done}).Loop(ctx); close(finished) }()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		control.mu.Lock()
		polls := control.polls
		control.mu.Unlock()
		cancel()
		<-finished
		// 5s, 10s, 20s, 40s: four failures take about 75 s, so after two minutes it is polling normally.
		if polls < 5 || polls > 8 {
			t.Errorf("%d polls in two minutes", polls)
		}
	})
}

func TestAWorkerThatIsRefusedWaitsAnHourAndReadsTheIdentityAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks()
		control.pollErrs = []error{statusOf(401)}
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: done}).Loop(ctx); close(finished) }()
		time.Sleep(30 * time.Minute)
		synctest.Wait()
		control.mu.Lock()
		before := control.polls
		control.mu.Unlock()
		time.Sleep(40 * time.Minute)
		synctest.Wait()
		control.mu.Lock()
		after, reloads := control.polls, control.reloads
		control.mu.Unlock()
		cancel()
		<-finished
		if before != 1 || after < 2 || reloads != 1 {
			t.Errorf("polls %d after 30 min, %d after 70 min; identity read %d times", before, after, reloads)
		}
	})
}

func TestAPollThatAnswersAtOnceDoesNotMakeABusyLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		batches := make([][]agentapi.Task, 1000)
		control := newFakeTasks(batches...) // each poll answers nothing, immediately
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: done}).Loop(ctx); close(finished) }()
		time.Sleep(time.Minute)
		synctest.Wait()
		control.mu.Lock()
		polls := control.polls
		control.mu.Unlock()
		cancel()
		<-finished
		if polls > 40 {
			t.Errorf("%d polls in a minute", polls)
		}
	})
}

func TestATaskThatIsRunningWhenTheDaemonStopsStillGetsItsResultSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks([]agentapi.Task{{ID: 1, Action: "doctor.run"}})
		runner := runnerFunc(func(ctx context.Context, _ agentapi.Task, _ func(string)) agentapi.TaskResult {
			<-ctx.Done()
			return agentapi.TaskResult{State: agentapi.TaskFailed, Error: "interrupted: the daemon stopped"}
		})
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: runner}).Loop(ctx); close(finished) }()
		synctest.Wait()
		cancel()
		<-finished
		if result, ok := control.result(1); !ok || result.State != agentapi.TaskFailed {
			t.Errorf("result = %+v, sent %v", result, ok)
		}
	})
}

func TestTheLogOfATaskGoesInBatchesNotLineByLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks([]agentapi.Task{{ID: 1, Action: "doctor.run"}})
		chatty := runnerFunc(func(_ context.Context, _ agentapi.Task, log func(string)) agentapi.TaskResult {
			for i := range 100 {
				log("line " + strconv.Itoa(i))
			}
			time.Sleep(2500 * time.Millisecond) // two ticks pass while the task works
			log("last line")
			return agentapi.TaskResult{State: agentapi.TaskSucceeded}
		})
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: chatty}).Loop(ctx); close(finished) }()
		synctest.Wait()
		cancel()
		<-finished
		if _, ok := control.result(1); !ok {
			t.Fatal("no result")
		}
		if calls := len(control.logs[1]); calls < 2 || calls > 4 {
			t.Errorf("101 lines took %d calls, want a few", calls)
		}
		lines := strings.Split(strings.Join(control.logs[1], "\n"), "\n")
		if len(lines) != 101 || lines[0] != "line 0" || lines[100] != "last line" {
			t.Errorf("the lines were not all sent in order: %d, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
		}
	})
}

func TestATaskThatCameAfterItsDeadlineIsNotRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		late := agentapi.Task{ID: 1, Action: agentapi.ActionBackupStop, ResultBy: time.Now().Add(-time.Second)}
		ontime := agentapi.Task{ID: 2, Action: agentapi.ActionBackupStop, ResultBy: time.Now().Add(time.Minute)}
		control := newFakeTasks([]agentapi.Task{late}, []agentapi.Task{ontime})
		var ran []int64
		runner := runnerFunc(func(ctx context.Context, task agentapi.Task, log func(string)) agentapi.TaskResult {
			ran = append(ran, task.ID)
			return done(ctx, task, log)
		})
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { (&Worker{Client: control, Runner: runner}).Loop(ctx); close(finished) }()
		synctest.Wait()
		cancel()
		<-finished
		if len(ran) != 1 || ran[0] != 2 {
			t.Errorf("ran %v, want only task 2", ran)
		}
		if _, sent := control.result(1); sent {
			t.Error("a result was sent for a task the control plane had already given up on")
		}
	})
}

func TestAfterResultIsCalledOnlyAfterTheResultWasSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := newFakeTasks([]agentapi.Task{{ID: 1, Action: "agent.update"}})
		var sentWhenCalled bool
		var called []int64
		worker := &Worker{Client: control, Runner: done}
		worker.AfterResult = func(task agentapi.Task, result agentapi.TaskResult) {
			_, sentWhenCalled = control.result(task.ID)
			called = append(called, task.ID)
		}
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() { worker.Loop(ctx); close(finished) }()
		synctest.Wait()
		cancel()
		<-finished
		if len(called) != 1 || called[0] != 1 || !sentWhenCalled {
			t.Errorf("called = %v, result already sent = %v", called, sentWhenCalled)
		}
	})
}
