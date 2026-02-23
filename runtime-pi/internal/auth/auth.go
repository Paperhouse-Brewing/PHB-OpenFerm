// SPDX-License-Identifier: Apache-2.0
package auth

import (
	cryptoRand "crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrLocked = errors.New("locked")
	ErrBadPIN = errors.New("bad pin")
)

type Manager struct {
	mu       sync.Mutex
	pin      string
	defTTL   time.Duration        // default session length
	sessions map[string]time.Time // token -> expiry
	block    map[string]time.Time // ip -> blocked until
	fails    map[string]int       // ip -> fail count
}

// If pin == "" the lock is disabled (everything allowed).
func NewManager(pin string, defTTL time.Duration) *Manager {
	if defTTL <= 0 {
		defTTL = 10 * time.Minute
	}
	return &Manager{
		pin:      pin,
		defTTL:   defTTL,
		sessions: make(map[string]time.Time),
		block:    make(map[string]time.Time),
		fails:    make(map[string]int),
	}
}

func clientIP(r *http.Request) string {
	// Best effort
	ip := r.Header.Get("X-Forwarded-For")
	if ip != "" {
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		return r.RemoteAddr
	}
	return host
}

func (m *Manager) Unlock(pin string, minutes int, r *http.Request) (string, time.Time, error) {
	// No PIN configured => always unlocked
	if m.pin == "" {
		tok := "unlocked"
		return tok, time.Now().Add(24 * time.Hour), nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	ip := clientIP(r)
	if until, ok := m.block[ip]; ok && time.Now().Before(until) {
		return "", time.Time{}, ErrLocked
	}
	if pin != m.pin {
		m.fails[ip]++
		if m.fails[ip] >= 5 {
			m.block[ip] = time.Now().Add(2 * time.Minute)
			m.fails[ip] = 0
		}
		return "", time.Time{}, ErrBadPIN
	}
	m.fails[ip] = 0

	ttl := m.defTTL
	if minutes > 0 {
		req := time.Duration(minutes) * time.Minute
		// clamp to sane max (2h)
		if req > 2*time.Hour {
			req = 2 * time.Hour
		}
		ttl = req
	}

	var buf [32]byte
	_, _ = cryptoRand.Read(buf[:])
	token := base64.RawURLEncoding.EncodeToString(buf[:])
	exp := time.Now().Add(ttl)
	m.sessions[token] = exp
	return token, exp, nil
}

func (m *Manager) Valid(token string) (bool, time.Time) {
	if token == "" {
		// no PIN configured => unlocked
		if m.pin == "" {
			return true, time.Now().Add(24 * time.Hour)
		}
		return false, time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.sessions[token]
	if !ok || time.Now().After(exp) {
		return false, time.Time{}
	}
	return true, exp
}

func (m *Manager) Lock(token string) {
	if token == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *Manager) extractToken(r *http.Request) string {
	if v := r.Header.Get("X-PHB-Session"); v != "" {
		return v
	}
	if c, _ := r.Cookie("phb_session"); c != nil {
		return c.Value
	}
	return ""
}

// Middleware: require a valid session for protected routes.
func (m *Manager) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, _ := m.Valid(m.extractToken(r))
		if !ok {
			http.Error(w, "locked", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// in internal/auth/auth.go
func (m *Manager) ExtractToken(r *http.Request) string { return m.extractToken(r) }
