package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Server serves the embedded SPA at "/" and the live standings over SSE at
// "/events". It holds no domain state: it asks the injected snapshot func for the
// current standings (which reads the persistence projection) and broadcasts a
// fresh snapshot to every connected client whenever Publish is called (the
// consumer calls Publish after a lap actually changes the read-model).
//
// This is the live display, NOT a health endpoint — liveness stays the heartbeat
// touch-file (ADR-0004). There is no HTTP polling: the browser opens one
// EventSource and the server pushes (Q26.1).
type Server struct {
	addr     string
	snapshot func() Snapshot
	log      *slog.Logger
	hub      *hub

	static http.Handler // serves the embedded SPA bundle (nil when not built)
}

// NewServer builds the server. snapshot returns the current standings on demand.
// The embedded SPA filesystem is resolved once at construction.
func NewServer(addr string, snapshot func() Snapshot, log *slog.Logger) *Server {
	s := &Server{addr: addr, snapshot: snapshot, log: log, hub: newHub()}
	if sub, built := bundleFS(); built {
		s.static = http.FileServer(http.FS(sub))
	}
	return s
}

// Handler returns the HTTP routes (exposed for tests via httptest).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Addr: s.addr, Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.log.Info("shutting down http/sse server")
		return srv.Shutdown(shutdownCtx)
	}
}

// Publish re-reads the current standings and pushes them to all connected SSE
// clients. Called by the consumer after the read-model changes.
func (s *Server) Publish() {
	b, err := json.Marshal(s.snapshot())
	if err != nil {
		s.log.Error("failed to marshal standings snapshot for SSE", "error", err.Error())
		return
	}
	s.hub.broadcast(b)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering so events flush immediately

	ch := s.hub.add()
	defer s.hub.remove(ch)

	// Send the current standings immediately on connect.
	if b, err := json.Marshal(s.snapshot()); err == nil {
		writeSSE(w, flusher, b)
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, flusher, b)
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, data []byte) {
	// SSE frame: one data: line, terminated by a blank line.
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
	flusher.Flush()
}

// handleStatic serves the embedded SPA bundle, or a clear 503 when it has not
// been built (Go-only build with just the .gitkeep placeholder).
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.static == nil {
		http.Error(w, "trackside board bundle not built — run `npm --prefix web run build`", http.StatusServiceUnavailable)
		return
	}
	s.static.ServeHTTP(w, r)
}

// hub fans broadcasts out to all connected SSE clients. Each client has a small
// buffered channel; a slow client that can't keep up loses the OLDEST queued
// frame, never the newest (no blocking, no unbounded growth). Keep-latest
// matters since Story 1.8: a session-finished flip may be the LAST push for a
// long time — dropping it would freeze a stale status on a healthy connection.
type hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func newHub() *hub { return &hub{clients: make(map[chan []byte]struct{})} }

func (h *hub) add() chan []byte {
	ch := make(chan []byte, 8)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) remove(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *hub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- b:
		default:
			// Buffer full: evict the oldest frame to make room for this one
			// (each frame is a FULL snapshot, so only the newest matters).
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- b:
			default: // reader raced us and refilled; it is actively draining anyway
			}
		}
	}
}
