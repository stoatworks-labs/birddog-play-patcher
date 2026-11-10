package main

// The API behind the Stagetimer tab.
//
//	GET  /api/status        open: what is on screen and the feed's health — no room, no key
//	GET  /api/config        the settings, with the key reduced to its last four characters
//	POST /api/config        change them; display changes apply live, a new room switches over
//	POST /api/test          {"room_id","api_key"} checked against Stagetimer, without saving
//	POST /api/show          put the saved room on HDMI
//	POST /api/stop          give HDMI back to the PLAY's own decoder
//	GET  /api/preview.png   what the screen shows, or would show, at 960×540
//	GET  /api/log           the last 200 log lines
//
// Everything but /api/status needs a birdUI session (auth.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const requestTimeout = 20 * time.Second

type APIServer struct {
	Store   *ConfigStore
	Screen  *Screen
	Gate    *Gate
	Caps    Caps
	Version string
	Log     *Logger
	HTTP    *http.Client
}

// Caps are what this box turned out to support at start.
type Caps struct {
	RawVideo bool `json:"raw_video"` // rawvideoparse present: frames can reach HDMI at all
}

func (a *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/config", a.gated(a.handleConfig))
	mux.HandleFunc("/api/test", a.gated(a.handleTest))
	mux.HandleFunc("/api/show", a.gated(a.handleShow))
	mux.HandleFunc("/api/stop", a.gated(a.handleStop))
	mux.HandleFunc("/api/preview.png", a.gated(a.handlePreview))
	mux.HandleFunc("/api/log", a.gated(a.handleLog))
	return cors(mux)
}

// cors echoes the caller's origin and allows credentials: a wildcard origin
// may not carry cookies, and the session cookie is what authorises a change.
// Changes are JSON POSTs, so they always face a preflight.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (a *APIServer) gated(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if _, err := a.Gate.Check(ctx, r); err != nil {
			// 403, not 401: a WWW-Authenticate challenge would make the
			// browser show its own basic-auth dialog.
			writeErr(w, http.StatusForbidden, err)
			return
		}
		h(w, r.WithContext(ctx))
	}
}

func decodeBody(r *http.Request, v any) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("use POST")
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		// Insisting on JSON keeps every change behind a CORS preflight.
		return fmt.Errorf("send application/json")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

type statusResponse struct {
	Version    string       `json:"version"`
	Screen     ScreenStatus `json:"screen"`
	Configured bool         `json:"configured"`
	Showing    bool         `json:"showing"`
	Caps       Caps         `json:"caps"`
	Auth       AuthState    `json:"auth"`
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cfg, _ := a.Store.Load()
	writeJSON(w, http.StatusOK, statusResponse{
		Version:    a.Version,
		Screen:     a.Screen.Status(),
		Configured: cfg.Configured(),
		Showing:    a.Screen.Showing(),
		Caps:       a.Caps,
		Auth:       a.Gate.Describe(ctx, r),
	})
}

// configView is the config as the tab sees it.
type configView struct {
	RoomID     string      `json:"room_id"`
	APIKey     string      `json:"api_key,omitempty"` // in: a new key, "" keeps the old
	APIKeySet  bool        `json:"api_key_set"`
	APIKeyHint string      `json:"api_key_hint,omitempty"`
	APIBase    string      `json:"api_base"`
	Display    ViewOptions `json:"display"`
	Resume     bool        `json:"resume"`
}

func viewOf(c Config) configView {
	return configView{
		RoomID: c.RoomID, APIKeySet: c.APIKey != "", APIKeyHint: KeyHint(c.APIKey),
		APIBase: c.APIBase, Display: c.Display, Resume: c.Resume,
	}
}

func (a *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg, err := a.Store.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": viewOf(cfg)})
		return
	}
	var v configView
	if err := decodeBody(r, &v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := a.Store.Update(func(c *Config) {
		c.RoomID = NormalizeRoomID(v.RoomID)
		if k := strings.TrimSpace(v.APIKey); k != "" {
			c.APIKey = k
		}
		if c.RoomID == "" {
			c.APIKey = "" // clearing the room clears its key
		}
		c.APIBase = strings.TrimRight(strings.TrimSpace(v.APIBase), "/")
		c.Display, c.Resume = v.Display, v.Resume
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	a.Screen.SetOptions(cfg.Display)
	if a.Screen.Showing() {
		if !cfg.Configured() {
			a.stop()
		} else if err := a.Screen.Start(cfg.Credentials(), cfg.Display); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": viewOf(cfg)})
}

// NormalizeRoomID accepts a room ID or a pasted Stagetimer link to the room.
func NormalizeRoomID(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		if u, err := url.Parse(s); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			for i := 0; i+1 < len(parts); i++ {
				if parts[i] == "r" || parts[i] == "room" {
					return parts[i+1]
				}
			}
		}
	}
	return s
}

func (a *APIServer) handleTest(w http.ResponseWriter, r *http.Request) {
	var v struct {
		RoomID  string `json:"room_id"`
		APIKey  string `json:"api_key"`
		APIBase string `json:"api_base"`
	}
	if err := decodeBody(r, &v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, _ := a.Store.Load()
	c := Credentials{RoomID: NormalizeRoomID(v.RoomID), APIKey: strings.TrimSpace(v.APIKey), Base: strings.TrimSpace(v.APIBase)}
	if c.APIKey == "" && c.RoomID == cfg.RoomID {
		c.APIKey = cfg.APIKey // testing the saved key
	}
	if c.RoomID == "" || c.APIKey == "" {
		writeErr(w, http.StatusBadRequest, errors.New("enter the room ID and the API key"))
		return
	}
	cl := NewClient(c, a.HTTP)
	if err := cl.TestAuth(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	room, err := cl.Room(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ts, _ := cl.Timers(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "room": room.Name, "timezone": room.Timezone, "timers": len(ts)})
}

func (a *APIServer) handleShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	if !a.Caps.RawVideo {
		writeErr(w, http.StatusConflict, errors.New("this PLAY's GStreamer has no rawvideoparse, so nothing can be drawn on HDMI"))
		return
	}
	cfg, err := a.Store.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !cfg.Configured() {
		writeErr(w, http.StatusBadRequest, errors.New("save a room ID and API key first"))
		return
	}
	if err := a.Screen.Start(cfg.Credentials(), cfg.Display); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if _, err := a.Store.Update(func(c *Config) { c.Showing = true }); err != nil {
		a.Log.Warn("config: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "screen": a.Screen.Status()})
}

func (a *APIServer) stop() {
	if _, err := a.Store.Update(func(c *Config) { c.Showing = false }); err != nil {
		a.Log.Warn("config: %v", err)
	}
	a.Screen.Stop()
}

func (a *APIServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	a.stop()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "screen": a.Screen.Status()})
}

func (a *APIServer) handlePreview(w http.ResponseWriter, r *http.Request) {
	cfg, _ := a.Store.Load()
	opts := cfg.Display
	if v := r.URL.Query().Get("view"); validView(v) {
		opts.View = v // the tab previews a view before it is saved
	}
	img := a.Screen.Preview(cfg.Credentials(), opts, 960, 540)
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (a *APIServer) handleLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"lines": a.Log.Lines()})
}
