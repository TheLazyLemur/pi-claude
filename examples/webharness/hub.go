package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// frame is one server-sent event: a named channel and an HTML fragment.
type frame struct {
	Event string
	HTML  string
}

// Hub fans server-sent events out to every open browser tab.
type Hub struct {
	mu      sync.Mutex
	clients map[chan frame]string // channel -> session it is watching
}

func NewHub() *Hub {
	return &Hub{clients: make(map[chan frame]string)}
}

func (h *Hub) subscribe(session string) chan frame {
	ch := make(chan frame, 256)
	h.mu.Lock()
	h.clients[ch] = session
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
func (h *Hub) Send(session, event, html string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, watching := range h.clients {
		if watching != session {
			continue
		}
		select {
		case ch <- frame{Event: event, HTML: html}:
		default:
		}
	}
}

// ServeHTTP streams one session's frames to one tab until it goes away.
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

	ch := h.subscribe(r.URL.Query().Get("s"))
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

// Watchers reports how many tabs are following a session.
func (h *Hub) Watchers(session string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := 0
	for _, watching := range h.clients {
		if watching == session {
			n++
		}
	}
	return n
}

// waitForWatcher gives the browser a moment to follow a session before its
// first message runs, so the opening turn streams rather than only appearing on
// reload. The transcript is recorded either way, so this is about liveness, not
// correctness.
func waitForWatcher(h *Hub, session string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.Watchers(session) > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}
