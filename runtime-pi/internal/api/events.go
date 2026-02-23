// SPDX-License-Identifier: Apache-2.0
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type eventsResponse struct {
	Events []struct {
		T    int64  `json:"t"`
		Type string `json:"type"`
		FVID string `json:"fv_id"`
		Data string `json:"data"`
	} `json:"events"`
}

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
	ev, err := s.store.QueryEvents(fv, fromS, toS)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var out eventsResponse
	for _, e := range ev {
		out.Events = append(out.Events, struct {
			T    int64  `json:"t"`
			Type string `json:"type"`
			FVID string `json:"fv_id"`
			Data string `json:"data"`
		}{T: e.T, Type: e.Type, FVID: e.FVID, Data: e.Data})
	}

	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
	//_ = json.NewEncoder(w).Encode(map[string]any{"events": ev})
}
