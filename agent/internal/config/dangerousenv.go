package config

import "strings"

// dangerousEnvNames change which program or shell startup file a process runs.
var dangerousEnvNames = map[string]bool{
	"PATH": true, "IFS": true, "ENV": true, "BASH_ENV": true, "SHELLOPTS": true, "BASHOPTS": true,
	"PS4": true, "CDPATH": true, "GCONV_PATH": true, "HOSTALIASES": true, "LOCPATH": true, "NLSPATH": true,
}

// IsDangerousEnvName reports whether a variable name changes which program or library a process runs
// (PATH, LD_PRELOAD, BASH_ENV …) or makes a tool run a command to get a value (RESTIC_PASSWORD_COMMAND).
// restic and the dump tools run as root with whatever environment Sentinel gives them, so neither the
// config nor the env file may set such a variable for them.
func IsDangerousEnvName(name string) bool {
	upper := strings.ToUpper(name)
	return dangerousEnvNames[upper] ||
		strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") ||
		strings.HasSuffix(upper, "_COMMAND")
}
