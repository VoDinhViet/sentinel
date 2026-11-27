package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodToken = "read-token-0123456789"

// writeFleetFile puts content in a fleet file of the given mode in a new directory.
func writeFleetFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsTheServersInFileOrder(t *testing.T) {
	path := writeFleetFile(t, `
servers:
  - { name: web-01, url: "https://web-01.example.com:9311", token: `+goodToken+` }
  - { name: db-01, url: "http://10.0.0.5:9311", token: `+goodToken+` }
  - { name: edge, url: "https://edge.example.com", token: `+goodToken+`, tls_ca: /etc/sentinel/edge-ca.pem }
`, 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, server := range cfg.Servers {
		names = append(names, server.Name)
	}
	if got := strings.Join(names, ","); got != "web-01,db-01,edge" {
		t.Errorf("servers = %s, want web-01,db-01,edge", got)
	}
	if cfg.Servers[2].TLSCA != "/etc/sentinel/edge-ca.pem" {
		t.Errorf("tls_ca = %q", cfg.Servers[2].TLSCA)
	}
}

func TestLoadRefusesAFileThatIsWrong(t *testing.T) {
	tests := map[string]struct{ content, wantError string }{
		"empty file":      {"", "lists no servers"},
		"lists no servers":      {"servers: []", "lists no servers"},
		"unknown key":     {"servers: [{name: a, url: 'https://a', token: " + goodToken + ", colour: red}]", "colour"},
		"bad name":        {"servers: [{name: 'a b', url: 'https://a', token: " + goodToken + "}]", "name must"},
		"name is empty":   {"servers: [{url: 'https://a', token: " + goodToken + "}]", "name must"},
		"duplicate name":  {"servers: [{name: a, url: 'https://a', token: " + goodToken + "}, {name: a, url: 'https://b', token: " + goodToken + "}]", "used twice"},
		"no url":          {"servers: [{name: a, token: " + goodToken + "}]", "url must be an address"},
		"ftp url":         {"servers: [{name: a, url: 'ftp://a', token: " + goodToken + "}]", "http:// or https://"},
		"url with secret": {"servers: [{name: a, url: 'https://me:pw@a', token: " + goodToken + "}]", "must not carry a user"},
		"url with query":  {"servers: [{name: a, url: 'https://a?x=1', token: " + goodToken + "}]", "query"},
		"no token":        {"servers: [{name: a, url: 'https://a'}]", "token is empty"},
		"relative tls_ca": {"servers: [{name: a, url: 'https://a', token: " + goodToken + ", tls_ca: ca.pem}]", "absolute path"},
		"tls_ca on http":  {"servers: [{name: a, url: 'http://a', token: " + goodToken + ", tls_ca: /ca.pem}]", "https://"},
		"unset variable":  {"servers: [{name: a, url: 'https://a', token: '${FLEET_TEST_NEVER_SET}'}]", "FLEET_TEST_NEVER_SET"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeFleetFile(t, tc.content, 0o600))
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("error = %v, want one that mentions %q", err, tc.wantError)
			}
		})
	}
}

func TestLoadNamesTheFileThatDoesNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "fleet.yaml"))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %v, want one that says the file does not exist", err)
	}
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), EnvPath) {
		t.Errorf("error = %v, want one that names $%s", err, EnvPath)
	}
}

func TestATokenWrittenInTheFileNeedsAFileOthersCannotRead(t *testing.T) {
	plain := "servers: [{name: a, url: 'https://a', token: " + goodToken + "}]"
	t.Setenv("FLEET_TEST_TOKEN", goodToken)
	reference := "servers: [{name: a, url: 'https://a', token: '${FLEET_TEST_TOKEN}'}]"
	tests := map[string]struct {
		content string
		mode    os.FileMode
		wantErr bool
	}{
		"written token, private file":    {plain, 0o600, false},
		"written token, group readable":  {plain, 0o640, true},
		"written token, world readable":  {plain, 0o644, true},
		"${VAR} token, world readable":   {reference, 0o644, false},
		"${VAR} token, group readable":   {reference, 0o640, false},
		"${VAR} token, private file":     {reference, 0o600, false},
		"written token, only owner x/rw": {plain, 0o700, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeFleetFile(t, tc.content, tc.mode)
			if err := os.Chmod(path, tc.mode); err != nil { // WriteFile is bound by the umask
				t.Fatal(err)
			}
			_, err := Load(path)
			if (err != nil) != tc.wantErr {
				t.Errorf("error = %v, want error: %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), goodToken) {
				t.Errorf("the error shows the token: %v", err)
			}
		})
	}
}

func TestTokensComeFromTheEnvFileBesideTheFile(t *testing.T) {
	path := writeFleetFile(t, "servers: [{name: a, url: 'https://a', token: '${FLEET_TEST_FROM_FILE}'}]", 0o600)
	envFile := filepath.Join(filepath.Dir(path), EnvFileName)
	if err := os.WriteFile(envFile, []byte("FLEET_TEST_FROM_FILE="+goodToken+"\nPATH=/evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("FLEET_TEST_FROM_FILE") })
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Token != goodToken {
		t.Errorf("token = %q, want the one in the env file", cfg.Servers[0].Token)
	}
	if os.Getenv("PATH") == "/evil" {
		t.Error("the env file set a variable the fleet file does not refer to")
	}
}

func TestTheEnvironmentWinsOverTheEnvFile(t *testing.T) {
	path := writeFleetFile(t, "servers: [{name: a, url: 'https://a', token: '${FLEET_TEST_BOTH}'}]", 0o600)
	envFile := filepath.Join(filepath.Dir(path), EnvFileName)
	if err := os.WriteFile(envFile, []byte("FLEET_TEST_BOTH=from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_TEST_BOTH", goodToken)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Token != goodToken {
		t.Errorf("token = %q, want the one of the environment", cfg.Servers[0].Token)
	}
}

func TestAnEnvFileOthersCanWriteIsRefused(t *testing.T) {
	path := writeFleetFile(t, "servers: [{name: a, url: 'https://a', token: '${FLEET_TEST_OPEN}'}]", 0o600)
	envFile := filepath.Join(filepath.Dir(path), EnvFileName)
	if err := os.WriteFile(envFile, []byte("FLEET_TEST_OPEN="+goodToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envFile, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "written by other users") {
		t.Errorf("error = %v, want a refusal of the open env file", err)
	}
}

func TestDefaultPathPrefersTheEnvironment(t *testing.T) {
	t.Setenv(EnvPath, "/srv/fleet.yaml")
	if got := DefaultPath(); got != "/srv/fleet.yaml" {
		t.Errorf("path = %q", got)
	}
	t.Setenv(EnvPath, "")
	t.Setenv("XDG_CONFIG_HOME", "/home/me/.config")
	if got := DefaultPath(); got != "/home/me/.config/sentinel/fleet.yaml" {
		t.Errorf("path = %q", got)
	}
}
