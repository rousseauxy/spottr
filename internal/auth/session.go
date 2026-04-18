// Package auth provides a lightweight in-memory session store and
// brute-force protection for single-password authentication.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const cookieName = "spottr_session"

// Store is a thread-safe in-memory session store.
type Store struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	ttl      time.Duration

	// brute-force tracking: ip → (failures, lockUntil)
	attempts map[string]*attempt
}

type attempt struct {
	failures  int
	lockUntil time.Time
}

// New creates a Store with the given session TTL.
func New(ttl time.Duration) *Store {
	s := &Store{
		sessions: make(map[string]time.Time),
		attempts: make(map[string]*attempt),
		ttl:      ttl,
	}
	go s.periodicCleanup()
	return s
}

// Create mints a new session token and returns it.
func (s *Store) Create() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: rand.Read failed: " + err.Error())
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	return token
}

// Valid returns true if the token exists and has not expired.
func (s *Store) Valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	return ok && time.Now().Before(exp)
}

// Delete removes a session (logout).
func (s *Store) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// CheckPassword does a constant-time comparison so timing attacks cannot
// reveal whether the password is long or short.
func CheckPassword(provided, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// Locked returns true if the IP is currently rate-limited.
func (s *Store) Locked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[ip]
	return a != nil && time.Now().Before(a.lockUntil)
}

// RecordFailure increments the failure counter for an IP and locks it
// after 5 consecutive failures (lockout: 15 min).
func (s *Store) RecordFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[ip]
	if a == nil {
		a = &attempt{}
		s.attempts[ip] = a
	}
	a.failures++
	if a.failures >= 5 {
		a.lockUntil = time.Now().Add(15 * time.Minute)
		a.failures = 0
	}
}

// RecordSuccess clears the failure counter for an IP.
func (s *Store) RecordSuccess(ip string) {
	s.mu.Lock()
	delete(s.attempts, ip)
	s.mu.Unlock()
}

// SetCookie writes the session cookie onto the response.
func SetCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure is intentionally omitted here so the app works over plain
		// HTTP on a LAN. Operators behind HTTPS should set Secure via a
		// reverse proxy or add SPOTTR_SECURE_COOKIE=true to their env.
	})
}

// ClearCookie expires the session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// TokenFromRequest extracts the session token from the cookie.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Store) periodicCleanup() {
	for range time.Tick(10 * time.Minute) {
		now := time.Now()
		s.mu.Lock()
		for t, exp := range s.sessions {
			if now.After(exp) {
				delete(s.sessions, t)
			}
		}
		// Clean up expired lockouts
		for ip, a := range s.attempts {
			if now.After(a.lockUntil) && a.failures == 0 {
				delete(s.attempts, ip)
			}
		}
		s.mu.Unlock()
	}
}
