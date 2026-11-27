package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func good() Manifest {
	return Manifest{Version: "1.2.0", OS: "linux", Arch: "amd64", SHA256: strings.Repeat("ab", 32), Size: 1234, URL: "https://releases.example.com/sentinel/1.2.0/sentinel_linux_amd64"}
}

func TestASignedManifestVerifiesAndGivesBackWhatWasSigned(t *testing.T) {
	public, private, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Sign(private, good())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(public, signed)
	if err != nil || got != good() {
		t.Errorf("%+v, %v", got, err)
	}
	// The key travels as text.
	again, err := ParsePublicKey(EncodeKey(public))
	if err != nil || !again.Equal(public) {
		t.Errorf("public key: %v", err)
	}
	priv, err := ParsePrivateKey(EncodeKey(private))
	if err != nil || !priv.Equal(private) {
		t.Errorf("private key: %v", err)
	}
}

func TestAManifestThatWasChangedOrSignedByAnotherKeyIsRefused(t *testing.T) {
	public, private, _ := GenerateKey()
	_, other, _ := GenerateKey()
	signed, _ := Sign(private, good())
	elsewhere, _ := Sign(other, good())

	var envelope Envelope
	_ = json.Unmarshal(signed, &envelope)
	changed := Envelope{Manifest: bytes.Replace(envelope.Manifest, []byte("1.2.0"), []byte("1.9.9"), 1), Signature: envelope.Signature}
	changedJSON, _ := json.Marshal(changed)
	// A signature that is good for the text, over a text that is not a manifest.
	junk := Envelope{Manifest: []byte(`{"version":"1.2.0","evil":true}`)}
	junk.Signature = ed25519.Sign(private, append([]byte(domain), junk.Manifest...))
	junkJSON, _ := json.Marshal(junk)
	// A signature of the same bytes without the domain: made for something else.
	other2 := Envelope{Manifest: envelope.Manifest, Signature: ed25519.Sign(private, envelope.Manifest)}
	other2JSON, _ := json.Marshal(other2)

	for name, data := range map[string][]byte{
		"another key":       elsewhere,
		"a changed version": changedJSON,
		"an unknown field":  junkJSON,
		"no domain":         other2JSON,
		"not json":          []byte("{"),
		"empty":             nil,
		"trailing data":     append(append([]byte{}, signed...), []byte(`{"x":1}`)...),
		"too long":          bytes.Repeat([]byte(" "), MaxManifest+1),
	} {
		if _, err := Verify(public, data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Verify(nil, signed); err == nil {
		t.Error("no key: accepted")
	}
}

func TestAManifestIsOnlyAsGoodAsItsFields(t *testing.T) {
	_, private, _ := GenerateKey()
	for name, change := range map[string]func(*Manifest){
		"a v in the version":      func(m *Manifest) { m.Version = "v1.2.0" },
		"a version with a suffix": func(m *Manifest) { m.Version = "1.2.0-rc1" },
		"a huge number":           func(m *Manifest) { m.Version = "1.2.99999999999" },
		"a leading zero":          func(m *Manifest) { m.Version = "01.2.0" },
		"an os with a slash":      func(m *Manifest) { m.OS = "../linux" },
		"an arch in capitals":     func(m *Manifest) { m.Arch = "AMD64" },
		"a short sum":             func(m *Manifest) { m.SHA256 = "abc" },
		"a sum in capitals":       func(m *Manifest) { m.SHA256 = strings.Repeat("AB", 32) },
		"no size":                 func(m *Manifest) { m.Size = 0 },
		"a size of a gigabyte":    func(m *Manifest) { m.Size = 1 << 30 },
		"an http url":             func(m *Manifest) { m.URL = "http://releases.example.com/x" },
		"a url with a password":   func(m *Manifest) { m.URL = "https://user:pw@releases.example.com/x" },
		"a url with no host":      func(m *Manifest) { m.URL = "https:///x" },
		"a file url":              func(m *Manifest) { m.URL = "file:///etc/passwd" },
		"a url with a fragment":   func(m *Manifest) { m.URL = "https://example.com/x#y" },
		"a url that is very long": func(m *Manifest) { m.URL = "https://example.com/" + strings.Repeat("a", 600) },
	} {
		manifest := good()
		change(&manifest)
		if _, err := Sign(private, manifest); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func TestVersionsAreComparedByTheirNumbers(t *testing.T) {
	version := func(text string) Version {
		t.Helper()
		parsed, err := ParseVersion(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		return parsed
	}
	for _, tc := range []struct {
		candidate, current string
		newer              bool
	}{
		{"1.2.1", "1.2.0", true},
		{"1.3.0", "1.2.9", true},
		{"2.0.0", "1.99.99", true},
		{"1.2.0", "1.2.0", false},
		{"1.2.0", "1.10.0", false},                    // not compared as text
		{"1.2.0", "v1.2.0-162-g404972b-dirty", false}, // a build after 1.2.0 is not older than 1.2.0
		{"1.2.1", "v1.2.0-162-g404972b-dirty", true},
		{"0.9.0", "1.0.0", false},
	} {
		if got := Newer(version(tc.candidate), version(tc.current)); got != tc.newer {
			t.Errorf("%s over %s: %v", tc.candidate, tc.current, got)
		}
	}
	for _, bad := range []string{"", "dev", "1.2", "1.2.3.4", "a.b.c", "v", "1.2.x", "-1.2.3"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("%q was taken for a version", bad)
		}
	}
}

func TestSumIsTheSHA256AndTheSize(t *testing.T) {
	sum, size, err := Sum(strings.NewReader("abc"))
	if err != nil || size != 3 || sum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("%s %d %v", sum, size, err)
	}
}
