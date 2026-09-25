// Package auth handles the single admin login and its sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

var (
	ErrSetupDone    = errors.New("an admin account already exists")
	ErrWeakPassword = errors.New("the password must be at least 10 characters")
	ErrBadLogin     = errors.New("wrong name or password")
	ErrLocked       = errors.New("too many failed logins, try again later")
	ErrNoSession    = errors.New("not signed in")
)

const minPassword = 10

type Service struct {
	Store      *store.Store
	Now        func() time.Time
	SessionTTL time.Duration
	Cost       int // bcrypt cost; 0 = default
	Limiter    *Limiter
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) NeedsSetup() (bool, error) {
	n, err := s.Store.CountUsers()
	return n == 0, err
}

func (s *Service) Setup(name, password string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("enter a name")
	}
	if len(password) < minPassword {
		return ErrWeakPassword
	}
	need, err := s.NeedsSetup()
	if err != nil {
		return err
	}
	if !need {
		return ErrSetupDone
	}
	cost := s.Cost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return err
	}
	_, err = s.Store.CreateUser(name, string(hash))
	return err
}

// dummyHash makes a login for an unknown name cost as much as a real one.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy password"), bcrypt.MinCost)

func (s *Service) Login(name, password, remote string) (string, error) {
	if s.Limiter != nil && !s.Limiter.Allow(remote) {
		return "", ErrLocked
	}
	u, err := s.Store.GetUserByName(strings.TrimSpace(name))
	hash := dummyHash
	if err == nil {
		hash = []byte(u.PasswordHash)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || err != nil {
		if s.Limiter != nil {
			s.Limiter.Fail(remote)
		}
		return "", ErrBadLogin
	}
	if s.Limiter != nil {
		s.Limiter.Reset(remote)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	ttl := s.SessionTTL
	if ttl == 0 {
		ttl = 30 * 24 * time.Hour
	}
	if err := s.Store.CreateSession(hashToken(token), u.ID, s.now().Add(ttl)); err != nil {
		return "", err
	}
	_ = s.Store.DeleteExpiredSessions(s.now())
	return token, nil
}

func (s *Service) Authenticate(token string) (int64, error) {
	if token == "" {
		return 0, ErrNoSession
	}
	uid, err := s.Store.SessionUser(hashToken(token), s.now())
	if errors.Is(err, store.ErrNotFound) {
		return 0, ErrNoSession
	}
	return uid, err
}

func (s *Service) Logout(token string) error {
	if token == "" {
		return nil
	}
	return s.Store.DeleteSession(hashToken(token))
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Limiter counts failures per key inside a window.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time
	mu     sync.Mutex
	fails  map[string]*failure
}

type failure struct {
	count int
	first time.Time
}

func NewLimiter(max int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{max: max, window: window, now: now, fails: map[string]*failure{}}
}

// Allow reports whether the key may try again.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[key]
	if !ok {
		return true
	}
	if l.now().Sub(f.first) >= l.window {
		delete(l.fails, key)
		return true
	}
	return f.count < l.max
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[key]
	if !ok || l.now().Sub(f.first) >= l.window {
		l.fails[key] = &failure{count: 1, first: l.now()}
		return
	}
	f.count++
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
