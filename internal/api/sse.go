package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// HeartbeatInterval keeps idle SSE connections alive through proxies.
const HeartbeatInterval = 15 * time.Second

// MaxSSEClients bounds concurrent event streams (FR-SEC-05).
const MaxSSEClients = 100

// handleEvents streams scan and share events to the UI (§9.4).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.bus == nil {
		writeError(w, http.StatusServiceUnavailable, "events_unavailable", "the event stream is not running", nil)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "this server cannot stream events", nil)
		return
	}
	if s.bus.Subscribers() >= MaxSSEClients {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "too_many_streams",
			fmt.Sprintf("at most %d event streams are served at once", MaxSSEClients), nil)
		return
	}

	// Last-Event-ID (or the ?last_event_id= fallback for EventSource
	// polyfills) replays what the client missed while reconnecting.
	lastID, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)
	if lastID == 0 {
		lastID, _ = strconv.ParseUint(r.URL.Query().Get("last_event_id"), 10, 64)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // ask nginx not to buffer the stream
	w.WriteHeader(http.StatusOK)

	sub := s.bus.Subscribe(lastID)
	defer sub.Close()

	// An initial comment makes the connection usable immediately.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-sub.C:
			if !open {
				// The subscriber fell behind and was dropped; ask the client
				// to reconnect, which replays from its Last-Event-ID.
				fmt.Fprintf(w, "event: reconnect\ndata: {\"reason\":\"slow_consumer\"}\n\n")
				flusher.Flush()
				return
			}
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", ev.IDString(), ev.Type, data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
