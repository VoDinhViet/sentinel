package host

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// sentinelUnit is the unit `deploy/install.sh` installs.
const sentinelUnit = "sentinel.service"

// systemctlTimeout bounds each systemctl call: a hung bus must not freeze the screen.
const systemctlTimeout = 3 * time.Second

// Unit is a systemd unit and what it is doing.
type Unit struct {
	Name        string
	Active      string // "active", "inactive", "failed", ...
	Description string
}

// readUnits asks systemd for the state of sentinel.service and for the units that failed. systemd
// is false when this machine has no systemctl (a container, a laptop): that is not an error.
func readUnits(ctx context.Context) (systemd bool, sentinel Unit, failed []Unit, err error) {
	systemctl, lookErr := exec.LookPath("systemctl")
	if lookErr != nil {
		return false, Unit{}, nil, nil
	}
	sentinel = Unit{Name: sentinelUnit, Active: unitActive(ctx, systemctl, sentinelUnit)}
	if sentinel.Active == "" { // systemctl does not run here: no systemd as init
		return false, Unit{}, nil, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, systemctlTimeout)
	defer cancel()
	output, runErr := exec.CommandContext(callCtx, systemctl, "list-units", "--failed", "--plain", "--no-legend", "--no-pager").Output()
	if runErr != nil {
		return true, sentinel, nil, fmt.Errorf("listing failed units: %w", runErr)
	}
	return true, sentinel, parseFailedUnits(string(output)), nil
}

// unitActive is what `systemctl is-active` says about a unit. It exits non-zero for a unit that is
// not active but still prints the state, so only an empty answer means systemctl did not work.
func unitActive(ctx context.Context, systemctl, name string) string {
	callCtx, cancel := context.WithTimeout(ctx, systemctlTimeout)
	defer cancel()
	output, err := exec.CommandContext(callCtx, systemctl, "is-active", name).Output()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// parseFailedUnits reads `systemctl list-units --failed --plain --no-legend`: one unit per line,
// "name load active sub description...". A leading "●" marks the failed ones on some versions.
func parseFailedUnits(text string) []Unit {
	var failed []Unit
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(line), "●* "))
		if len(fields) < 4 {
			continue
		}
		failed = append(failed, Unit{Name: fields[0], Active: fields[2], Description: strings.Join(fields[4:], " ")})
	}
	return failed
}
