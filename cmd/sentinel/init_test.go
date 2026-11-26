package main

import (
	"strings"
	"testing"

	"sentinel/internal/tui"
)

func TestPrintInitOutcomeTellsWhatToDoNext(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "")
	var output strings.Builder
	printInitOutcome(&output, &tui.InitOutcome{Path: "/home/me/.config/sentinel/config.yaml", PasswordEnv: "RESTIC_PASSWORD", JobName: "documents"})
	for _, want := range []string{"Đã ghi /home/me/.config/sentinel/config.yaml", "sẽ hỏi RESTIC_PASSWORD", "sentinel backup init documents", "sentinel doctor", "sentinel manage"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, output.String())
		}
	}
	output.Reset()
	printInitOutcome(&output, &tui.InitOutcome{Path: "/etc/sentinel/config.yaml", EnvFile: "/etc/sentinel/sentinel.env", PasswordEnv: "RESTIC_PASSWORD", SecretWritten: true, JobName: "documents"})
	if !strings.Contains(output.String(), "mật khẩu repository ngẫu nhiên") || !strings.Contains(output.String(), ". /etc/sentinel/sentinel.env") || strings.Contains(output.String(), "export RESTIC_PASSWORD") {
		t.Errorf("root output:\n%s", output.String())
	}
}
