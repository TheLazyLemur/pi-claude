package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// frame is one server-sent event: a named channel and an HTML fragment.
type frame struct {
	Event string
	HTML  string
}

// Hub fans server-sent events out to every open browser tab.
type Hub struct {
	mu      sync.Mutex
	clients map[chan frame]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: make(map[chan frame]struct{})}
}

func (h *Hub) subscribe() chan frame {
	ch := make(chan frame, 256)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan frame) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// Send pushes a fragment to every tab. A tab that cannot keep up loses the
// frame rather than stalling the agent, and a frame sent while no tab is
// connected is simply gone. That is the right trade for a local tool watched by
// one person: the agent must never block on a browser. A shared deployment
// would want the transcript kept server-side and replayed on connect.
func (h *Hub) Send(event, html string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- frame{Event: event, HTML: html}:
		default:
		}
	}
}

// ServeHTTP streams frames to one tab until it goes away.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case f := <-ch:
			fmt.Fprintf(w, "event: %s\n", f.Event)
			// Every line of an SSE payload needs its own data: prefix.
			for _, line := range strings.Split(f.HTML, "\n") {
				fmt.Fprintf(w, "data: %s\n", line)
			}
			fmt.Fprint(w, "\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
