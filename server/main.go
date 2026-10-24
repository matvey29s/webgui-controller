package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultAddr  = ":8080"
	metadataPath = "modules-config.json"
	publicDir    = "../public"
)

// ---- SSE hub: fan-out live state changes to every connected browser -------

type sseHub struct {
	mu      sync.Mutex
	clients map[chan []byte]bool
}

func newSseHub() *sseHub {
	return &sseHub{clients: map[chan []byte]bool{}}
}

func (h *sseHub) subscribe() chan []byte {
	ch := make(chan []byte, 8)
	h.mu.Lock()
	h.clients[ch] = true
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *sseHub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- payload:
		default: // slow client — drop this update rather than block the hub
		}
	}
}

// ---- wire it all together ---------------------------------------------------

var (
	adc    = NewAdcMonitor()
	store  = NewConfigStore(configStorePath)
	hub    = newSseHub()
)

func main() {
	addr := os.Getenv("PLC_WEBGUI_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	adc.Start(500 * time.Millisecond)
	go func() {
		for diff := range adc.Events {
			payload, _ := json.Marshal(map[string]interface{}{
				"type":  "state",
				"full":  false,
				"state": diff,
			})
			hub.broadcast(payload)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(publicDir)))
	mux.HandleFunc("/api/metadata", handleMetadata)
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/events", handleEvents)
	mux.HandleFunc("/api/interface/", handleInterfaceConfig)
	mux.HandleFunc("/api/_simulate/", handleSimulate)

	log.Printf("PLC web GUI listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequests(mux)))
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// GET /api/metadata — static per-slot descriptions used for hover popups
func handleMetadata(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, metadataPath)
}

// GET /api/state — full current hardware state (used on initial page load)
func handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, adc.GetFullState())
}

// GET /api/events — Server-Sent Events stream of state diffs
func handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send the full current state immediately so a fresh page always
	// starts consistent, even if it missed earlier diffs.
	initial, _ := json.Marshal(map[string]interface{}{
		"type":  "state",
		"full":  true,
		"state": adc.GetFullState(),
	})
	writeSseEvent(w, initial)
	flusher.Flush()

	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case payload := <-ch:
			writeSseEvent(w, payload)
			flusher.Flush()
		}
	}
}

func writeSseEvent(w http.ResponseWriter, payload []byte) {
	w.Write([]byte("data: "))
	w.Write(payload)
	w.Write([]byte("\n\n"))
}

// GET/POST /api/interface/{id}/config — read or write RS-485 style settings
func handleInterfaceConfig(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/interface/"), "/")
	if len(parts) != 2 || parts[1] != "config" || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	slotID := parts[0]

	switch r.Method {
	case http.MethodGet:
		cfg, ok := store.Get(slotID)
		if !ok {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeJSON(w, http.StatusOK, cfg)

	case http.MethodPost:
		state := adc.GetFullState()
		slot, exists := state[slotID]
		if !exists || slot.Detected == nil || !*slot.Detected || slot.Type == nil || *slot.Type != "rs485" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "no RS-485 interface currently detected on this slot",
			})
			return
		}

		var cfg InterfaceConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if err := validateInterfaceConfig(cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := store.Set(slotID, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save config"})
			return
		}

		// Real deployment: push the new settings into the actual serial
		// driver here (e.g. termios ioctl on /dev/ttyS*, or notify a
		// separate fieldbus daemon over its own IPC channel).

		saved, _ := store.Get(slotID)
		payload, _ := json.Marshal(map[string]interface{}{
			"type": "config", "slot": slotID, "config": saved,
		})
		hub.broadcast(payload)
		writeJSON(w, http.StatusOK, saved)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// POST /api/_simulate/{slot}/{on|off} — demo-only hotplug simulator so the
// UI is demonstrable without real hardware. Remove in production.
func handleSimulate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/_simulate/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	adc.SimulateHotplug(parts[0], parts[1])
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
