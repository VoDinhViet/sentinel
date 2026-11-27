package report

import (
	"strings"
	"testing"
	"time"

	"sentinel/pkg/notify"
)

func TestTheDigestIsInfoWhenAllIsWellAndAWarningWhenNot(t *testing.T) {
	well := Digest(DigestInput{Servers: []ServerLine{{"web01", "ONLINE"}, {"db01", "ONLINE"}}, Jobs: []JobSummary{{Runs: 4}}}, base)
	if well.Level != notify.Info || !strings.Contains(well.Body, "Servers: 2, 2 ONLINE") || !strings.Contains(well.Body, "4 runs, 0 failed") || well.Title != "Daily report 2026-11-27" {
		t.Errorf("%+v", well)
	}
	bad := Digest(DigestInput{
		Servers: []ServerLine{{"web01", "ONLINE"}, {"db01", "OFFLINE"}, {"app01", "MAINTENANCE"}},
		Jobs:    []JobSummary{{Runs: 3, Failures: 1}},
		Failed:  []Run{{Agent: "web01", Job: "db", Repository: "s3", FinishedAt: base, Result: "FAILED: disk full"}},
		Alerts:  []string{"db01 is offline"},
	}, base)
	if bad.Level != notify.Warning {
		t.Errorf("level %v", bad.Level)
	}
	for _, want := range []string{"1 OFFLINE", "1 MAINTENANCE", "db01 OFFLINE", "app01 MAINTENANCE", "web01 db/s3 at 12:00: FAILED: disk full", "Open alerts:", "db01 is offline"} {
		if !strings.Contains(bad.Body, want) {
			t.Errorf("lacks %q:\n%s", want, bad.Body)
		}
	}
}

func TestALongListInTheDigestIsCut(t *testing.T) {
	var servers []ServerLine
	for range 40 {
		servers = append(servers, ServerLine{"s", "OFFLINE"})
	}
	if body := Digest(DigestInput{Servers: servers}, base).Body; !strings.Contains(body, "… and 25 more") {
		t.Errorf("%s", body)
	}
}

func TestTheDigestComesAtTheNextTimeOfDay(t *testing.T) {
	hour, minute, err := ParseDigestTime("08:30")
	if err != nil {
		t.Fatal(err)
	}
	morning := time.Date(2026, 11, 27, 7, 0, 0, 0, time.UTC)
	if got := NextDigest(morning, hour, minute); !got.Equal(time.Date(2026, 11, 27, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("before: %v", got)
	}
	if got := NextDigest(time.Date(2026, 11, 27, 8, 30, 0, 0, time.UTC), hour, minute); !got.Equal(time.Date(2026, 11, 28, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("at the minute itself: %v", got)
	}
	for _, bad := range []string{"", "8", "25:00", "08:60", "8:30pm", "tomorrow"} {
		if _, _, err := ParseDigestTime(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
