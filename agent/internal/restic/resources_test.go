package restic

import (
	"os/exec"
	"slices"
	"testing"
)

func TestCommandRunsResticPlainByDefault(t *testing.T) {
	name, argv := (&Client{Bin: "/usr/bin/restic"}).command([]string{"backup", "--json"})
	if name != "/usr/bin/restic" || !slices.Equal(argv, []string{"backup", "--json"}) {
		t.Errorf("command = %s %v", name, argv)
	}
}

func TestCommandLimitsUploadBeforeTheSubcommand(t *testing.T) {
	name, argv := (&Client{Bin: "restic", UploadLimitKiB: 2048}).command([]string{"backup"})
	if name != "restic" || !slices.Equal(argv, []string{"--limit-upload", "2048", "backup"}) {
		t.Errorf("command = %s %v", name, argv)
	}
}

func TestCommandLowPriorityWrapsRestic(t *testing.T) {
	nice, niceErr := exec.LookPath("nice")
	if niceErr != nil {
		t.Skip("nice is not installed")
	}
	name, argv := (&Client{Bin: "restic", LowPriority: true}).command([]string{"backup"})
	if name != nice || !slices.Contains(argv, "restic") || argv[len(argv)-1] != "backup" || argv[0] != "-n" || argv[1] != "10" {
		t.Errorf("command = %s %v", name, argv)
	}
	if _, err := exec.LookPath("ionice"); err == nil && !slices.Contains(argv, "ionice") && !slices.ContainsFunc(argv, func(arg string) bool { return filepathBase(arg) == "ionice" }) {
		t.Errorf("ionice missing from %v", argv)
	}
}

func filepathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
