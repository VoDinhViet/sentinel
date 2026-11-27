package tui

import (
	"context"
	"testing"
	"time"

	"sentinel/agent/internal/status"
)

func reloadModel(report status.Report) manageModel {
	return newManageModel(context.Background(), func() (status.Report, error) { return report, nil }, manageActions{}, nil)
}

func TestTimedRefreshLoadsOnceAndSchedulesTheNextOnlyWhenTheReportArrives(t *testing.T) {
	screen := reloadModel(sampleReport())

	_, cmd := screen.update(refreshMsg{})
	msg, ok := cmd().(reportMsg)
	if !ok || !msg.periodic {
		t.Fatalf("a timed refresh must be a single load whose report starts the next one, got %#v", cmd())
	}

	_, next := screen.update(msg)
	if next == nil {
		t.Error("the report of a timed refresh must schedule the next refresh")
	}
}

func TestReportOfAnActionDoesNotStartAnotherRefreshChain(t *testing.T) {
	screen := reloadModel(sampleReport())
	_, cmd := screen.update(reportMsg{report: sampleReport(), asked: time.Now()})
	if cmd != nil {
		t.Error("a reload after an action must not add a second timed refresh")
	}
}

func TestOlderReportDoesNotReplaceANewerOne(t *testing.T) {
	newer := status.Report{Server: "newer"}
	older := status.Report{Server: "older"}
	asked := time.Now()
	model, _ := reloadModel(sampleReport()).update(reportMsg{report: newer, asked: asked})
	model, _ = model.(manageModel).update(reportMsg{report: older, asked: asked.Add(-time.Second)})
	if got := model.(manageModel).report.Server; got != "newer" {
		t.Errorf("report = %q, a load started earlier must not overwrite the newer one", got)
	}
}

func TestStaleTimedReportStillSchedulesTheNextRefresh(t *testing.T) {
	asked := time.Now()
	model, _ := reloadModel(sampleReport()).update(reportMsg{asked: asked})
	_, cmd := model.(manageModel).update(reportMsg{asked: asked.Add(-time.Second), periodic: true})
	if cmd == nil {
		t.Error("dropping a stale report must not end the refresh chain")
	}
}
