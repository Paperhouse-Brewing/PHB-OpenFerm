// SPDX-License-Identifier: Apache-2.0
package sse

import (
	"log"
	"net/http"
	"sync"
)

type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{clients: map[chan []byte]struct{}{}} }

func (h *Hub) Run() {}

func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	for c := range h.clients {
		select {
		case c <- msg:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", 500)
		return
	}
	c := make(chan []byte, 8)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.clients, c); close(c); h.mu.Unlock() }()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-c:
			if _, err := w.Write(append([]byte("data: "), append(msg, []byte("\n\n")...)...)); err != nil {
				log.Printf("sse write: %v", err)
				return
			}
			flusher.Flush()
		}
	}
}
