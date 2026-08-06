package web

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"lookout/internal/metrics"
	"lookout/internal/netmgr"
)

const minInterval = 500 * time.Millisecond

// Version is surfaced via /api/health; main sets it at startup.
var Version = "dev"

// Server wires the metrics collector to an HTTP interface: static SPA assets,
// a one-shot JSON endpoint, and an SSE stream fed by a single shared poller.
type Server struct {
	col    *metrics.Collector
	nm     *netmgr.Manager
	assets fs.FS

	termEnabled bool
	termCmd     []string

	intervalNs atomic.Int64 // poll interval, dynamically adjustable
	hub        *hub
}

// EnableTerminal turns on the opt-in web terminal, spawning cmd (argv) per
// connection. Must be called before Handler().
func (s *Server) EnableTerminal(cmd []string) {
	s.termEnabled = true
	s.termCmd = cmd
}

// New builds a Server. assets is the embedded web/ filesystem (rooted so that
// "index.html" resolves at the top level). nm may be nil (NetworkManager
// absent), which hides the network-manager feature.
func New(col *metrics.Collector, nm *netmgr.Manager, interval time.Duration, assets fs.FS) *Server {
	s := &Server{
		col:    col,
		nm:     nm,
		assets: assets,
		hub:    newHub(),
	}
	if interval < minInterval {
		interval = minInterval
	}
	s.intervalNs.Store(int64(interval))
	go s.pollLoop()
	return s
}

// pollLoop is the ONLY goroutine that reads system metrics. It ticks on the
// configured interval and fans the snapshot out to every SSE subscriber, so
// N connected browsers cost the same as one.
func (s *Server) pollLoop() {
	for {
		snap := s.col.Collect()
		if payload, err := json.Marshal(snap); err == nil {
			s.hub.broadcast(payload)
		}
		time.Sleep(time.Duration(s.intervalNs.Load()))
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"ok":       true,
			"version":  Version,
			"docker":   s.col.DockerEnabled(),
			"netmgr":   s.nm.Available(),
			"terminal": s.terminalEnabled(),
			"interval": time.Duration(s.intervalNs.Load()).Milliseconds(),
		})
	})
	mux.HandleFunc("/api/interval", s.handleInterval)

	// NetworkManager endpoints are only registered when NM is present, so the
	// feature (and its UI tab) is fully absent otherwise.
	if s.nm.Available() {
		mux.HandleFunc("/api/nm/state", s.handleNMState)
		mux.HandleFunc("/api/nm/wifi", s.handleNMWifi)
		mux.HandleFunc("/api/nm/connect", s.handleNMConnect)
		mux.HandleFunc("/api/nm/disconnect", s.handleNMDisconnect)
		mux.HandleFunc("/api/nm/forget", s.handleNMForget)
	}

	// Opt-in web terminal (a full remote shell) — only when -terminal was set.
	if s.terminalEnabled() {
		mux.HandleFunc("/api/term/ws", s.handleTermWS)
	}
	// Static SPA assets (index.html, app.js, app.css) from the embedded FS.
	mux.Handle("/", http.FileServer(http.FS(s.assets)))
	return logRequests(mux)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.col.Collect())
}

// handleInterval adjusts the global poll cadence. It is clamped to a sane
// floor so a client can't hammer the box.
func (s *Server) handleInterval(w http.ResponseWriter, r *http.Request) {
	ms, err := strconv.Atoi(r.URL.Query().Get("ms"))
	if err != nil {
		http.Error(w, "bad ms", http.StatusBadRequest)
		return
	}
	d := time.Duration(ms) * time.Millisecond
	if d < minInterval {
		d = minInterval
	}
	s.intervalNs.Store(int64(d))
	writeJSON(w, map[string]any{"interval": d.Milliseconds()})
}

// ---- NetworkManager handlers (registered only when NM is available) ----

func (s *Server) handleNMState(w http.ResponseWriter, r *http.Request) {
	d, err := s.nm.Snapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (s *Server) handleNMWifi(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("on") == "1" || r.FormValue("on") == "true"
	s.nmDo(w, s.nm.SetWireless(on))
}

func (s *Server) handleNMConnect(w http.ResponseWriter, r *http.Request) {
	if uuid := r.FormValue("uuid"); uuid != "" {
		s.nmDo(w, s.nm.ActivateUUID(uuid))
		return
	}
	if ssid := r.FormValue("ssid"); ssid != "" {
		s.nmDo(w, s.nm.ConnectWifi(ssid, r.FormValue("psk")))
		return
	}
	http.Error(w, "uuid or ssid required", http.StatusBadRequest)
}

func (s *Server) handleNMDisconnect(w http.ResponseWriter, r *http.Request) {
	s.nmDo(w, s.nm.Disconnect(r.FormValue("iface")))
}

func (s *Server) handleNMForget(w http.ResponseWriter, r *http.Request) {
	s.nmDo(w, s.nm.DeleteUUID(r.FormValue("uuid")))
}

// nmDo reports the outcome of a NetworkManager action as JSON. NM errors are
// often polkit authorization failures — surfaced verbatim so the UI can show
// what went wrong.
func (s *Server) nmDo(w http.ResponseWriter, err error) {
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx/caddy)

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	// Push the latest snapshot immediately so the UI paints without waiting a
	// whole interval for the next tick.
	if last := s.hub.last(); last != nil {
		writeSSE(w, flusher, last)
	}

	ctx := r.Context()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			writeSSE(w, flusher, msg)
		case <-keepalive.C:
			// SSE comment line keeps intermediaries from timing the conn out.
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, f http.Flusher, data []byte) {
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
	f.Flush()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stream" {
			log.Printf("stream connect %s", r.RemoteAddr)
		}
		next.ServeHTTP(w, r)
	})
}

// ---- hub: fan-out of the latest payload to all SSE subscribers ----

type hub struct {
	mu      sync.RWMutex
	subs    map[chan []byte]struct{}
	lastMsg []byte
}

func newHub() *hub {
	return &hub{subs: map[chan []byte]struct{}{}}
}

func (h *hub) subscribe() chan []byte {
	ch := make(chan []byte, 4)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *hub) broadcast(msg []byte) {
	h.mu.Lock()
	h.lastMsg = msg
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
			// Slow client: drop this frame rather than block the poller.
		}
	}
	h.mu.Unlock()
}

func (h *hub) last() []byte {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMsg
}
