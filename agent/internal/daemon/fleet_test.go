package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentinel/agent/internal/alertkey"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/fleet"
	"sentinel/agent/internal/status"
	"sentinel/pkg/alert"
)

// fleetRounds scripts what the servers answer, one round at a time.
type fleetRounds struct {
	answers [][]fleet.ServerStatus
	err     error // the fleet file cannot be read
	round   int
}

func (f *fleetRounds) poll(context.Context, string) ([]fleet.ServerStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	answers := f.answers[min(f.round, len(f.answers)-1)]
	f.round++
	return answers, nil
}

func up(name string) fleet.ServerStatus {
	return fleet.ServerStatus{Server: fleet.Server{Name: name, URL: "https://" + name + ":9311"}, Report: status.Report{Version: "v1.2.0"}}
}

func down(name string) fleet.ServerStatus {
	answer := up(name)
	answer.Report, answer.Err = status.Report{}, errors.New("server "+name+": connection refused")
	return answer
}

func newFleetWatcher(rec *recorder, rounds *fleetRounds) *FleetWatcher {
	return &FleetWatcher{
		Alerts: &alert.Manager{Notifier: rec, Cooldown: time.Hour}, Path: "/etc/sentinel/fleet.yaml",
		FailAfter: 3, Every: time.Minute, Poll: rounds.poll,
	}
}

func TestFleetWatcherAlertsAfterFailAfterRoundsAndResolvesWhenTheServerAnswers(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	rounds := &fleetRounds{answers: [][]fleet.ServerStatus{
		{down("web-01")}, {down("web-01")}, // two failures: not yet
		{down("web-01")}, {down("web-01")}, // the third opens the alert, the fourth is inside the cooldown
		{up("web-01")}, {up("web-01")},
	}}
	watcher := newFleetWatcher(rec, rounds)

	for round, want := range [][]string{nil, nil, {"CRITICAL web-01 cannot be reached"}, nil, {"RESOLVED web-01 answers again"}, nil} {
		watcher.Check(ctx)
		if got := rec.titles(); !slices.Equal(got, want) {
			t.Errorf("round %d: messages = %v, want %v", round+1, got, want)
		}
	}
}

func TestFleetWatcherAlertTextSaysWhyAndWhere(t *testing.T) {
	rec := &recorder{}
	watcher := newFleetWatcher(rec, &fleetRounds{answers: [][]fleet.ServerStatus{{down("web-01")}}})
	for range 3 {
		watcher.Check(context.Background())
	}
	body := rec.msgs[0].Body
	for _, want := range []string{"connection refused", "3 rounds in a row", "https://web-01:9311", "1m0s"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q: %s", want, body)
		}
	}
}

func TestFleetWatcherAnAnswerBetweenFailuresStartsTheCountOver(t *testing.T) {
	rec := &recorder{}
	rounds := &fleetRounds{answers: [][]fleet.ServerStatus{{down("web-01")}, {down("web-01")}, {up("web-01")}, {down("web-01")}, {down("web-01")}}}
	watcher := newFleetWatcher(rec, rounds)
	for range 5 {
		watcher.Check(context.Background())
	}
	if got := rec.titles(); len(got) != 0 {
		t.Errorf("messages = %v, want none: the failures were never FailAfter in a row", got)
	}
}

func TestFleetWatcherKeepsServersApart(t *testing.T) {
	rec := &recorder{}
	rounds := &fleetRounds{answers: [][]fleet.ServerStatus{{down("web-01"), up("db-01")}}}
	watcher := newFleetWatcher(rec, rounds)
	for range 3 {
		watcher.Check(context.Background())
	}
	if got, want := rec.titles(), []string{"CRITICAL web-01 cannot be reached"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if watcher.Alerts.Firing(alertkey.Fleet("db-01")) || !watcher.Alerts.Firing(alertkey.Fleet("web-01")) {
		t.Error("the alert of one server must not touch the other")
	}
}

func TestFleetWatcherDoesNotRepeatWhatTheServersSayThemselves(t *testing.T) {
	rec := &recorder{}
	troubled := up("db-01")
	troubled.Report.Alerts = []status.Alert{{Key: "stale:shop", Message: "no backup for 3 days"}}
	troubled.Report.Jobs = []status.Job{{Job: "shop", Success: false}}
	troubled.Report.Checks = []status.Check{{Name: "site", OK: false}}
	watcher := newFleetWatcher(rec, &fleetRounds{answers: [][]fleet.ServerStatus{{troubled}}})
	for range 4 {
		watcher.Check(context.Background())
	}
	if got := rec.titles(); len(got) != 0 {
		t.Errorf("messages = %v: the server alerts about its own trouble through its own notifiers", got)
	}
}

func TestFleetWatcherClosesTheAlertOfAServerThatLeftTheFile(t *testing.T) {
	rec := &recorder{}
	rounds := &fleetRounds{answers: [][]fleet.ServerStatus{{down("web-01"), up("db-01")}, {down("web-01"), up("db-01")}, {down("web-01"), up("db-01")}, {up("db-01")}}}
	watcher := newFleetWatcher(rec, rounds)
	for range 3 {
		watcher.Check(context.Background())
	}
	rec.titles()
	watcher.Check(context.Background())
	if got, want := rec.titles(), []string{"RESOLVED Server no longer watched: web-01"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if _, kept := watcher.failures["web-01"]; kept {
		t.Error("the count of a server that left the file was kept")
	}
}

func TestFleetWatcherWatchesAServerAddedToTheFileAtOnce(t *testing.T) {
	rec := &recorder{}
	rounds := &fleetRounds{answers: [][]fleet.ServerStatus{{up("web-01")}, {up("web-01"), down("new-01")}, {up("web-01"), down("new-01")}, {up("web-01"), down("new-01")}}}
	watcher := newFleetWatcher(rec, rounds)
	for range 4 {
		watcher.Check(context.Background())
	}
	if got, want := rec.titles(), []string{"CRITICAL new-01 cannot be reached"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

func TestFleetWatcherSaysWhenItCannotReadTheFileAndLeavesTheServersAlertsAlone(t *testing.T) {
	rec := &recorder{}
	rounds := &fleetRounds{answers: [][]fleet.ServerStatus{{down("web-01")}}}
	watcher := newFleetWatcher(rec, rounds)
	for range 3 {
		watcher.Check(context.Background())
	}
	rec.titles()

	rounds.err = errors.New("fleet.yaml: servers[0]: token is empty")
	watcher.Check(context.Background())
	if got, want := rec.titles(), []string{"WARNING The fleet file cannot be read: no server is being watched"}; !slices.Equal(got, want) {
		t.Fatalf("messages = %v, want %v", got, want)
	}
	watcher.Check(context.Background())
	if got := rec.titles(); len(got) != 0 {
		t.Errorf("the same problem was announced again within the cooldown: %v", got)
	}
	if !watcher.Alerts.Firing(alertkey.Fleet("web-01")) {
		t.Error("the alert of web-01 was closed on the strength of a file nobody could read")
	}

	rounds.err = nil
	rounds.answers = [][]fleet.ServerStatus{{up("web-01")}}
	watcher.Check(context.Background())
	if got := rec.titles(); !slices.Contains(got, "RESOLVED The fleet file can be read again") || !slices.Contains(got, "RESOLVED web-01 answers again") {
		t.Errorf("messages = %v, want both alerts closed", got)
	}
}

func TestFleetWatcherCountsNothingWhileTheDaemonStops(t *testing.T) {
	rec := &recorder{}
	rounds := &fleetRounds{answers: [][]fleet.ServerStatus{{down("web-01")}}}
	watcher := newFleetWatcher(rec, rounds)
	ctx, stop := context.WithCancel(context.Background())
	stop()
	for range 5 {
		watcher.Check(ctx)
	}
	if got := rec.titles(); len(got) != 0 || len(watcher.failures) != 0 {
		t.Errorf("messages %v, counts %v: a stopping daemon failed every call by itself", got, watcher.failures)
	}
}

func TestFleetTaskExistsOnlyWhenTheConfigNamesAFleetFile(t *testing.T) {
	alerts := &alert.Manager{}
	if tasks := fleetTask(&config.Config{}, alerts); len(tasks) != 0 {
		t.Errorf("tasks = %+v, want none without a file", tasks)
	}
	cfg := &config.Config{Fleet: config.Fleet{Config: "/etc/sentinel/fleet.yaml", Every: 2 * time.Minute, FailAfter: 3}}
	tasks := fleetTask(cfg, alerts)
	if len(tasks) != 1 || tasks[0].Name != "fleet" || tasks[0].Every != 2*time.Minute || !tasks[0].AtStart {
		t.Errorf("tasks = %+v", tasks)
	}
}

// The first round runs at start, outside the schedule, so it can still be going when the first tick comes.
func TestFleetWatcherSkipsARoundWhileThePreviousOneRuns(t *testing.T) {
	rec := &recorder{}
	asked, release := make(chan struct{}), make(chan struct{})
	var polls atomic.Int32
	watcher := newFleetWatcher(rec, &fleetRounds{})
	watcher.Poll = func(context.Context, string) ([]fleet.ServerStatus, error) {
		polls.Add(1)
		asked <- struct{}{}
		<-release
		return []fleet.ServerStatus{down("web-01")}, nil
	}
	finished := make(chan struct{})
	go func() { watcher.Check(context.Background()); close(finished) }()
	<-asked // the first round is inside its poll

	watcher.Check(context.Background()) // returns at once, or this test hangs on <-release
	close(release)
	<-finished
	if polls.Load() != 1 || watcher.failures["web-01"] != 1 {
		t.Errorf("polls = %d, failures = %d, want one round that counted one failure", polls.Load(), watcher.failures["web-01"])
	}
}

func TestFleetWatcherAlertTextHoldsNoControlCharactersAndIsCut(t *testing.T) {
	rec := &recorder{}
	answer := down("web-01")
	answer.Err = errors.New("x509: certificate is valid for \x1b]0;pwned\x07" + strings.Repeat("a", 5000))
	watcher := newFleetWatcher(rec, &fleetRounds{answers: [][]fleet.ServerStatus{{answer}}})
	for range 3 {
		watcher.Check(context.Background())
	}
	body := rec.msgs[0].Body
	if strings.ContainsAny(body, "\x1b\x07") {
		t.Errorf("the alert holds control characters: %q", body)
	}
	if len(body) > 1000 {
		t.Errorf("the alert is %d bytes long: what a server says must be cut", len(body))
	}
}
