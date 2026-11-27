package audit

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/server/internal/store"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func appendAll(t *testing.T, st *store.Store, ctx context.Context, entries ...Entry) {
	t.Helper()
	now := time.UnixMilli(1_700_000_000_000)
	for i, entry := range entries {
		err := st.InTx(ctx, func(tx *sql.Tx) error { return Append(ctx, tx, now.Add(time.Duration(i)*time.Second), entry) })
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppendChainsAndVerifyAcceptsTheChain(t *testing.T) {
	st := openStore(t)
	ctx := WithActor(context.Background(), Actor{Name: "admin", Session: "ab12cd34", IP: "10.0.0.5"})
	appendAll(t, st, ctx,
		Entry{Action: "login.ok"},
		Entry{Action: "agent.enroll", Target: "web-01"},
		Entry{Action: "agent.revoke", Target: "web-01", Detail: "lost laptop"})

	records, err := List(ctx, st.DB, 10)
	if err != nil || len(records) != 3 {
		t.Fatalf("records = %d, %v", len(records), err)
	}
	if records[0].Entry.Action != "agent.revoke" || records[2].PrevHash != "" || records[0].PrevHash != records[1].Hash || records[1].PrevHash != records[2].Hash {
		t.Errorf("the rows are not chained, newest first: %+v", records)
	}
	if got := records[1].Actor; got != (Actor{Name: "admin", Session: "ab12cd34", IP: "10.0.0.5"}) {
		t.Errorf("actor = %+v", got)
	}
	if checked, err := Verify(ctx, st.DB); err != nil || checked != 3 {
		t.Errorf("Verify = %d, %v; want 3, nil", checked, err)
	}
}

func TestAnEmptyLogVerifies(t *testing.T) {
	if checked, err := Verify(context.Background(), openStore(t).DB); err != nil || checked != 0 {
		t.Errorf("Verify = %d, %v", checked, err)
	}
}

func TestActorDefaultsToSystem(t *testing.T) {
	if got := ActorFrom(context.Background()); got != System {
		t.Errorf("actor = %+v, want system", got)
	}
}

// The trigger stops an UPDATE or DELETE, but root on the machine can drop the trigger. The chain is what
// shows it, so each way of tampering must be caught.
func TestVerifyFindsTampering(t *testing.T) {
	tests := []struct {
		name    string
		tamper  string
		wantErr string
	}{
		{"a field is edited", `UPDATE audit_log SET target = 'db-01' WHERE id = 2`, "row 2 was changed"},
		{"the actor is edited", `UPDATE audit_log SET actor = 'admin' WHERE id = 1`, "row 1 was changed"},
		{"a row is removed", `DELETE FROM audit_log WHERE id = 2`, "row 3 does not follow"},
		{"the first row is removed", `DELETE FROM audit_log WHERE id = 1`, "row 2 does not follow"},
		{"the last row is edited", `UPDATE audit_log SET detail = 'x' WHERE id = 3`, "row 3 was changed"},
		{"a row is added with a wrong hash", `INSERT INTO audit_log (at, actor, session, ip, action, target, detail, prev_hash, hash) VALUES (9, 'admin', '', '', 'x', '', '', 'nope', 'nope')`, "does not follow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openStore(t)
			ctx := context.Background()
			appendAll(t, st, ctx, Entry{Action: "a"}, Entry{Action: "b", Target: "web-01"}, Entry{Action: "c"})
			for _, drop := range []string{`DROP TRIGGER audit_log_no_update`, `DROP TRIGGER audit_log_no_delete`} {
				if _, err := st.DB.Exec(drop); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.DB.Exec(tt.tamper); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(ctx, st.DB); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Verify err = %v, want it to say %q", err, tt.wantErr)
			}
		})
	}
}

// A field boundary must be part of the hash: moving text from one field to the next is a change.
func TestHashSeparatesFields(t *testing.T) {
	first := hashOf(Record{Entry: Entry{Action: "ab", Target: "c"}})
	second := hashOf(Record{Entry: Entry{Action: "a", Target: "bc"}})
	if first == second {
		t.Error("two different records have the same hash")
	}
}

func TestAppendInARolledBackTransactionLeavesNothing(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	_ = st.InTx(ctx, func(tx *sql.Tx) error {
		if err := Append(ctx, tx, time.Now(), Entry{Action: "x"}); err != nil {
			t.Fatal(err)
		}
		return sql.ErrConnDone
	})
	if records, _ := List(ctx, st.DB, 10); len(records) != 0 {
		t.Errorf("records = %d, want none", len(records))
	}
}
