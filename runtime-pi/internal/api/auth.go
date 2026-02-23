package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

const cookieName = "phb_session"

func (s *Server) newSession() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)

	s.sessMu.Lock()
	s.sessions[id] = time.Now().Add(s.sessionTTL)
	s.sessMu.Unlock()
	return id
}

func (s *Server) validSession(id string) bool {
	if id == "" {
		return false
	}
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	exp, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, id)
		return false
	}
	return true
}

func (s *Server) dropSession(id string) {
	if id == "" {
		return
	}
	s.sessMu.Lock()
	delete(s.sessions, id)
	s.sessMu.Unlock()
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	// If PIN is disabled, everything is open.
	if s.pin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow safe methods without auth
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// BUT: /api/auth/* are always allowed (login/logout/status)
			next.ServeHTTP(w, r)
			return
		}
		// Allow auth endpoints (login/logout) without a session
		if len(r.URL.Path) >= 9 && r.URL.Path[:9] == "/api/auth" {
			next.ServeHTTP(w, r)
			return
		}
		// Check cookie
		c, err := r.Cookie(cookieName)
		if err != nil || !s.validSession(c.Value) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	type out struct {
		Enabled    bool  `json:"enabled"`
		Authorized bool  `json:"authorized"`
		ExpiresAt  int64 `json:"expires_at,omitempty"`
	}
	res := out{Enabled: s.pin != ""}

	if s.pin != "" {
		if c, err := r.Cookie(cookieName); err == nil && s.validSession(c.Value) {
			s.sessMu.Lock()
			exp := s.sessions[c.Value]
			s.sessMu.Unlock()
			res.Authorized = true
			res.ExpiresAt = exp.Unix()
		}
	}
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleAuthPIN(w http.ResponseWriter, r *http.Request) {
	if s.pin == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PIN == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.PIN != s.pin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := s.newSession()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure: true, // enable if you put this behind HTTPS
		Expires: time.Now().Add(s.sessionTTL),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.dropSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}
