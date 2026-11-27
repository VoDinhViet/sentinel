package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/pkg/release"
)

func runRelease(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd(&cli{})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"release"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestKeygenMakesAPrivateKeyFileAndNeverOverwritesIt(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "release.key")
	out, err := runRelease(t, "keygen", "--out", keyFile)
	if err != nil || !strings.Contains(out, "public_key: ") {
		t.Fatalf("keygen: %v\n%s", err, out)
	}
	info, err := os.Stat(keyFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("key file: %v %v", info, err)
	}
	if _, err := runRelease(t, "keygen", "--out", keyFile); err == nil {
		t.Error("a key file was overwritten")
	}
}

func TestSignMakesAManifestThatTheMatchingPublicKeyVerifies(t *testing.T) {
	dir := t.TempDir()
	keyFile, binary, manifestFile := filepath.Join(dir, "release.key"), filepath.Join(dir, "sentinel"), filepath.Join(dir, "manifest.json")
	out, err := runRelease(t, "keygen", "--out", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	publicText := strings.TrimSpace(out[strings.Index(out, "public_key: ")+len("public_key: "):])
	if err := os.WriteFile(binary, []byte("the binary"), 0o755); err != nil { //nolint:gosec // a test file
		t.Fatal(err)
	}
	args := []string{"sign", "--key", keyFile, "--binary", binary, "--version", "1.2.0", "--os", "linux", "--arch", "amd64", "--url", "https://releases.example.com/sentinel", "--out", manifestFile}
	if out, err := runRelease(t, args...); err != nil {
		t.Fatalf("sign: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	public, err := release.ParsePublicKey(publicText)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := release.Verify(public, raw)
	if err != nil || manifest.Version != "1.2.0" || manifest.OS != "linux" || manifest.Size != int64(len("the binary")) {
		t.Errorf("manifest = %+v, %v", manifest, err)
	}
}

func TestSignRefusesAKeyFileOthersCanReadAndAnAddressThatIsNotHTTPS(t *testing.T) {
	dir := t.TempDir()
	keyFile, binary := filepath.Join(dir, "release.key"), filepath.Join(dir, "sentinel")
	if _, err := runRelease(t, "keygen", "--out", keyFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"sign", "--key", keyFile, "--binary", binary, "--version", "1.2.0", "--url", "https://releases.example.com/s"}
	if err := os.Chmod(keyFile, 0o644); err != nil { //nolint:gosec // the point of the test
		t.Fatal(err)
	}
	if _, err := runRelease(t, args...); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("a world-readable key was used: %v", err)
	}
	if err := os.Chmod(keyFile, 0o600); err != nil {
		t.Fatal(err)
	}
	insecure := []string{"sign", "--key", keyFile, "--binary", binary, "--version", "1.2.0", "--url", "http://releases.example.com/s"}
	if _, err := runRelease(t, insecure...); err == nil {
		t.Error("an http address was signed")
	}
	bad := []string{"sign", "--key", keyFile, "--binary", binary, "--version", "v1.2", "--url", "https://releases.example.com/s"}
	if _, err := runRelease(t, bad...); err == nil {
		t.Error("a bad version was signed")
	}
}
