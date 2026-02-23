package api

import (
	"encoding/json"
	"net/http"
	"time"

	"phb/fermenter-runtime/internal/control"
	"phb/fermenter-runtime/internal/store"

	"github.com/go-chi/chi/v5"

	"math"
	"regexp"
	"strconv"
	"strings"
)

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProfiles()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Name string            `json:"name"`
		Spec store.ProfileSpec `json:"spec"`
	}
	var body req
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if len(body.Spec.Steps) == 0 {
		http.Error(w, "spec.steps is empty", http.StatusBadRequest)
		return
	}

	id, err := s.store.CreateProfile(body.Name, body.Spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
}

var durRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)([smhdw])`)

func parseHumanDuration(s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	var total float64
	for _, m := range durRe.FindAllStringSubmatch(s, -1) {
		val, _ := strconv.ParseFloat(m[1], 64)
		switch strings.ToLower(m[2]) {
		case "s":
			total += val
		case "m":
			total += val * 60
		case "h":
			total += val * 3600
		case "d":
			total += val * 86400
		case "w":
			total += val * 604800
		}
	}
	return int64(math.Round(total)), nil
}

// GET /api/fv/{id}/profile  -> 204 if none, else JSON
func (s *Server) handleGetAssignment(w http.ResponseWriter, r *http.Request) {
	fvID := chi.URLParam(r, "id")
	if fvID == "" {
		http.Error(w, "missing fv id", http.StatusBadRequest)
		return
	}

	a, err := s.store.GetAssignment(fvID)
	if err != nil || a == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Try to enrich with profile name
	name := ""
	if list, err := s.store.ListProfiles(); err == nil {
		for _, p := range list {
			if p.ID == a.ProfileID {
				name = p.Name
				break
			}
		}
	}

	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"fv":              a.FVID,
		"profile_id":      a.ProfileID,
		"name":            name,
		"step_idx":        a.StepIdx,
		"step_started_ts": a.StepStartedTs,
		"paused":          a.Paused,
		"from_c":          a.FromC,
	})
}

type assignBody struct {
	ProfileID int64    `json:"profile_id"`
	FromC     *float64 `json:"from_c,omitempty"` // optional override
}

// POST /api/fv/{id}/profile/assign
// Body: {"profile_id": 2, "from_c": 18.0}  (from_c optional)
// Also accepts ?profile_id=2 as a fallback.
func (s *Server) handleAssignProfile(w http.ResponseWriter, r *http.Request) {
	fvID := chi.URLParam(r, "id")
	if fvID == "" {
		http.Error(w, "missing fv id", http.StatusBadRequest)
		return
	}

	var body assignBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ProfileID == 0 {
		if q := r.URL.Query().Get("profile_id"); q != "" {
			if v, err := strconv.ParseInt(q, 10, 64); err == nil {
				body.ProfileID = v
			}
		}
	}
	if body.ProfileID == 0 {
		http.Error(w, "missing profile_id", http.StatusBadRequest)
		return
	}

	// Ensure profile exists
	if _, err := s.store.GetProfile(body.ProfileID); err != nil {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}

	// Determine FromC baseline (defaults to current FV target)
	fromC := 0.0
	if body.FromC != nil {
		fromC = *body.FromC
	} else {
		found := false
		s.state.ForEach(func(ff *control.Fermenter) {
			if ff.ID == fvID {
				fromC = ff.TargetC
				found = true
			}
		})
		if !found {
			http.Error(w, "fv not found", http.StatusNotFound)
			return
		}
	}

	a := store.Assignment{
		FVID:          fvID,
		ProfileID:     body.ProfileID,
		StepIdx:       0,
		StepStartedTs: time.Now().Unix(),
		Paused:        false,
		FromC:         fromC,
	}
	if err := s.store.UpsertAssignment(a); err != nil {
		http.Error(w, "assign failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/fv/{id}/profile/pause?paused=true|false
func (s *Server) handlePauseProfile(w http.ResponseWriter, r *http.Request) {
	fvID := chi.URLParam(r, "id")
	if fvID == "" {
		http.Error(w, "missing fv id", http.StatusBadRequest)
		return
	}

	a, err := s.store.GetAssignment(fvID)
	if err != nil || a == nil {
		http.Error(w, "no assignment", http.StatusNotFound)
		return
	}

	paused := false
	if q := r.URL.Query().Get("paused"); q != "" {
		paused = (q == "1" || q == "true" || q == "TRUE")
	} else {
		// toggle if not provided
		paused = !a.Paused
	}
	a.Paused = paused

	if err := s.store.UpsertAssignment(*a); err != nil {
		http.Error(w, "pause failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/fv/{id}/profile/cancel
func (s *Server) handleCancelProfile(w http.ResponseWriter, r *http.Request) {
	fvID := chi.URLParam(r, "id")
	if fvID == "" {
		http.Error(w, "missing fv id", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteAssignment(fvID); err != nil {
		http.Error(w, "cancel failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
