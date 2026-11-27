package main

import (
	"context"
	"strings"
	"testing"
)

func TestArmGuardStartsATimerThatRunsThePreviousBinaryWithFixedArguments(t *testing.T) {
	var name string
	var args []string
	old := runSystemTool
	t.Cleanup(func() { runSystemTool = old })
	runSystemTool = func(_ context.Context, program string, arguments ...string) (string, error) {
		name, args = program, arguments
		return "", nil
	}
	if err := armGuard(context.Background(), "/usr/local/bin/sentinel", "/etc/sentinel/config.yaml"); err != nil {
		t.Fatal(err)
	}
	got := name + " " + strings.Join(args, " ")
	want := "systemd-run --on-active=420 --unit=sentinel-self-update-guard --collect --quiet -- /usr/local/bin/sentinel.prev --config /etc/sentinel/config.yaml update-guard"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestTheHelperCommandsAreHiddenAndTakeNoArgument(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"update-install", "update-rollback", "update-guard"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name || !cmd.Hidden {
			t.Errorf("%s: %v hidden=%v", name, err, cmd != nil && cmd.Hidden)
			continue
		}
		if err := cmd.Args(cmd, []string{"/etc/passwd"}); err == nil {
			t.Errorf("%s took an argument", name)
		}
	}
}
