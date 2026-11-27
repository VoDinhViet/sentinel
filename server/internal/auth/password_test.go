package auth

import (
	"strings"
	"testing"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "1000" {
		t.Fatalf("hash = %q, want pbkdf2-sha256$<rounds>$<salt>$<hash>", hash)
	}
	if strings.Contains(hash, "correct horse") {
		t.Error("the hash holds the password")
	}
	for password, want := range map[string]bool{"correct horse battery": true, "correct horse batterY": false, "": false, "correct horse battery ": false} {
		match, stale, err := CheckPassword(hash, password)
		if err != nil || match != want || stale {
			t.Errorf("CheckPassword(%q) = %v, stale %v, %v; want %v", password, match, stale, err, want)
		}
	}
	other, _ := HashPassword("correct horse battery")
	if other == hash {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
}

func TestCheckPasswordSaysWhenTheHashIsOlder(t *testing.T) {
	old, err := hashWith("correct horse battery", 500, []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	match, stale, err := CheckPassword(old, "correct horse battery")
	if err != nil || !match || !stale {
		t.Errorf("old hash: match %v, stale %v, %v; want it accepted and stale", match, stale, err)
	}
}

func TestCheckPasswordRefusesADamagedHashAsAnErrorNotAMismatch(t *testing.T) {
	good, _ := HashPassword("correct horse battery")
	parts := strings.Split(good, "$")
	for name, stored := range map[string]string{
		"empty":          "",
		"another scheme": "bcrypt$1000$" + parts[2] + "$" + parts[3],
		"too few parts":  "pbkdf2-sha256$1000$abc",
		"rounds text":    "pbkdf2-sha256$many$" + parts[2] + "$" + parts[3],
		"zero rounds":    "pbkdf2-sha256$0$" + parts[2] + "$" + parts[3],
		"huge rounds":    "pbkdf2-sha256$999999999$" + parts[2] + "$" + parts[3],
		"short salt":     "pbkdf2-sha256$1000$AAAA$" + parts[3],
		"bad salt":       "pbkdf2-sha256$1000$!!!$" + parts[3],
		"short key":      "pbkdf2-sha256$1000$" + parts[2] + "$AAAA",
	} {
		if match, _, err := CheckPassword(stored, "correct horse battery"); err == nil || match {
			t.Errorf("%s: match %v, err %v; want an error", name, match, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for password, ok := range map[string]bool{
		"": false, "short": false, "elevenchars": false, "twelve chars": true,
		"mật khẩu dài đủ rồi": true, "mật khẩu": false, strings.Repeat("a", 1024): true, strings.Repeat("a", 1025): false,
	} {
		if err := ValidatePassword(password); (err == nil) != ok {
			t.Errorf("ValidatePassword(%.20q) = %v, want ok=%v", password, err, ok)
		}
	}
}
