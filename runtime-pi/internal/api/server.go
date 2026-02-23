// SPDX-License-Identifier: Apache-2.0
package api

import (
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"phb/fermenter-runtime/internal/alarm"
	"phb/fermenter-runtime/internal/control"
	"phb/fermenter-runtime/internal/store"
	"phb/fermenter-runtime/internal/web"
)

type Server struct {
	state           *control.SystemState
	alarms          *alarm.Manager
	store           *store.Store
	tmpl            *template.Template
	static          http.Handler
	prefsMu         sync.RWMutex
	units           string // "C" or "F"
	locale          string // "en", "es", ...
	uiFS            fs.FS
	reloadTemplates bool
	pin             string
	sessionTTL      time.Duration
	sessMu          sync.Mutex
	sessions        map[string]time.Time
}

func NewServer(state *control.SystemState, al *alarm.Manager, st *store.Store) http.Handler {
	s := &Server{
		state:  state,
		alarms: al,
		store:  st,
		units:  "C",
		locale: "en",
	}

	// Read PIN/TLL from env (empty PIN => auth disabled)
	s.pin = os.Getenv("PHB_PIN")
	ttlMin := 10
	if v := os.Getenv("PHB_SESSION_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ttlMin = n
		}
	}
	s.sessionTTL = time.Duration(ttlMin) * time.Minute
	s.sessions = make(map[string]time.Time)

	// Pick embedded UI, unless /var/lib/openbrew/www exists
	uiFS := web.SelectFS()
	s.uiFS = uiFS
	s.reloadTemplates = web.UsingOverride()

	// Parse templates
	t, err := template.ParseFS(uiFS, "templates/index.html")
	if err != nil {
		// ultra-safe fallback to a tiny page if template missing
		t = template.Must(template.New("index").Parse(`<html><body>PHB UI missing</body></html>`))
	}
	s.tmpl = t

	// Static files under /static/
	sub, err := fs.Sub(uiFS, "static")
	if err == nil {
		s.static = http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	} else {
		s.static = http.NotFoundHandler()
	}

	r := chi.NewRouter()

	// Auth endpoints
	r.Get("/api/auth/status", s.handleAuthStatus)
	r.Post("/api/auth/login", s.handleAuthPIN)
	r.Post("/api/auth/logout", s.handleAuthLogout)

	// Public Fermenter Reads
	r.Get("/api/state", s.handleState)
	r.Get("/api/prefs", s.handleGetPrefs)

	// Public History Reads
	r.Get("/api/history/{id}", s.handleHistory)

	// Public Profile Reads
	r.Get("/api/profiles", s.handleListProfiles)
	r.Get("/api/fv/{id}/profile", s.handleGetAssignment)

	// Public Alarm Reads
	r.Get("/api/alarms", s.handleGetAlarm)
	r.Get("/api/alarms/history", s.handleGetAlarmHistory)
	r.Get("/api/events", s.handleEvents)

	// Public Mode Reads
	r.Get("/api/fv/{id}/mode", s.handleGetMode)

	// Static
	r.Get("/static/*", func(w http.ResponseWriter, r *http.Request) { s.static.ServeHTTP(w, r) })

	// UI
	r.Get("/", s.handleIndex)

	// Settings
	r.Get("/api/settings", s.handleGetSettings)

	// Protected writes (PIN required)
	r.Group(func(pr chi.Router) {
		pr.Use(s.requireAuth)
		pr.Post("/api/prefs", s.handleSetPrefs)
		pr.Post("/api/fv/{id}/target", s.handleSetTarget)

		pr.Post("/api/profiles", s.handleCreateProfile)
		pr.Post("/api/fv/{id}/profile/assign", s.handleAssignProfile)
		pr.Post("/api/fv/{id}/profile/cancel", s.handleCancelProfile)
		pr.Post("/api/fv/{id}/profile/pause", s.handlePauseProfile)

		// Alarms
		//pr.Post("/api/alarms/{id}/ack", s.handleGetAlarm)

		// Modes
		pr.Post("/api/fv/{id}/mode", s.handleSetMode)
		pr.Post("/api/fv/{id}/valve", s.handleSetValveOverride)
		pr.Post("/api/fv/{id}/valve/clear", s.handleClearValveOverride)

		// Settings
		pr.Post("/api/settings", s.handleSetSettings)
	})

	return r
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html; charset=utf-8")

	// When editing /var/lib/openbrew/www, re-parse the template on each request
	if s.reloadTemplates {
		t, err := template.ParseFS(s.uiFS, "templates/index.html")
		if err != nil {
			http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = t.Execute(w, map[string]any{"Now": time.Now().Format(time.RFC1123)})
		return
	}

	// Embedded fallback (parsed at startup)
	_ = s.tmpl.Execute(w, map[string]any{"Now": time.Now().Format(time.RFC1123)})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(s.state.Export())
}

func (s *Server) handleGetPrefs(w http.ResponseWriter, r *http.Request) {
	s.prefsMu.RLock()
	resp := map[string]any{
		"units":  s.units, // "C" or "F"
		"locale": s.locale,
		"labelC": "°C",
		"labelF": "°F",
	}
	s.prefsMu.RUnlock()
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSetPrefs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Units  string `json:"units"`  // "C" or "F"
		Locale string `json:"locale"` // "en", "es", ...
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.prefsMu.Lock()
	if body.Units != "" {
		u := strings.ToUpper(body.Units)
		if u == "C" || u == "F" {
			s.units = u
		}
	}
	if body.Locale != "" {
		s.locale = body.Locale
	}
	s.prefsMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		TargetC float64 `json:"target_c"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.TargetC < -20 || body.TargetC > 40 {
		http.Error(w, "target out of range", http.StatusBadRequest)
		return
	}
	if err := s.state.SetTarget(id, body.TargetC); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
