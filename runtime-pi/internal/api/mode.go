package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"phb/fermenter-runtime/internal/control"

	"github.com/go-chi/chi/v5"
)

// GET /api/fv/{id}/mode  -> returns mode + override + current target
func (s *Server) handleGetMode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	type resp struct {
		Mode     string  `json:"mode"`
		TargetC  float64 `json:"target_c"`
		Override *struct {
			State   string `json:"state"`
			Expires int64  `json:"expires"` // 0 means indefinite
		} `json:"override,omitempty"`
	}

	var out resp
	found := false
	now := time.Now()

	s.state.ForEach(func(f *control.Fermenter) {
		if found || f.ID != id {
			return
		}
		out.Mode = string(f.Mode)
		out.TargetC = f.TargetC
		if f.Override.State != "" && (f.Override.Until.IsZero() || now.Before(f.Override.Until)) {
			exp := int64(0)
			if !f.Override.Until.IsZero() {
				exp = f.Override.Until.Unix()
			}
			out.Override = &struct {
				State   string `json:"state"`
				Expires int64  `json:"expires"`
			}{State: f.Override.State, Expires: exp}
		}
		found = true
	})

	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// POST /api/fv/{id}/mode  {mode: "profile"|"fixed"|"valve", target_c?:float}
func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var body struct {
		Mode    string  `json:"mode"`
		TargetC float64 `json:"target_c"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	m := control.ControlMode(body.Mode)
	if m != control.ModeProfile && m != control.ModeFixed && m != control.ModeValve {
		http.Error(w, "bad mode", http.StatusBadRequest)
		return
	}

	if err := s.state.SetMode(id, m); err != nil {
		http.NotFound(w, r)
		return
	}
	_ = s.store.SaveMode(id, string(m))

	if m == control.ModeFixed && body.TargetC != 0 {
		_ = s.state.SetTarget(id, body.TargetC)
		// target persistence already handled by OnTargetChange hook
	}
	// --- DEBUG: log URL params/query/remote ---
	log.Printf("handleSetMode: id=%q mode=%q", id, body.Mode)

	s.store.AddEvent("mode_change", id, body.Mode)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/fv/{id}/valve
// body: {"state":"open"|"closed","for_s":<seconds,optional>,"until_s":<unix,optional>}
// If neither for_s nor until_s provided => indefinite override.
func (s *Server) handleSetValveOverride(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var body struct {
		State  string `json:"state"`             // required: "open"|"closed"
		ForS   int64  `json:"for_s,omitempty"`   // optional: duration (seconds)
		UntilS int64  `json:"until_s,omitempty"` // optional: absolute unix seconds
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.State != "open" && body.State != "closed" {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	// Compute until (zero => indefinite)
	var until time.Time
	switch {
	case body.UntilS > 0:
		until = time.Unix(body.UntilS, 0)
	case body.ForS > 0:
		until = time.Now().Add(time.Duration(body.ForS) * time.Second)
	default:
		// leave until = zero time => indefinite
	}

	if err := s.state.SetValveOverrideUntil(id, body.State, until); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Event logging for charts/audit
	if until.IsZero() {
		s.store.AddEvent("override_set", id, `{"state":"`+body.State+`","until":0}`)
	} else {
		s.store.AddEvent("override_set", id, `{"state":"`+body.State+`","until":`+strconv.FormatInt(until.Unix(), 10)+`}`)
	}

	w.WriteHeader(http.StatusNoContent)
}

// POST /api/fv/{id}/valve/clear
func (s *Server) handleClearValveOverride(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.state.ClearOverride(id) // your existing helper
	s.store.AddEvent("override_clear", id, "")
	w.WriteHeader(http.StatusNoContent)
}
