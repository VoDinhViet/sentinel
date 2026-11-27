package watchdog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/alert"
	"sentinel/pkg/notify"
	"sentinel/pkg/pki"
	"sentinel/server/internal/enroll"
	"sentinel/server/internal/inventory"
	"sentinel/server/internal/scheduler"
	"sentinel/server/internal/store"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// sent records what the notifier was asked to send.
type sent struct {
	mu       sync.Mutex
	messages []notify.Message
}

func (s *sent) Deliver(_ context.Context, msg notify.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

func (s *sent) titles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var titles []string
	for _, msg := range s.messages {
		titles = append(titles, msg.Title)
	}
	return titles
}

type plant struct {
	watchdog  *Watchdog
	agents    *enroll.Service
	inventory *inventory.Service
	store     *store.Store
	notified  *sent
}

func newPlant(t *testing.T) plant {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	notified := &sent{}
	manager := &alert.Manager{Notifier: notified, Store: AlertStore{DB: st.DB}, Server: "cp", Cooldown: time.Hour}
	agents := enroll.New(st, ca)
	return plant{
		watchdog:  &Watchdog{Alerts: manager, Agents: agents, Inventory: inventory.New(st), OfflineAfter: 3 * time.Minute, Started: time.Now()},
		agents:    agents,
		inventory: inventory.New(st),
		store:     st,
		notified:  notified,
	}
}

// join makes the agent called name and joins it.
func (p plant) join(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	token, _, err := p.agents.Create(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	_, request, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.agents.Join(ctx, token, request); err != nil {
		t.Fatal(err)
	}
}

// joinAndReport joins the agent and has it send its first report, as a daemon that is set up does.
func (p plant) joinAndReport(t *testing.T, name string) {
	t.Helper()
	p.join(t, name)
	if err := p.inventory.SaveReport(context.Background(), name, agentapi.Report{Version: "1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAnAgentThatGoesQuietIsReportedOnceAndThenWelcomedBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlant(t)
		ctx := context.Background()
		p.joinAndReport(t, "web01")
		p.watchdog.Started = time.Now().Add(-time.Hour) // not in its first minutes

		time.Sleep(time.Minute)
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 0 {
			t.Fatalf("an agent silent for a minute: %v", titles)
		}
		time.Sleep(150 * time.Second)
		p.watchdog.Check(ctx)
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 1 || titles[0] != "web01 is offline" {
			t.Fatalf("after 3m30s of silence: %v", titles)
		}
		if err := p.inventory.Seen(ctx, "web01", time.Now()); err != nil {
			t.Fatal(err)
		}
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 2 || titles[1] != "web01 is back" {
			t.Fatalf("after it reported: %v", titles)
		}
	})
}

func TestNoAgentIsCalledOfflineInTheFirstMinutesAfterTheControlPlaneStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlant(t)
		ctx := context.Background()
		p.joinAndReport(t, "web01")
		p.watchdog.Started = time.Now()
		time.Sleep(time.Hour) // the agent was silent all that while, but so was the control plane
		p.watchdog.Started = time.Now()
		time.Sleep(time.Minute)
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 0 {
			t.Fatalf("inside the grace period: %v", titles)
		}
		time.Sleep(150 * time.Second)
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 1 {
			t.Fatalf("after the grace period: %v", titles)
		}
	})
}

func TestACertificateNearItsEndWarnsUntilItIsRenewed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlant(t)
		ctx := context.Background()
		p.join(t, "web01")
		if err := p.inventory.Seen(ctx, "web01", time.Now()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(80 * 24 * time.Hour) // 10 of its 90 days left
		if err := p.inventory.Seen(ctx, "web01", time.Now()); err != nil {
			t.Fatal(err)
		}
		p.watchdog.Check(ctx)
		titles := p.notified.titles()
		if len(titles) != 1 || !strings.Contains(titles[0], "certificate is about to end") {
			t.Fatalf("with 10 days left: %v", titles)
		}
		_, request, err := pki.NewKeyAndCSR()
		if err != nil {
			t.Fatal(err)
		}
		renewed, err := p.agents.Renew(ctx, "web01", time.Now().Add(10*24*time.Hour), request)
		if err != nil {
			t.Fatal(err)
		}
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 1 {
			t.Fatalf("a certificate that was issued but is not in use yet must not hide the warning: %v", titles)
		}
		renewedCert, _ := pki.ParseCert(renewed.CertPEM)
		if err := p.agents.Check(ctx, pki.SerialOf(renewedCert), "web01"); err != nil { // the agent shows its new certificate
			t.Fatal(err)
		}
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 2 || !strings.Contains(titles[1], "renewed") {
			t.Fatalf("after the renewal: %v", titles)
		}
	})
}

func TestRevokingAnAgentClosesItsAlerts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlant(t)
		ctx := context.Background()
		p.joinAndReport(t, "web01")
		p.watchdog.Started = time.Now().Add(-time.Hour)
		time.Sleep(5 * time.Minute)
		p.watchdog.Check(ctx)
		if !p.watchdog.Alerts.Firing(OfflineKey("web01")) {
			t.Fatal("no offline alert to close")
		}
		if err := p.agents.Revoke(ctx, "web01"); err != nil {
			t.Fatal(err)
		}
		p.watchdog.Check(ctx)
		if p.watchdog.Alerts.Firing(OfflineKey("web01")) {
			t.Error("the alert of a revoked agent is still open")
		}
	})
}

func TestAnAgentThatJoinedAndHasNotReportedYetGetsHalfAnHourBeforeItIsCalledOffline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlant(t)
		ctx := context.Background()
		p.join(t, "web01")
		p.watchdog.Started = time.Now().Add(-time.Hour)
		if err := p.inventory.Seen(ctx, "web01", time.Now()); err != nil { // the whoami of `agent join`
			t.Fatal(err)
		}
		time.Sleep(10 * time.Minute) // the operator is still editing the config
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 0 {
			t.Fatalf("10 minutes after joining: %v", titles)
		}
		time.Sleep(25 * time.Minute)
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 1 || titles[0] != "web01 is offline" {
			t.Fatalf("35 minutes after joining, still no report: %v", titles)
		}
	})
}

// windows is a scheduler over the same database, with windows made at the time the test says.
func (p plant) windows() *scheduler.Service {
	return scheduler.New(p.store, nil)
}

func TestAnAgentInAMaintenanceWindowIsNotCalledOffline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlant(t)
		ctx := context.Background()
		p.joinAndReport(t, "web01")
		p.joinAndReport(t, "db01")
		p.watchdog.Started = time.Now().Add(-time.Hour)
		timetable := p.windows()
		p.watchdog.Maintenance = timetable
		if _, err := timetable.AddWindow(ctx, "web01", time.Now(), time.Now().Add(time.Hour), "kernel"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Minute) // both are silent now
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); len(titles) != 1 || titles[0] != "db01 is offline" {
			t.Fatalf("one server in a window, one not: %v", titles)
		}
		time.Sleep(time.Hour) // the window is over; web01 is still silent
		p.watchdog.Check(ctx)
		if titles := p.notified.titles(); !slices.Contains(titles, "web01 is offline") { // db01 is said again: the cooldown is an hour
			t.Fatalf("after the window: %v", titles)
		}
	})
}

func TestAWindowForAllMutesEveryAgentButNotAnAlertThatIsAlreadyOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlant(t)
		ctx := context.Background()
		p.joinAndReport(t, "web01")
		p.joinAndReport(t, "db01")
		p.watchdog.Started = time.Now().Add(-time.Hour)
		timetable := p.windows()
		p.watchdog.Maintenance = timetable
		time.Sleep(10 * time.Minute)
		p.watchdog.Check(ctx) // both offline: two alerts
		if titles := p.notified.titles(); len(titles) != 2 {
			t.Fatalf("%v", titles)
		}
		if _, err := timetable.AddWindow(ctx, "", time.Now(), time.Now().Add(time.Hour), ""); err != nil {
			t.Fatal(err)
		}
		if err := p.inventory.Seen(ctx, "web01", time.Now()); err != nil {
			t.Fatal(err)
		}
		p.watchdog.Check(ctx) // web01 came back inside the window: that is still said
		if titles := p.notified.titles(); len(titles) != 3 || titles[2] != "web01 is back" {
			t.Fatalf("%v", titles)
		}
	})
}
