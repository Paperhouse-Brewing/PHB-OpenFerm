package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"phb/fermenter-runtime/internal/store"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.LoadControllerSettings()
	if err != nil {
		http.Error(w, "load settings", http.StatusInternalServerError)
		return
	}

	// If DB doesn’t have overrides, reflect live values from state
	snap := s.state.SnapshotControllerSettings()
	if cs.BandC == 0 {
		cs.BandC = snap.BandC
	}
	if cs.MinChangeS == 0 {
		cs.MinChangeS = snap.MinChangeS
	}
	if cs.MaxOpen == 0 {
		cs.MaxOpen = snap.MaxOpen
	} // 0 means unlimited is fine

	_ = json.NewEncoder(w).Encode(cs)
}

func (s *Server) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BandC      float64 `json:"band_c"`
		MinChangeS int     `json:"min_change_s"`
		MaxOpen    int     `json:"max_open"` // 0 = unlimited
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// persist
	if err := s.store.SaveControllerSettings(store.ControllerSettings{
		BandC: body.BandC, MinChangeS: body.MinChangeS, MaxOpen: body.MaxOpen,
	}); err != nil {
		http.Error(w, "save settings", http.StatusInternalServerError)
		return
	}
	// apply live
	s.state.ApplyControllerSettings(body.BandC, body.MinChangeS, body.MaxOpen)

	// audit
	s.store.AddEvent("settings_update", "",
		fmt.Sprintf("band=%.3f,min_change_s=%d,max_open=%d", body.BandC, body.MinChangeS, body.MaxOpen))

	w.WriteHeader(http.StatusNoContent)
}
