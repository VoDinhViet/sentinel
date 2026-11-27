// Package selfupdate replaces the binary of the agent with a later release, and puts the old one back when the new one
// does not work. The release is the one the owner signed: the manifest is fetched from the address in the agent's own
// config and checked against the public key in it, so what the control plane sends (a version number) cannot
// bring in a binary of its own. The steps are three:
//
//   - Stage (in the daemon): fetch and verify the manifest, download the binary, check its size and SHA-256 against
//     the manifest, try it with --version, and keep it in the state directory with a note of what it is.
//   - Install (in a transient unit, because the sandbox of the daemon keeps /usr read-only): check the staged binary
//     against the note again, keep the running one as sentinel.prev and put the new one in its place, atomically.
//   - Trial (in the daemon after the restart): if the new version does not report to the control plane within
//     five minutes, or it starts again and again without getting there, put sentinel.prev back.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sentinel/pkg/release"
)

// Names and limits.
const (
	// DirName is the directory of the update, inside the one the store is in.
	DirName = "update"
	// StagedName is the downloaded binary; MarkerName is the note about it and about the trial.
	StagedName = "sentinel.new"
	MarkerName = "update.json"
	// ManifestName is the signed manifest of the staged release, kept so that the helper that installs it can check it again.
	ManifestName = "manifest.json"
	// SnapshotName is a copy of the state database made before the install: the previous binary cannot open a database
	// that a newer schema has changed, so a rollback puts the copy back in that case.
	SnapshotName = "store.prev.db"
	// TrialWindow is how long a new version has to report to the control plane before it is taken for bad.
	TrialWindow = 5 * time.Minute
	// MaxTrialStarts is how many times the new version may be started in its trial: a version that crashes at once
	// is put back without waiting for the window.
	MaxTrialStarts = 3
	// staleStaged is how long a staged binary that was never installed is kept.
	staleStaged  = time.Hour
	manifestWait = time.Minute
	binaryWait   = 8 * time.Minute
	checkWait    = 10 * time.Second
)

// Marker is the note in the state directory about an update that is going on.
type Marker struct {
	State  string    `json:"state"` // staged, installed or rolled-back
	From   string    `json:"from"`
	To     string    `json:"to"`
	SHA256 string    `json:"sha256"`
	At     time.Time `json:"at"`
	Starts int       `json:"starts"` // how many times the new version was started in its trial
	Reason string    `json:"reason,omitempty"`
}

// The states of a Marker.
const (
	Staged     = "staged"
	Installed  = "installed"
	RolledBack = "rolled-back"
)

// Dir is the directory of the update for a store at storePath.
func Dir(storePath string) string { return filepath.Join(filepath.Dir(storePath), DirName) }

// ReadMarker reads the note; a missing file is no note and no error.
func ReadMarker(dir string) (Marker, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, fmt.Errorf("read the note of the update: %w", err)
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return Marker{}, false, fmt.Errorf("the note of the update is damaged: %w", err)
	}
	return marker, true, nil
}

// WriteMarker keeps the note, written whole and renamed in place so that a stop never leaves half of it.
func WriteMarker(dir string, marker Marker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, MarkerName), data, 0o600)
}

// RemoveMarker deletes the note and the staged binary.
func RemoveMarker(dir string) {
	_ = os.Remove(filepath.Join(dir, MarkerName))
	_ = os.Remove(filepath.Join(dir, StagedName))
	_ = os.Remove(filepath.Join(dir, ManifestName))
	_ = os.Remove(filepath.Join(dir, SnapshotName))
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, mode); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// Stager fetches, checks and keeps a release.
type Stager struct {
	// PublicKey and ManifestURL are the keys of allow.self_update; URL gives the address of one release.
	PublicKey    string
	URLFor       func(version, goos, goarch string) string
	Current      string // the version that runs
	GOOS, GOARCH string
	Dir          string
	// HTTP is the client; nil means one with no proxy, no redirect and a TLS 1.2 floor.
	HTTP *http.Client
	// Check tries the staged binary and returns what it says of its version; nil runs it with --version.
	Check func(ctx context.Context, path string) (string, error)
}

// Forget drops a staged binary and its note: the update did not go through.
// An update that is already on trial is left alone.
func (s *Stager) Forget() {
	if marker, found, _ := ReadMarker(s.Dir); found && marker.State != Staged {
		return
	}
	RemoveMarker(s.Dir)
	_ = os.Remove(filepath.Join(s.Dir, StagedName))
	_ = os.Remove(filepath.Join(s.Dir, ManifestName))
}

// Client is the HTTP client for the release files: no proxy (a proxy is another party in the middle), no
// redirect (the address in the manifest is the address that is used), and TLS 1.2 at least.
func Client() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = nil
	return &http.Client{Transport: transport, CheckRedirect: noRedirect}
}

// noRedirect stops the client at an answer that points somewhere else, which then counts as no good.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (s *Stager) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return Client()
}

// get reads at most limit bytes of the body at address; a longer body is an error.
func (s *Stager) get(ctx context.Context, address string, limit int64, timeout time.Duration, into io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "sentinel-agent")
	response, err := s.client().Do(request)
	if err != nil {
		// A *url.Error prints the whole address, query included, and that may carry a token.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("fetch %s: %w", host(address), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: the answer was %s", host(address), response.Status)
	}
	written, err := io.Copy(into, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", host(address), err)
	}
	if written > limit {
		return fmt.Errorf("%s sent more than %d bytes", host(address), limit)
	}
	return nil
}

// host is the scheme and host of an address: nothing after them (a path, a query) is repeated in text that is sent away.
func host(address string) string {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return "the release server"
	}
	return parsed.Scheme + "://" + parsed.Host
}

// Stage gets the release of version and keeps it, ready to install. It refuses a version that is not later than
// the one that runs, and a manifest that is not signed by the key, or is for another version, system or machine.
func (s *Stager) Stage(ctx context.Context, version string, log func(string)) (Marker, error) {
	wanted, err := release.ParseVersion(version)
	if err != nil || !release.ValidVersion(version) {
		return Marker{}, fmt.Errorf("%q is not a version like 1.2.0", version)
	}
	current, err := release.ParseVersion(s.Current)
	if err != nil {
		return Marker{}, fmt.Errorf("this agent runs %q, which is not a release: it cannot tell whether %s is later (install a release by hand first)", s.Current, version)
	}
	if !release.Newer(wanted, current) {
		return Marker{}, fmt.Errorf("%s is not later than the version that runs (%s): an agent is never taken back to an earlier release by a task", version, s.Current)
	}
	key, err := release.ParsePublicKey(s.PublicKey)
	if err != nil {
		return Marker{}, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Marker{}, fmt.Errorf("make the directory of the update: %w", err)
	}
	if existing, found, _ := ReadMarker(s.Dir); found && existing.State == Installed {
		return Marker{}, fmt.Errorf("version %s was installed and has not reported to the control plane yet: it is on trial, and another update waits for it to end", existing.To)
	}
	address := s.URLFor(version, s.GOOS, s.GOARCH)
	log("fetching the manifest from " + host(address))
	var raw limitedBuffer
	if err := s.get(ctx, address, release.MaxManifest, manifestWait, &raw); err != nil {
		return Marker{}, err
	}
	manifest, err := release.Verify(key, raw.bytes())
	if err != nil {
		return Marker{}, err
	}
	switch {
	case manifest.Version != version:
		return Marker{}, fmt.Errorf("the manifest is for %s, not %s", manifest.Version, version)
	case manifest.OS != s.GOOS || manifest.Arch != s.GOARCH:
		return Marker{}, fmt.Errorf("the manifest is for %s/%s, this machine is %s/%s", manifest.OS, manifest.Arch, s.GOOS, s.GOARCH)
	}
	log(fmt.Sprintf("the manifest is signed by the key of this agent: %s, %d bytes, sha256 %s", manifest.Version, manifest.Size, manifest.SHA256[:12]))
	staged := filepath.Join(s.Dir, StagedName)
	if err := s.download(ctx, manifest, staged, log); err != nil {
		_ = os.Remove(staged)
		return Marker{}, err
	}
	said, err := s.check(ctx, staged)
	if err != nil {
		_ = os.Remove(staged)
		return Marker{}, fmt.Errorf("the new binary does not run on this machine: %w", err)
	}
	if !saysVersion(said, version) {
		_ = os.Remove(staged)
		return Marker{}, fmt.Errorf("the new binary says it is %q, not %s", strings.TrimSpace(said), version)
	}
	if err := writeFile(filepath.Join(s.Dir, ManifestName), raw.bytes(), 0o600); err != nil {
		return Marker{}, fmt.Errorf("keep the signed manifest: %w", err)
	}
	marker := Marker{State: Staged, From: s.Current, To: version, SHA256: manifest.SHA256, At: time.Now()}
	if err := WriteMarker(s.Dir, marker); err != nil {
		return Marker{}, fmt.Errorf("keep the note of the update: %w", err)
	}
	log("the new binary is checked and kept; installing it")
	return marker, nil
}

// download writes the binary to path and checks its size and SHA-256 against the manifest.
func (s *Stager) download(ctx context.Context, manifest release.Manifest, path string, log func(string)) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700) //nolint:gosec // the staged binary is run for --version, and only by root
	if err != nil {
		return fmt.Errorf("make the file of the new binary: %w", err)
	}
	hash := sha256.New()
	log("downloading the binary from " + host(manifest.URL))
	counted := &countingWriter{w: io.MultiWriter(file, hash)}
	getErr := s.get(ctx, manifest.URL, manifest.Size, binaryWait, counted)
	if err := errors.Join(getErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if counted.n != manifest.Size {
		return fmt.Errorf("the binary is %d bytes, the manifest says %d", counted.n, manifest.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != manifest.SHA256 {
		return fmt.Errorf("the sha256 of the binary is %s…, the signed manifest says %s…", got[:12], manifest.SHA256[:12])
	}
	return nil
}

// check runs the staged binary with --version, with a clock and a small environment.
func (s *Stager) check(ctx context.Context, path string) (string, error) {
	if s.Check != nil {
		return s.Check(ctx, path)
	}
	ctx, cancel := context.WithTimeout(ctx, checkWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version") //nolint:gosec // a file this package wrote and checked against a signed sha256
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	out, err := cmd.CombinedOutput()
	if len(out) > 1024 {
		out = out[:1024]
	}
	return string(out), err
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *limitedBuffer) bytes() []byte { return b.data }

// sumFile is the SHA-256 of a file, in hex.

// saysVersion says the output of --version names the release, as a word: 1.2.0 is not in 11.2.0.
func saysVersion(said, version string) bool {
	for _, word := range strings.Fields(said) {
		if parsed, err := release.ParseVersion(word); err == nil {
			if wanted, err := release.ParseVersion(version); err == nil && parsed == wanted {
				return true
			}
		}
	}
	return false
}
