// SPDX-License-Identifier: Apache-2.0
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	q := r.URL.Query()

	// defaults: last 6h, 60s buckets
	to := time.Now()
	from := to.Add(-6 * time.Hour)
	step := time.Minute

	if v := q.Get("from"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = time.Unix(sec, 0)
		}
	}
	if v := q.Get("to"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			to = time.Unix(sec, 0)
		}
	}
	if v := q.Get("step"); v != "" {
		if s, err := time.ParseDuration(v); err == nil && s > 0 {
			step = s
		}
	}

	series, err := s.store.QuerySeries(id, from, to, step)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"fv_id": id,
		"from":  from.Unix(),
		"to":    to.Unix(),
		"step":  int(step.Seconds()),
		"data":  series,
	})
}

/*
// GET /api/events?fv={id}&from={unix}&to={unix}
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fv := r.URL.Query().Get("fv")
	fromS, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	toS, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if toS == 0 {
		toS = time.Now().Unix()
	}
	if fromS == 0 {
		fromS = toS - 6*3600
	}
	evs, err := s.store.QueryEvents(fv, fromS, toS)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": evs})
}
*/
