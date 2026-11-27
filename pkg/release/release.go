// Package release is how an agent knows that a binary is one its owner published: a manifest that names the
// binary by its SHA-256 and size, signed with an ed25519 key whose public half is in the agent's own config. The
// control plane only says which version to update to; it never holds the key, so one that is taken over cannot
// hand an agent a binary of its own.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The limits on what is accepted: a manifest is a few hundred bytes, a binary some tens of megabytes.
const (
	MaxManifest = 16 << 10
	MaxBinary   = 256 << 20
	// domain is signed ahead of the manifest, so that a signature made for something else with the same key is
	// not taken for one of a release.
	domain = "sentinel-release-v1\n"
)

// Manifest says which binary is the release of Version for one OS and architecture.
type Manifest struct {
	Version string `json:"version"` // 1.2.0, no leading v
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256"` // of the binary, in hex
	Size    int64  `json:"size"`   // of the binary, in bytes
	URL     string `json:"url"`    // where the binary is, https
}

var (
	versionText = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
	nameText    = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
	hexText     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Validate checks the fields of a manifest the way an agent needs them: nothing in them is trusted for more
// than its shape.
func (m Manifest) Validate() error {
	switch {
	case !versionText.MatchString(m.Version):
		return fmt.Errorf("the version %q is not major.minor.patch", clip(m.Version))
	case !nameText.MatchString(m.OS), !nameText.MatchString(m.Arch):
		return errors.New("the os or the arch is not a plain name")
	case !hexText.MatchString(m.SHA256):
		return errors.New("the sha256 is not 64 hex digits in lower case")
	case m.Size < 1 || m.Size > MaxBinary:
		return fmt.Errorf("the size must be from 1 to %d bytes", MaxBinary)
	}
	return CheckURL(m.URL)
}

// ValidVersion says text is exactly major.minor.patch.
func ValidVersion(text string) bool { return versionText.MatchString(text) }

// CheckURL accepts an https URL with a host, without a user name, a password or a fragment.
func CheckURL(text string) error {
	parsed, err := url.Parse(text)
	switch {
	case err != nil, parsed.Scheme != "https", parsed.Host == "", parsed.User != nil, parsed.Fragment != "", len(text) > 512:
		return fmt.Errorf("%q is not an https URL", clip(text))
	}
	return nil
}

func clip(text string) string {
	if len(text) > 64 {
		return text[:64] + "…"
	}
	return text
}

// Envelope is a signed manifest as it is published.
type Envelope struct {
	Manifest  []byte `json:"manifest"`  // the JSON of a Manifest, exactly as it was signed
	Signature []byte `json:"signature"` // ed25519 over domain + Manifest
}

// GenerateKey makes a pair of keys.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// EncodeKey writes a key as text, the form of a config file and of a key file.
func EncodeKey(key []byte) string { return base64.StdEncoding.EncodeToString(key) }

// ParsePublicKey reads the public key of a config file.
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("the public key is not the base64 of 32 bytes")
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePrivateKey reads the private key of a key file.
func ParsePrivateKey(text string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("the private key is not the base64 of 64 bytes")
	}
	return ed25519.PrivateKey(raw), nil
}

// Sign validates the manifest and returns it signed, as the JSON to publish.
func Sign(key ed25519.PrivateKey, manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode the manifest: %w", err)
	}
	return json.MarshalIndent(Envelope{Manifest: body, Signature: ed25519.Sign(key, append([]byte(domain), body...))}, "", "  ")
}

// Verify checks the signature of a published manifest and returns what it says. It trusts nothing in data
// before the signature is good, and no field after it unless Validate passes.
func Verify(key ed25519.PublicKey, data []byte) (Manifest, error) {
	if len(key) != ed25519.PublicKeySize {
		return Manifest{}, errors.New("no public key to check the manifest with")
	}
	if len(data) > MaxManifest {
		return Manifest{}, errors.New("the manifest is too long")
	}
	var envelope Envelope
	if err := decodeStrict(data, &envelope); err != nil {
		return Manifest{}, fmt.Errorf("the manifest is not a signed manifest: %w", err)
	}
	if !ed25519.Verify(key, append([]byte(domain), envelope.Manifest...), envelope.Signature) {
		return Manifest{}, errors.New("the signature of the manifest is not valid for this public key")
	}
	var manifest Manifest
	if err := decodeStrict(envelope.Manifest, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("the signed manifest is damaged: %w", err)
	}
	return manifest, manifest.Validate()
}

// decodeStrict decodes one JSON value and nothing after it, with no field that is not known.
func decodeStrict(data []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("there is more after the JSON value")
	}
	return nil
}

// Sum is the SHA-256 and the size of what r holds, to put in a manifest.
func Sum(r io.Reader) (sha string, size int64, err error) {
	hash := sha256.New()
	size, err = io.Copy(hash, r)
	return hex.EncodeToString(hash.Sum(nil)), size, err
}

// Version is major.minor.patch.
type Version struct{ Major, Minor, Patch int }

// ParseVersion reads a version, as "1.2.0" or "v1.2.0"; anything after the third number that begins with "-" or "+"
// (the "-162-g404972b-dirty" of a build between releases) is not part of the version. Text that is not a release
// version, such as "dev", is an error.
func ParseVersion(text string) (Version, error) {
	text = strings.TrimPrefix(text, "v")
	if cut := strings.IndexAny(text, "-+"); cut >= 0 {
		text = text[:cut]
	}
	if !versionText.MatchString(text) {
		return Version{}, fmt.Errorf("%q is not a release version", clip(text))
	}
	var numbers [3]int
	for i, part := range strings.Split(text, ".") {
		numbers[i], _ = strconv.Atoi(part)
	}
	return Version{numbers[0], numbers[1], numbers[2]}, nil
}

// Newer says candidate is a later version than current. A build between releases counts as its release (v1.2.0-5-gabc
// is 1.2.0), so the release it came from is not newer than it.
func Newer(candidate, current Version) bool {
	if candidate.Major != current.Major {
		return candidate.Major > current.Major
	}
	if candidate.Minor != current.Minor {
		return candidate.Minor > current.Minor
	}
	return candidate.Patch > current.Patch
}
