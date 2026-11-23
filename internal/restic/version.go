package restic

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var versionRe = regexp.MustCompile(`restic (\d+)\.(\d+)\.(\d+)`)

// Version returns restic's version as [major, minor, patch].
func Version(ctx context.Context, bin string) ([3]int, error) {
	var ver [3]int
	if bin == "" {
		bin = "restic"
	}
	stdout, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return ver, err
	}
	match := versionRe.FindStringSubmatch(string(stdout))
	if match == nil {
		return ver, fmt.Errorf("cannot parse restic version from %q", strings.TrimSpace(string(stdout)))
	}
	for i := range ver {
		ver[i], _ = strconv.Atoi(match[i+1])
	}
	return ver, nil
}
