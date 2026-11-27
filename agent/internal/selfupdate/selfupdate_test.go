package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentinel/pkg/release"
)

var binary = []byte("#!/bin/sh\necho sentinel version 1.2.0\n")

// shelf is a release server: the manifest of 1.2.0 for linux/amd64, signed by key, and the binary it names.
type shelf struct {
	*httptest.Server
	public  ed25519.PublicKey
	private ed25519.PrivateKey
	body    []byte // what is served as the binary
	hits    atomic.Int32
}

func newShelf(t *testing.T) *shelf {
	t.Helper()
	public, private, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s := &shelf{public: public, private: private, body: binary}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.2.0/manifest-linux-amd64.json", func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		_, _ = w.Write(s.manifest(t, private, release.Manifest{Version: "1.2.0", OS: "linux", Arch: "amd64", SHA256: sum(binary), Size: int64(len(binary)), URL: s.URL + "/files/sentinel_linux_amd64"}))
	})
	mux.HandleFunc("/files/sentinel_linux_amd64", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(s.body) })
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/files/sentinel_linux_amd64", http.StatusFound)
	})
	s.Server = httptest.NewTLSServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *shelf) manifest(t *testing.T, key ed25519.PrivateKey, m release.Manifest) []byte {
	t.Helper()
	signed, err := release.Sign(key, m)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func sum(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (s *shelf) stager(t *testing.T, current string) *Stager {
	t.Helper()
	return &Stager{
		PublicKey: release.EncodeKey(s.public),
		URLFor: func(version, goos, goarch string) string {
			return s.URL + "/" + version + "/manifest-" + goos + "-" + goarch + ".json"
		},
		Current: current, GOOS: "linux", GOARCH: "amd64", Dir: filepath.Join(t.TempDir(), "update"),
		HTTP: &http.Client{Transport: s.Client().Transport, CheckRedirect: noRedirect}, // the server's certificate, the policy of Client
		Check: func(_ context.Context, path string) (string, error) {
			data, err := os.ReadFile(path)
			return "sentinel version " + strings.TrimSpace(string(data[strings.LastIndex(string(data), "version ")+8:])), err
		},
	}
}

func nothing(string) {}

func TestAReleaseThatIsSignedAndWholeIsStagedWithANote(t *testing.T) {
	s := newShelf(t)
	stager := s.stager(t, "v1.1.0")
	var said []string
	marker, err := stager.Stage(t.Context(), "1.2.0", func(line string) { said = append(said, line) })
	if err != nil {
		t.Fatal(err)
	}
	if marker.State != Staged || marker.From != "v1.1.0" || marker.To != "1.2.0" || marker.SHA256 != sum(binary) {
		t.Errorf("%+v", marker)
	}
	data, err := os.ReadFile(filepath.Join(stager.Dir, StagedName))
	if err != nil || string(data) != string(binary) {
		t.Errorf("the staged file: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(stager.Dir, StagedName)); info.Mode().Perm() != 0o700 {
		t.Errorf("mode %v: only root may read what is staged", info.Mode().Perm())
	}
	if noted, found, _ := ReadMarker(stager.Dir); !found || noted.To != "1.2.0" {
		t.Errorf("the note: %+v %v", noted, found)
	}
	if len(said) < 3 {
		t.Errorf("little was said: %q", said)
	}
}

func TestAReleaseIsRefusedWhenAnythingAboutItIsOff(t *testing.T) {
	_, otherKey, _ := release.GenerateKey()
	for name, tc := range map[string]struct {
		current string
		change  func(*shelf, *Stager)
		want    string
	}{
		"a version that is not later":   {"1.2.0", func(*shelf, *Stager) {}, "not later"},
		"an earlier version running on": {"1.3.0", func(*shelf, *Stager) {}, "not later"},
		"a build that is not a release": {"dev", func(*shelf, *Stager) {}, "not a release"},
		"a manifest signed by a stranger": {"1.1.0", func(s *shelf, st *Stager) {
			st.URLFor = func(string, string, string) string { return s.URL + "/other.json" }
			s.Config.Handler.(*http.ServeMux).HandleFunc("/other.json", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(s.manifest(t, otherKey, release.Manifest{Version: "1.2.0", OS: "linux", Arch: "amd64", SHA256: sum(binary), Size: int64(len(binary)), URL: s.URL + "/files/sentinel_linux_amd64"}))
			})
		}, "signature"},
		"a binary that was changed":                  {"1.1.0", func(s *shelf, _ *Stager) { s.body = []byte("#!/bin/sh\necho version 1.2.0 plus a backdoor\n") }, "bytes"},
		"a binary of the right size but other bytes": {"1.1.0", func(s *shelf, _ *Stager) { s.body = []byte(strings.Replace(string(binary), "echo", "ech0", 1)) }, "sha256"},
		"a binary longer than the manifest says":     {"1.1.0", func(s *shelf, _ *Stager) { s.body = append(append([]byte{}, binary...), []byte("more")...) }, "more than"},
		"another architecture":                       {"1.1.0", func(_ *shelf, st *Stager) { st.GOARCH = "arm64" }, "404"},
		"a binary that says another version": {"1.1.0", func(_ *shelf, st *Stager) {
			st.Check = func(context.Context, string) (string, error) { return "sentinel version 1.9.9", nil }
		}, "says it is"},
		"a binary that does not run": {"1.1.0", func(_ *shelf, st *Stager) {
			st.Check = func(context.Context, string) (string, error) { return "", errors.New("exec format error") }
		}, "does not run"},
	} {
		s := newShelf(t)
		stager := s.stager(t, tc.current)
		tc.change(s, stager)
		_, err := stager.Stage(t.Context(), "1.2.0", nothing)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, tc.want)
		}
		if _, statErr := os.Stat(filepath.Join(stager.Dir, StagedName)); statErr == nil {
			t.Errorf("%s: a refused binary was left staged", name)
		}
		if _, found, _ := ReadMarker(stager.Dir); found {
			t.Errorf("%s: a refused release has a note", name)
		}
	}
}

func TestAManifestForAnotherVersionIsRefused(t *testing.T) {
	s := newShelf(t)
	stager := s.stager(t, "v1.1.0")
	stager.URLFor = func(_, goos, goarch string) string { return s.URL + "/1.2.0/manifest-" + goos + "-" + goarch + ".json" }
	if _, err := stager.Stage(t.Context(), "1.5.0", nothing); err == nil || !strings.Contains(err.Error(), "is for 1.2.0, not 1.5.0") {
		t.Errorf("a manifest of an older release given as a newer one: %v", err)
	}
}

func TestARedirectIsNotFollowedAndATaskCannotNameAVersionThatIsNotOne(t *testing.T) {
	s := newShelf(t)
	stager := s.stager(t, "v1.1.0")
	stager.URLFor = func(string, string, string) string { return s.URL + "/moved" }
	if _, err := stager.Stage(t.Context(), "1.2.0", nothing); err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("a redirect: %v", err)
	}
	for _, bad := range []string{"", "latest", "1.2", "v1.2.0", "1.2.0/../../x", "1.2.0?x=1"} {
		if _, err := stager.Stage(t.Context(), bad, nothing); err == nil || !strings.Contains(err.Error(), "not a version") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if s.hits.Load() != 0 {
		t.Error("a version that is not one was looked up")
	}
}

// installation is a binary in a directory, with the directory of the update next to it.
func installation(t *testing.T) (dir, exe string) {
	t.Helper()
	root := t.TempDir()
	exe = filepath.Join(root, "bin", "sentinel")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(root, "state", "update")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, exe
}

func stage(t *testing.T, dir string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, StagedName), content, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarker(dir, Marker{State: Staged, From: "1.1.0", To: "1.2.0", SHA256: sum(binary), At: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallKeepsTheOldBinaryAndPutsTheNewOneInItsPlace(t *testing.T) {
	dir, exe := installation(t)
	stage(t, dir, binary)
	if err := Install(dir, exe); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != string(binary) {
		t.Errorf("the binary is %q", data)
	}
	if data, _ := os.ReadFile(exe + ".prev"); string(data) != "old binary" {
		t.Errorf("the previous binary is %q", data)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Error("the temporary file was left")
	}
	if marker, _, _ := ReadMarker(dir); marker.State != Installed || marker.Starts != 0 {
		t.Errorf("%+v", marker)
	}
	if _, err := os.Stat(filepath.Join(dir, StagedName)); err == nil {
		t.Error("the staged copy was left")
	}
}

func TestInstallRefusesWhatIsNotTheBinaryThatWasChecked(t *testing.T) {
	for name, prepare := range map[string]func(dir, exe string){
		"a staged binary that was swapped": func(dir, _ string) { stage(t, dir, []byte("something else")) },
		"no note":                          func(dir, _ string) { _ = os.WriteFile(filepath.Join(dir, StagedName), binary, 0o700) },
		"a note of an installed update": func(dir, _ string) {
			stage(t, dir, binary)
			_ = WriteMarker(dir, Marker{State: Installed, SHA256: sum(binary), At: time.Now()})
		},
		"an old note": func(dir, _ string) {
			stage(t, dir, binary)
			_ = WriteMarker(dir, Marker{State: Staged, SHA256: sum(binary), At: time.Now().Add(-2 * time.Hour)})
		},
		"a staged binary that is a link": func(dir, _ string) {
			stage(t, dir, binary)
			_ = os.Remove(filepath.Join(dir, StagedName))
			_ = os.Symlink("/bin/sh", filepath.Join(dir, StagedName))
		},
	} {
		dir, exe := installation(t)
		prepare(dir, exe)
		if err := Install(dir, exe); err == nil {
			t.Errorf("%s: installed", name)
		}
		if data, _ := os.ReadFile(exe); string(data) != "old binary" {
			t.Errorf("%s: the binary was changed to %q", name, data)
		}
		if _, err := os.Stat(exe + ".prev"); err == nil {
			t.Errorf("%s: a previous binary was made", name)
		}
	}
	dir, exe := installation(t)
	stage(t, dir, binary)
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", exe); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir, exe); err == nil {
		t.Error("a binary that is a link was replaced")
	}
}

func TestRollbackPutsThePreviousBinaryBackAndSaysWhy(t *testing.T) {
	dir, exe := installation(t)
	stage(t, dir, binary)
	if err := Install(dir, exe); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(dir, exe, "no report in 5m"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "old binary" {
		t.Errorf("the binary is %q", data)
	}
	if marker, _, _ := ReadMarker(dir); marker.State != RolledBack || marker.Reason != "no report in 5m" || marker.To != "1.2.0" {
		t.Errorf("%+v", marker)
	}
	if err := Rollback(dir, exe, "again"); err == nil {
		t.Error("rolled back twice")
	}
}

func TestTheTrialCountsStartsAndEndsWhenTheNewVersionReports(t *testing.T) {
	dir, exe := installation(t)
	stage(t, dir, binary)
	if err := Install(dir, exe); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if verdict, _, _ := BeginTrial(dir, "v1.1.0", now); verdict != NoTrial { // the old binary is still what runs
		t.Errorf("the old version running: %v", verdict)
	}
	if _, found, _ := ReadMarker(dir); found {
		t.Error("a note about a binary that does not run was kept")
	}
	stage(t, dir, binary)
	if err := Install(dir, exe); err != nil {
		t.Fatal(err)
	}
	for start := 1; start <= MaxTrialStarts; start++ {
		if verdict, marker, err := BeginTrial(dir, "v1.2.0", now); err != nil || verdict != OnTrial || marker.Starts != start {
			t.Fatalf("start %d: %v %+v %v", start, verdict, marker, err)
		}
	}
	if verdict, _, _ := BeginTrial(dir, "v1.2.0", now); verdict != GoBack {
		t.Errorf("after too many starts: %v", verdict)
	}
	// A new version that reports ends its trial.
	dir, exe = installation(t)
	stage(t, dir, binary)
	_ = Install(dir, exe)
	if verdict, _, _ := BeginTrial(dir, "v1.2.0-3-gabc", now); verdict != OnTrial {
		t.Fatalf("%v", verdict)
	}
	Confirm(dir)
	if _, found, _ := ReadMarker(dir); found {
		t.Error("the note stayed after the new version reported")
	}
	if verdict, _, _ := BeginTrial(dir, "v1.2.0", now); verdict != NoTrial {
		t.Errorf("after the trial: %v", verdict)
	}
}

func TestANoteOfARollbackGoesWhenTheOldVersionRunsAgain(t *testing.T) {
	dir, exe := installation(t)
	stage(t, dir, binary)
	_ = Install(dir, exe)
	_ = Rollback(dir, exe, "x")
	if verdict, marker, _ := BeginTrial(dir, "v1.1.0", time.Now()); verdict != NoTrial || marker.State != RolledBack {
		t.Errorf("%v %+v", verdict, marker)
	}
	if _, found, _ := ReadMarker(dir); found {
		t.Error("the note stayed")
	}
}

func TestAStagedBinaryThatWasNeverInstalledIsDroppedWhenOld(t *testing.T) {
	dir, _ := installation(t)
	stage(t, dir, binary)
	if _, _, err := BeginTrial(dir, "v1.1.0", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := ReadMarker(dir); found {
		t.Error("an old staged note stayed")
	}
}

func TestADamagedNoteIsRemovedNotObeyed(t *testing.T) {
	dir, _ := installation(t)
	if err := os.WriteFile(filepath.Join(dir, MarkerName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if verdict, _, err := BeginTrial(dir, "v1.2.0", time.Now()); err == nil || verdict != NoTrial {
		t.Errorf("%v %v", verdict, err)
	}
	if _, found, _ := ReadMarker(dir); found {
		t.Error("the damaged note stayed")
	}
}
