// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type ProfileBundle struct {
	Schema    string            `json:"schema"`
	Templates []json.RawMessage `json:"templates"`
	Signature string            `json:"signature,omitempty"`
}

func main() {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	r.Post("/profiles/apply", func(w http.ResponseWriter, r *http.Request) {
		var pb ProfileBundle
		if err := json.NewDecoder(r.Body).Decode(&pb); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		log.Printf("received profile bundle with %d template(s)", len(pb.Templates))
		w.WriteHeader(202)
	})
	log.Println("central: listening on :8081")
	log.Fatal(http.ListenAndServe(":8081", r))
}
