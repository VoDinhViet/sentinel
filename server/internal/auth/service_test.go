package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
)

const password = "correct horse battery"

func TestMain(m *testing.M) {
	rounds = 1000 // the tests sign in dozens of times
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// newService is a service on a new database with the password set. Call it inside synctest.Test: time
// then moves only when the test sleeps.
func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	service := New(st)
	if err := service.SetPassword(context.Background(), password); err != nil {
		t.Fatal(err)
	}
	return service, st
}

func wrong(t *testing.T, service *Service, ip string) error {
	t.Helper()
	_, _, err := service.Login(context.Background(), ip, "not the password")
	return err
}

func TestLoginWithTheRightPasswordStartsASession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		ctx := context.Background()
		token, session, err := service.Login(ctx, "10.0.0.1", password)
		if err != nil {
			t.Fatal(err)
		}
		if len(token) < 43 || session.ID == "" || len(session.ID) != 8 || session.IP != "10.0.0.1" || session.ExpiresAt.Sub(session.CreatedAt) != SessionLifetime {
			t.Errorf("token %q, session %+v", token, session)
		}
		if got, err := service.Authenticate(ctx, token); err != nil || got.ID != session.ID {
			t.Errorf("Authenticate = %+v, %v", got, err)
		}
		if _, err := service.Authenticate(ctx, token+"x"); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("another token: err = %v", err)
		}
		// The token is shown once: the database holds its hash only.
		var rows int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, token).Scan(&rows); err != nil || rows != 0 {
			t.Errorf("the token itself is stored (%d rows, %v)", rows, err)
		}
		var hashed string
		if err := st.DB.QueryRow(`SELECT password_hash FROM login`).Scan(&hashed); err != nil || strings.Contains(hashed, password) {
			t.Errorf("stored password hash = %q, %v", hashed, err)
		}
	})
}

func TestLoginRefusesAWrongPasswordAndAnUninitializedServer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		if err := wrong(t, service, "10.0.0.1"); !errors.Is(err, ErrBadPassword) {
			t.Errorf("err = %v, want a wrong password", err)
		}
		st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "empty.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if _, _, err := New(st).Login(context.Background(), "10.0.0.1", password); !errors.Is(err, ErrNotInitialized) {
			t.Errorf("err = %v, want not initialized", err)
		}
	})
}

func TestFiveWrongPasswordsBlockOneAddressForFifteenMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		ctx := context.Background()
		for attempt := 1; attempt < MaxFailures; attempt++ {
			if err := wrong(t, service, "10.0.0.9"); !errors.Is(err, ErrBadPassword) {
				t.Fatalf("attempt %d: err = %v", attempt, err)
			}
		}
		var blocked *BlockedError
		if err := wrong(t, service, "10.0.0.9"); !errors.As(err, &blocked) {
			t.Fatalf("the fifth wrong password: err = %v, want the block", err)
		}
		// Blocked: even the right password is refused, and another address is not affected.
		if _, _, err := service.Login(ctx, "10.0.0.9", password); !errors.As(err, &blocked) {
			t.Errorf("right password while blocked: err = %v", err)
		}
		if _, _, err := service.Login(ctx, "10.0.0.10", password); err != nil {
			t.Errorf("another address: %v", err)
		}

		time.Sleep(BlockFor - time.Second)
		if _, _, err := service.Login(ctx, "10.0.0.9", password); !errors.As(err, &blocked) {
			t.Errorf("just before the end of the block: err = %v", err)
		}
		time.Sleep(2 * time.Second)
		if _, _, err := service.Login(ctx, "10.0.0.9", password); err != nil {
			t.Errorf("after the block: %v", err)
		}
	})
}

func TestTheBlockStartsCountingAgainAfterItEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		for range MaxFailures {
			_ = wrong(t, service, "10.0.0.9")
		}
		time.Sleep(BlockFor + time.Second)
		for attempt := 1; attempt < MaxFailures; attempt++ {
			if err := wrong(t, service, "10.0.0.9"); !errors.Is(err, ErrBadPassword) {
				t.Fatalf("attempt %d after the block: err = %v, want a plain wrong password", attempt, err)
			}
		}
	})
}

func TestASuccessAndTheWindowForgetOldFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		ctx := context.Background()
		for range MaxFailures - 1 {
			_ = wrong(t, service, "10.0.0.9")
		}
		if _, _, err := service.Login(ctx, "10.0.0.9", password); err != nil {
			t.Fatal(err)
		}
		for range MaxFailures - 1 {
			if err := wrong(t, service, "10.0.0.9"); !errors.Is(err, ErrBadPassword) {
				t.Fatalf("after a success the count must start again: err = %v", err)
			}
		}
		time.Sleep(FailureWindow + time.Minute)
		if err := wrong(t, service, "10.0.0.9"); !errors.Is(err, ErrBadPassword) {
			t.Errorf("failures older than the window must be forgotten: err = %v", err)
		}
	})
}

// When the whole server has seen many wrong passwords, each address gets one try; an address that has not
// failed can still sign in, so an attacker cannot lock the owner of the password out.
func TestManyFailuresAcrossAddressesTightenEachAddressButDoNotLockAnyoneOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		ctx := context.Background()
		for i := range ServerFailureCap {
			if err := wrong(t, service, fmt.Sprintf("192.0.2.%d", i+1)); err == nil {
				t.Fatal("a wrong password was accepted")
			}
		}
		var blocked *BlockedError
		if err := wrong(t, service, "198.51.100.1"); !errors.As(err, &blocked) {
			t.Errorf("a new address's first wrong password under attack: err = %v, want the block", err)
		}
		if _, _, err := service.Login(ctx, "203.0.113.7", password); err != nil {
			t.Errorf("the password holder from an address that has not failed: %v", err)
		}
		time.Sleep(FailureWindow + time.Minute)
		if err := wrong(t, service, "198.51.100.2"); !errors.Is(err, ErrBadPassword) {
			t.Errorf("an hour later the limit is back to normal: err = %v", err)
		}
	})
}

func TestSessionEndsAfterTwelveHoursOrAnHourIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		ctx := context.Background()

		idle, _, err := service.Login(ctx, "10.0.0.1", password)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(SessionIdle - time.Second)
		if _, err := service.Authenticate(ctx, idle); err != nil {
			t.Fatalf("just under the idle limit: %v", err)
		}
		time.Sleep(SessionIdle - time.Second) // the use above counts: still not idle for an hour
		if _, err := service.Authenticate(ctx, idle); err != nil {
			t.Fatalf("use renews the idle time: %v", err)
		}
		time.Sleep(SessionIdle + time.Second)
		if _, err := service.Authenticate(ctx, idle); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("idle for an hour: err = %v", err)
		}
		var left int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, hashToken(idle)).Scan(&left); err != nil || left != 0 {
			t.Errorf("the ended session is still stored (%d, %v)", left, err)
		}

		busy, _, err := service.Login(ctx, "10.0.0.1", password)
		if err != nil {
			t.Fatal(err)
		}
		for elapsed := time.Duration(0); elapsed < SessionLifetime-30*time.Minute; elapsed += 30 * time.Minute {
			time.Sleep(30 * time.Minute)
			if _, err := service.Authenticate(ctx, busy); err != nil {
				t.Fatalf("after %v of steady use: %v", elapsed+30*time.Minute, err)
			}
		}
		time.Sleep(31 * time.Minute) // past twelve hours in all, used every half hour
		if _, err := service.Authenticate(ctx, busy); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("past twelve hours: err = %v", err)
		}
	})
}

func TestLogoutEndsOnlyThatSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		ctx := context.Background()
		first, _, _ := service.Login(ctx, "10.0.0.1", password)
		second, _, _ := service.Login(ctx, "10.0.0.2", password)
		if err := service.Logout(ctx, first); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Authenticate(ctx, first); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("first after logout: %v", err)
		}
		if _, err := service.Authenticate(ctx, second); err != nil {
			t.Errorf("second: %v", err)
		}
		if err := service.Logout(ctx, "not a session"); err != nil {
			t.Errorf("logout of nothing: %v", err)
		}
	})
}

func TestChangePasswordNeedsTheOldOneAndEndsEverySession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		ctx := context.Background()
		token, _, _ := service.Login(ctx, "10.0.0.1", password)
		const newer = "a different long phrase"

		if err := service.ChangePassword(ctx, "10.0.0.1", "wrong old password", newer); !errors.Is(err, ErrBadPassword) {
			t.Errorf("wrong old password: err = %v", err)
		}
		if err := service.ChangePassword(ctx, "10.0.0.1", password, "short"); err == nil {
			t.Error("a short new password was accepted")
		}
		if _, err := service.Authenticate(ctx, token); err != nil {
			t.Fatalf("a refused change ended the session: %v", err)
		}
		if err := service.ChangePassword(ctx, "10.0.0.1", password, newer); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("the session survived a password change: %v", err)
		}
		if _, _, err := service.Login(ctx, "10.0.0.1", password); !errors.Is(err, ErrBadPassword) {
			t.Errorf("the old password still works: %v", err)
		}
		if _, _, err := service.Login(ctx, "10.0.0.1", newer); err != nil {
			t.Errorf("the new password: %v", err)
		}
	})
}

// Changing the password must not be a way to guess it without the limit.
func TestGuessingTheOldPasswordThroughChangePasswordIsLimitedToo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		var blocked *BlockedError
		var err error
		for range MaxFailures {
			err = service.ChangePassword(context.Background(), "10.0.0.1", "guess guess guess", "a different long phrase")
		}
		if !errors.As(err, &blocked) {
			t.Errorf("err = %v, want the block after %d guesses", err, MaxFailures)
		}
	})
}

func TestSetPasswordEndsSessionsAndRefusesAShortOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _ := newService(t)
		ctx := context.Background()
		token, _, _ := service.Login(ctx, "10.0.0.1", password)
		if err := service.SetPassword(ctx, "short"); err == nil {
			t.Error("a short password was set")
		}
		if err := service.SetPassword(ctx, "recovered with a new one"); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("session after SetPassword: %v", err)
		}
		if ok, err := service.Initialized(ctx); err != nil || !ok {
			t.Errorf("Initialized = %v, %v", ok, err)
		}
	})
}

func TestAnOlderHashIsReplacedAtSignIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		old, _ := hashWith(password, 500, []byte("0123456789abcdef"))
		if _, err := st.DB.Exec(`UPDATE login SET password_hash = ?`, old); err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.Login(context.Background(), "10.0.0.1", password); err != nil {
			t.Fatal(err)
		}
		var now string
		_ = st.DB.QueryRow(`SELECT password_hash FROM login`).Scan(&now)
		if now == old || !strings.HasPrefix(now, "pbkdf2-sha256$1000$") {
			t.Errorf("the hash was not renewed: %q", now)
		}
	})
}

func TestPruneDeletesWhatHasEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		ctx := context.Background()
		_, _, _ = service.Login(ctx, "10.0.0.1", password)
		_ = wrong(t, service, "10.0.0.2")
		time.Sleep(SessionLifetime + FailureWindow)
		_, _, _ = service.Login(ctx, "10.0.0.3", password) // a live session
		if err := service.Prune(ctx); err != nil {
			t.Fatal(err)
		}
		var sessions, failures int
		_ = st.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions)
		_ = st.DB.QueryRow(`SELECT COUNT(*) FROM login_failures`).Scan(&failures)
		if sessions != 1 || failures != 0 {
			t.Errorf("sessions = %d, failures = %d; want 1 and 0", sessions, failures)
		}
	})
}

func TestEverySignInAttemptIsAuditedAndTheChainHolds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		ctx := context.Background()
		_ = wrong(t, service, "10.0.0.2")
		_, session, _ := service.Login(ctx, "10.0.0.1", password)

		records, err := audit.List(ctx, st.DB, 20)
		if err != nil {
			t.Fatal(err)
		}
		var actions []string
		for _, record := range records {
			actions = append(actions, record.Entry.Action)
			switch record.Entry.Action {
			case "login.ok":
				if record.Actor.Name != "ctl" || record.Actor.Session != session.ID || record.Actor.IP != "10.0.0.1" {
					t.Errorf("login.ok actor = %+v", record.Actor)
				}
			case "login.fail":
				if record.Actor.IP != "10.0.0.2" {
					t.Errorf("login.fail actor = %+v", record.Actor)
				}
			}
			if strings.Contains(record.Entry.Detail, password) {
				t.Errorf("an audit entry holds the password: %+v", record)
			}
		}
		if got := strings.Join(actions, " "); got != "login.ok login.fail password.set" {
			t.Errorf("actions = %q", got)
		}
		if checked, err := audit.Verify(ctx, st.DB); err != nil || checked != 3 {
			t.Errorf("Verify = %d, %v", checked, err)
		}
	})
}

// Wrong passwords that arrive together must not all be let in before the first failure is counted.
func TestAddressThatSendsManyTriesAtOnceStillGetsOnlyItsTries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		const sent = 20
		answers := make(chan error, sent)
		for range sent {
			go func() { answers <- wrong(t, service, "10.0.0.9") }()
		}
		var checked, blocked int
		for range sent {
			var block *BlockedError
			switch err := <-answers; {
			case errors.As(err, &block):
				blocked++
			case errors.Is(err, ErrBadPassword):
				checked++
			default:
				t.Errorf("unexpected answer: %v", err)
			}
		}
		// The last try that is let in is answered with the block too, so MaxFailures tries produce MaxFailures-1 plain answers.
		if checked != MaxFailures-1 || blocked != sent-checked {
			t.Errorf("%d plain answers and %d blocks out of %d tries, want %d plain", checked, blocked, sent, MaxFailures-1)
		}
		var fails int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'login.fail'`).Scan(&fails); err != nil || fails != MaxFailures {
			t.Errorf("%d failures audited (err %v), want %d", fails, err, MaxFailures)
		}
		var block *BlockedError
		if _, _, err := service.Login(context.Background(), "10.0.0.9", password); !errors.As(err, &block) {
			t.Errorf("the right password after the burst: err = %v, want the block", err)
		}
	})
}

// A sign-in that hashed the old password must not put its older hash back over a new password.
func TestRehashDoesNotUndoAPasswordChangedMeanwhile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		ctx := context.Background()
		old, _ := hashWith(password, 500, []byte("0123456789abcdef"))
		if err := service.SetPassword(ctx, "another long password"); err != nil {
			t.Fatal(err)
		}
		var changed string
		_ = st.DB.QueryRow(`SELECT password_hash FROM login`).Scan(&changed)
		service.rehash(ctx, old, password)
		var after string
		_ = st.DB.QueryRow(`SELECT password_hash FROM login`).Scan(&after)
		if after != changed {
			t.Error("rehash replaced a password that was changed after the one it checked")
		}
	})
}

// Behind an SSH tunnel every client has one address, so anybody can use up its tries. The person at the
// machine must be able to lift the block, with the password reset or without it.
func TestAResetOfThePasswordAndUnblockLiftTheBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, st := newService(t)
		ctx := context.Background()
		block := func() {
			for range MaxFailures {
				_ = wrong(t, service, "127.0.0.1")
			}
			var blocked *BlockedError
			if _, _, err := service.Login(ctx, "127.0.0.1", password); !errors.As(err, &blocked) {
				t.Fatalf("not blocked: %v", err)
			}
		}
		block()
		if err := service.Unblock(ctx); err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.Login(ctx, "127.0.0.1", password); err != nil {
			t.Errorf("after unblock: %v", err)
		}
		block()
		if err := service.SetPassword(ctx, "a new long password"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.Login(ctx, "127.0.0.1", "a new long password"); err != nil {
			t.Errorf("after a reset of the password: %v", err)
		}
		var unblocks int
		_ = st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'login.unblock'`).Scan(&unblocks)
		if unblocks != 1 {
			t.Errorf("%d login.unblock entries, want 1", unblocks)
		}
	})
}
