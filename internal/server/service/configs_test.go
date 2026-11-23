package service

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"sentinel/internal/api"
)

const goodConfig = `repositories:
  r: { url: /tmp/r, password: "${RESTIC_PW}" }
backups:
  - { name: a, type: files, paths: [/x], repositories: [r] }
`

func TestConfigVersions(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a, _, _ := s.CreateAgent(ctx, "web-01")

	if _, ok, _ := s.CurrentConfig(ctx, a.ID); ok {
		t.Fatal("new agent has a config")
	}
	imported, err := s.ImportConfig(ctx, a.ID, goodConfig)
	if err != nil || !imported {
		t.Fatalf("import = %v, %v", imported, err)
	}
	if again, _ := s.ImportConfig(ctx, a.ID, "uptime: []"); again {
		t.Fatal("second import overwrote the config")
	}

	bad := []string{
		"backups:\n  - { name: a, type: tape }",
		"store: /evil.db",
		"not: [valid",
	}
	for _, y := range bad {
		if _, err := s.SaveConfig(ctx, a.ID, y, ""); err == nil {
			t.Errorf("invalid config accepted: %q", y)
		}
	}

	v2, err := s.SaveConfig(ctx, a.ID, goodConfig+"repo_check_schedule: \"0 4 * * 0\"\n", "weekly check")
	if err != nil || v2 != 2 {
		t.Fatalf("save = %d, %v", v2, err)
	}
	v3, err := s.Rollback(ctx, a.ID, 1)
	if err != nil || v3 != 3 {
		t.Fatalf("rollback = %d, %v", v3, err)
	}
	cur, _, _ := s.CurrentConfig(ctx, a.ID)
	if cur.Version != 3 || cur.YAML != goodConfig {
		t.Errorf("current = %+v", cur)
	}
	hist, _ := s.ConfigHistory(ctx, a.ID, 10)
	if len(hist) != 3 || hist[0].Comment != "rollback to v1" {
		t.Errorf("history = %+v", hist)
	}

	s.SetConfigStatus(ctx, a.ID, api.ConfigStatus{Version: 3, Applied: false, Error: "unset environment variables: [RESTIC_PW]"})
	got, _ := s.GetAgent(ctx, a.ID)
	if got.ConfigStatus.Applied || !strings.Contains(got.ConfigStatus.Error, "RESTIC_PW") {
		t.Errorf("status = %+v", got.ConfigStatus)
	}
	s.SetConfigStatus(ctx, a.ID, api.ConfigStatus{Version: 3, Applied: true})
	if got, _ := s.GetAgent(ctx, a.ID); !got.ConfigStatus.Applied {
		t.Errorf("status after apply = %+v", got.ConfigStatus)
	}
}

func TestDiff(t *testing.T) {
	d := Diff("a\nb\nc", "a\nc\nd")
	var got []string
	for _, l := range d {
		got = append(got, l.Kind+":"+l.Text)
	}
	want := "same:a del:b same:c add:d"
	if strings.Join(got, " ") != want {
		t.Errorf("diff = %v, want %s", got, want)
	}
}

func TestDiffEdgeCases(t *testing.T) {
	for name, tc := range map[string]struct{ a, b, want string }{
		"identical":          {"a\nb\n", "a\nb\n", "same:a same:b"},
		"empty to something": {"", "a\nb", "del: add:a add:b"},
		"trailing newline":   {"a\nb", "a\nb\n", "same:a same:b"},
		"replace middle":     {"a\nx\nc\n", "a\ny\nc\n", "same:a del:x add:y same:c"},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			for _, l := range Diff(tc.a, tc.b) {
				got = append(got, l.Kind+":"+l.Text)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("diff = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestDiffLargeConfigStaysCheap(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 20000; i++ { // ~256 KiB of lines: the old table needed n*m ints
		a.WriteString("line number " + strconv.Itoa(i) + "\n")
		b.WriteString("line number " + strconv.Itoa(i) + "\n")
	}
	b.WriteString("one more\n")
	if d := Diff(a.String(), b.String()); len(d) != 20001 || d[20000].Kind != "add" {
		t.Errorf("unexpected diff of %d lines", len(d))
	}
}
