package main

// The API behind the Output tab.
//
//	GET  /api/status     open: the screen, the format in force, the shim, a trial's countdown
//	GET  /api/formats    open: the standard formats and the PHY's clocks
//	POST /api/resolve    {"format":{…}}  the timing a format gives, without applying it
//	POST /api/preview    {"aoi":{…},"picture":[w,h],"screen":[w,h]}  where an AOI puts the picture
//	POST /api/format     {"format":{…}}  try a format (or follow the source again)
//	POST /api/keep       keep the format being tried
//	POST /api/revert     go back to the kept format now
//	POST /api/aoi        {"aoi":{…}}  set the area of interest; takes at once
//	POST /api/restart    restart the PLAY's decoder (loads the shim after an install)
//	GET  /api/config     the kept settings
//	GET  /api/log        the last 200 log lines
//
// Everything else but /api/status and /api/formats needs a birdUI session
// (auth.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const requestTimeout = 60 * time.Second // a format change waits for PPApp to come back

type APIServer struct {
	Store      *ConfigStore
	Runner     *Runner
	Decoder    Decoder
	Gate       *Gate
	Sys        SysPaths
	ShimStatus string
	Version    string
	Log        *Logger
}

func (a *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/formats", a.handleFormats)
	mux.HandleFunc("/api/resolve", a.gated(a.handleResolve))
	mux.HandleFunc("/api/preview", a.gated(a.handlePreview))
	mux.HandleFunc("/api/format", a.gated(a.handleFormat))
	mux.HandleFunc("/api/keep", a.gated(a.post(a.Runner.Keep)))
	mux.HandleFunc("/api/revert", a.gated(a.post(a.Runner.Revert)))
	mux.HandleFunc("/api/aoi", a.gated(a.handleAOI))
	mux.HandleFunc("/api/restart", a.gated(a.post(a.Runner.RestartDecoder)))
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

// post is a bodiless action: still JSON, so still preflighted.
func (a *APIServer) post(fn func() error) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var empty struct{}
		if err := decodeBody(r, &empty); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := fn(); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runner": a.Runner.Status()})
	}
}

type statusResponse struct {
	Version string       `json:"version"`
	Runner  RunnerStatus `json:"runner"`
	Screen  Screen       `json:"screen"`
	Shim    ShimStatus   `json:"shim"`
	Decoder bool         `json:"decoder_running"`
	Auth    AuthState    `json:"auth"`
	Revert  int          `json:"revert_seconds"`
	Preview *Plane       `json:"aoi_preview,omitempty"`
	// PreviewError says why the kept AOI would not apply to what is showing.
	PreviewError string `json:"aoi_preview_error,omitempty"`
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	shim := ReadShimStatus(a.ShimStatus)
	pid := a.Decoder.PID()
	shim.Loaded = shim.Present && (pid == -1 || pid != 0 && shim.PID == pid)
	resp := statusResponse{
		Version: a.Version,
		Runner:  a.Runner.Status(),
		Screen:  ReadScreen(a.Sys),
		Shim:    shim,
		Decoder: a.Decoder.Running(),
		Auth:    a.Gate.Describe(ctx, r),
		Revert:  int(a.Runner.RevertAfter / time.Second),
	}
	if aoi := resp.Runner.Kept.AOI; aoi.Active() {
		if pw, ph, sw, sh, ok := shim.PictureSizes(); ok {
			if p, err := Preview(aoi, pw, ph, sw, sh); err != nil {
				resp.PreviewError = err.Error()
			} else {
				resp.Preview = &p
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type formatsResponse struct {
	Standards []Standard `json:"standards"`
	Clocks    []int      `json:"clocks_khz"`
}

func (a *APIServer) handleFormats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, formatsResponse{Standards: Standards, Clocks: ClocksKHz})
}

// resolved is a format's timing, spelled out for the tab.
type resolved struct {
	Mode     *Mode    `json:"mode"`
	Label    string   `json:"label"`
	Refresh  float64  `json:"refresh,omitempty"`
	Modeline string   `json:"modeline,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

func (a *APIServer) resolve(f Format) (resolved, error) {
	m, err := f.Resolve()
	if err != nil {
		return resolved{}, err
	}
	out := resolved{Mode: m, Label: f.Label()}
	if m == nil {
		return out, nil
	}
	out.Refresh, out.Modeline, out.Notes = m.Refresh(), m.Modeline(), m.Notes()
	sc := ReadScreen(a.Sys)
	if sc.Connected && len(sc.Modes) > 0 {
		listed := false
		for _, s := range sc.Modes {
			if s == fmt.Sprintf("%dx%d", m.HDisplay, m.VDisplay) {
				listed = true
			}
		}
		if !listed {
			out.Notes = append(out.Notes, "the screen does not list this size: it may show nothing, which is what the countdown is for")
		}
	}
	return out, nil
}

func (a *APIServer) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Format Format `json:"format"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := a.resolve(req.Format)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *APIServer) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AOI     AOI    `json:"aoi"`
		Picture [2]int `json:"picture"`
		Screen  [2]int `json:"screen"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := req.AOI.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := Preview(req.AOI, req.Picture[0], req.Picture[1], req.Screen[0], req.Screen[1])
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plane": p})
}

func (a *APIServer) handleFormat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Format Format `json:"format"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := req.Format.Resolve(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.Runner.TryFormat(req.Format); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runner": a.Runner.Status()})
}

func (a *APIServer) handleAOI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AOI AOI `json:"aoi"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.Runner.SetAOI(req.AOI); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runner": a.Runner.Status()})
}

func (a *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("use GET; changes go to /api/format and /api/aoi"))
		return
	}
	cfg, err := a.Store.Load()
	resp := map[string]any{"config": cfg}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *APIServer) handleLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"lines": a.Log.Lines()})
}
