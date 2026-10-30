package main

// The API behind the OMT tab.
//
//	GET  /api/status    open: state, source, stream, counters, CPU, temperature
//	GET  /api/sources   OMT sources on the network (?refresh=1 browses again)
//	POST /api/show      {"source":{"name":…,"address":…}}  put a source on HDMI
//	POST /api/stop      give HDMI back to the PLAY's own decoder
//	GET  /api/config    the settings
//	POST /api/config    change them (a live session restarts with them)
//	GET  /api/log       the last 200 log lines
//
// Everything but /api/status needs a birdUI session (auth.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const requestTimeout = 20 * time.Second

type APIServer struct {
	Store     *ConfigStore
	Session   *Session
	Discovery *Discovery
	Gate      *Gate
	Sys       *sysSampler
	Caps      Caps
	Version   string
	Log       *Logger
}

// Caps are what this box turned out to support at start.
type Caps struct {
	RawVideo bool   `json:"raw_video"` // rawvideoparse present: video can be shown at all
	Audio    bool   `json:"audio"`     // rawaudioparse present: sound can be played
	VMX      bool   `json:"vmx"`
	VMXError string `json:"vmx_error,omitempty"`
}

func (a *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/sources", a.gated(a.handleSources))
	mux.HandleFunc("/api/show", a.gated(a.handleShow))
	mux.HandleFunc("/api/stop", a.gated(a.handleStop))
	mux.HandleFunc("/api/config", a.gated(a.handleConfig))
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
	Version string        `json:"version"`
	Session SessionStatus `json:"session"`
	OnAir   bool          `json:"on_air"`
	Saved   Source        `json:"saved_source"`
	Caps    Caps          `json:"caps"`
	Sys     SysStats      `json:"sys"`
	Auth    AuthState     `json:"auth"`
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cfg, _ := a.Store.Load()
	writeJSON(w, http.StatusOK, statusResponse{
		Version: a.Version,
		Session: a.Session.Status(),
		OnAir:   cfg.OnAir,
		Saved:   cfg.Source,
		Caps:    a.Caps,
		Sys:     a.Sys.Read(),
		Auth:    a.Gate.Describe(ctx, r),
	})
}

func (a *APIServer) handleSources(w http.ResponseWriter, r *http.Request) {
	maxAge := 5 * time.Second
	if r.URL.Query().Get("refresh") != "" {
		maxAge = 0
	}
	found, err := a.Discovery.Sources(r.Context(), maxAge)
	resp := map[string]any{"sources": found}
	if found == nil {
		resp["sources"] = []Discovered{}
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *APIServer) handleShow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source Source `json:"source"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	src := Source{Name: strings.TrimSpace(req.Source.Name), Address: strings.TrimSpace(req.Source.Address)}
	if src.IsZero() {
		writeErr(w, http.StatusBadRequest, errors.New("choose a source or type an address"))
		return
	}
	if src.Address != "" {
		if _, _, err := parseAddress(src.Address); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if !a.Caps.RawVideo {
		writeErr(w, http.StatusConflict, errors.New("this PLAY's GStreamer has no rawvideoparse, so OMT cannot be shown on HDMI"))
		return
	}
	if !a.Caps.VMX {
		writeErr(w, http.StatusConflict, fmt.Errorf("the VMX decoder did not load: %s", a.Caps.VMXError))
		return
	}
	cfg, err := a.Store.Update(func(c *Config) {
		c.Source = src
		c.OnAir = true
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.Session.Start(src, cfg.Settings()); err != nil {
		_, _ = a.Store.Update(func(c *Config) { c.OnAir = false })
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": a.Session.Status()})
}

func (a *APIServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	if _, err := a.Store.Update(func(c *Config) { c.OnAir = false }); err != nil {
		a.Log.Warn("config: %v", err)
	}
	a.Session.Stop()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": a.Session.Status()})
}

// configView is the editable part of the config.
type configView struct {
	Resume      bool    `json:"resume"`
	Quality     Quality `json:"quality"`
	Audio       bool    `json:"audio"`
	Tally       bool    `json:"tally"`
	Threads     int     `json:"threads"`
	HoldOnLoss  bool    `json:"hold_on_loss"`
	MatchOutput bool    `json:"match_output"`
}

func viewOf(c Config) configView {
	return configView{c.Resume, c.Quality, c.Audio, c.Tally, c.Threads, c.HoldOnLoss, c.MatchOutput}
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
	probe := DefaultConfig()
	probe.Quality, probe.Threads = v.Quality, v.Threads
	if err := probe.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := a.Store.Update(func(c *Config) {
		c.Resume, c.Quality, c.Audio, c.Tally = v.Resume, v.Quality, v.Audio, v.Tally
		c.Threads, c.HoldOnLoss, c.MatchOutput = v.Threads, v.HoldOnLoss, v.MatchOutput
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// A live session picks the new settings up by restarting its loops; the
	// display stays held, so PPApp is not cycled for a settings change.
	if src, running := a.Session.Current(); running {
		if err := a.Session.Start(src, cfg.Settings()); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": viewOf(cfg)})
}

func (a *APIServer) handleLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"lines": a.Log.Lines()})
}
