package selfupdate

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"sentinel/pkg/release"
)

// Verdict is what the start of the daemon finds about an update.
type Verdict int

const (
	// NoTrial: nothing is going on, or what the note says does not concern the running binary.
	NoTrial Verdict = iota
	// OnTrial: the running binary was just installed; it has TrialWindow to report to the control plane.
	OnTrial
	// GoBack: the new version has started too often without getting through its trial.
	GoBack
)

// BeginTrial reads the note at the start of the daemon running the version running. A note that is not about
// this binary is dropped; one that is counts a start, and says go back at the MaxTrialStarts-th.
func BeginTrial(dir, running string, now time.Time) (Verdict, Marker, error) {
	marker, found, err := ReadMarker(dir)
	if err != nil {
		RemoveMarker(dir)
		return NoTrial, Marker{}, err
	}
	if !found {
		return NoTrial, Marker{}, nil
	}
	switch marker.State {
	case RolledBack:
		if sameRelease(running, marker.From) { // the old version runs again: the note has done its work
			slog.Warn("self-update: the update to a new version was rolled back", "to", marker.To, "reason", marker.Reason)
			RemoveMarker(dir)
		}
		return NoTrial, marker, nil
	case Staged:
		if now.Sub(marker.At) > staleStaged {
			RemoveMarker(dir)
		}
		return NoTrial, marker, nil
	case Installed:
		if !sameRelease(running, marker.To) { // somebody put another binary there: it is nobody's trial
			RemoveMarker(dir)
			return NoTrial, marker, nil
		}
		marker.Starts++
		if err := WriteMarker(dir, marker); err != nil {
			return NoTrial, marker, fmt.Errorf("count the start of the new version: %w", err)
		}
		if marker.Starts > MaxTrialStarts {
			return GoBack, marker, nil
		}
		return OnTrial, marker, nil
	}
	RemoveMarker(dir)
	return NoTrial, marker, nil
}

// Confirm ends the trial: the new version reported to the control plane.
func Confirm(dir string) {
	if marker, found, _ := ReadMarker(dir); found && marker.State == Installed {
		slog.Info("self-update: the new version reported to the control plane; it stays", "version", marker.To, "was", marker.From)
		_ = os.Remove(filepath.Join(dir, MarkerName))
	}
}

// sameRelease says two version texts name the same major.minor.patch.
func sameRelease(a, b string) bool {
	left, err1 := release.ParseVersion(a)
	right, err2 := release.ParseVersion(b)
	return err1 == nil && err2 == nil && left == right
}

// OnTrialNow says an update was put in place and has not yet reported to the control plane.
func OnTrialNow(dir string) bool {
	marker, found, _ := ReadMarker(dir)
	return found && marker.State == Installed
}

// WhyGoBack is the reason noted when a new version that did not get through its trial is taken off.
func WhyGoBack(dir string) string {
	marker, _, _ := ReadMarker(dir)
	if marker.Starts > MaxTrialStarts {
		return fmt.Sprintf("the new version was started %d times without reporting to the control plane", marker.Starts-1)
	}
	return fmt.Sprintf("the new version did not report to the control plane within %s", TrialWindow)
}
