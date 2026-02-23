// SPDX-License-Identifier: Apache-2.0
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleGetAlarmHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("limit")
	limit, _ := strconv.Atoi(q)
	h, err := s.store.ListEvents(limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(h)
}

func (s *Server) handleGetAlarm(w http.ResponseWriter, r *http.Request) {
	a := s.alarms.Active()
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

func (s *Server) handleSetAlarm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" || !s.alarms.Ack(id) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
