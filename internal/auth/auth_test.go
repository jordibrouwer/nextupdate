package auth

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.UnixMilli(1_700_000_000_000)
	clock := func() time.Time { return now }
	return &Service{Store: st, Now: clock, SessionTTL: time.Hour, Cost: bcrypt.MinCost, Limiter: NewLimiter(5, 15*time.Minute, clock)}, st, &now
}

func TestSetupOnlyOnce(t *testing.T) {
	s, _, _ := newService(t)
	if need, err := s.NeedsSetup(); err != nil || !need {
		t.Fatalf("need %v %v", need, err)
	}
	if err := s.Setup("jordi", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("want ErrWeakPassword, got %v", err)
	}
	if err := s.Setup("jordi", "long enough password"); err != nil {
		t.Fatal(err)
	}
	if need, _ := s.NeedsSetup(); need {
		t.Fatal("setup still needed")
	}
	if err := s.Setup("other", "another long password"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("want ErrSetupDone, got %v", err)
	}
}

func TestLoginAuthenticateLogout(t *testing.T) {
	s, st, _ := newService(t)
	s.Setup("jordi", "long enough password")

	if _, err := s.Login("jordi", "wrong password!!", "1.2.3.4"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("bad password: %v", err)
	}
	if _, err := s.Login("nobody", "long enough password", "1.2.3.4"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("unknown user: %v", err)
	}
	token, err := s.Login("JORDI", "long enough password", "1.2.3.4")
	if err != nil || len(token) < 40 {
		t.Fatalf("login: %q %v", token, err)
	}
	if _, err := st.SessionUser(token, s.Now()); err == nil {
		t.Fatal("the raw token must not be stored; only its hash")
	}
	uid, err := s.Authenticate(token)
	if err != nil || uid == 0 {
		t.Fatalf("authenticate: %d %v", uid, err)
	}
	if err := s.Logout(token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after logout: %v", err)
	}
	if _, err := s.Authenticate(""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("empty token: %v", err)
	}
}

func TestSessionExpires(t *testing.T) {
	s, _, now := newService(t)
	s.Setup("jordi", "long enough password")
	token, _ := s.Login("jordi", "long enough password", "ip")
	*now = now.Add(2 * time.Hour)
	if _, err := s.Authenticate(token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired session accepted: %v", err)
	}
}

func TestLoginLocksAfterFiveFailures(t *testing.T) {
	s, _, now := newService(t)
	s.Setup("jordi", "long enough password")
	for i := 0; i < 5; i++ {
		if _, err := s.Login("jordi", "wrong password!!", "9.9.9.9"); !errors.Is(err, ErrBadLogin) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Login("jordi", "long enough password", "9.9.9.9"); !errors.Is(err, ErrLocked) {
		t.Fatalf("even the right password must be refused while locked: %v", err)
	}
	if _, err := s.Login("jordi", "long enough password", "8.8.8.8"); err != nil {
		t.Fatalf("another address is not locked: %v", err)
	}
	*now = now.Add(16 * time.Minute)
	if _, err := s.Login("jordi", "long enough password", "9.9.9.9"); err != nil {
		t.Fatalf("lock must end after the window: %v", err)
	}
}
