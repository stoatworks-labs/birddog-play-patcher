package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newAPI(t *testing.T, gateOpen bool) (*httptest.Server, *Runner, *fakeDecoder) {
	t.Helper()
	r, d := newRunner(t, time.Minute)
	gate := NewGate("http://127.0.0.1:1") // nothing answers: only AllowUnprotected gets through
	gate.AllowUnprotected = gateOpen
	api := &APIServer{Store: r.Store, Runner: r, Decoder: d, Gate: gate,
		Sys: fakeSys(t, "connected", edid(14850, "TEST")), ShimStatus: filepath.Join(t.TempDir(), "none"),
		Version: "test", Log: r.Log}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, r, d
}

func call(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", "http://play.local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if method != http.MethodOptions && resp.Header.Get("Access-Control-Allow-Origin") != "http://play.local" {
		t.Errorf("%s: origin not echoed", path)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestStatusAndFormatsAreOpen(t *testing.T) {
	srv, _, _ := newAPI(t, false)
	code, st := call(t, srv, "GET", "/api/status", "")
	if code != 200 || st["version"] != "test" || st["screen"].(map[string]any)["name"] != "TEST" {
		t.Fatal(code, st)
	}
	if auth := st["auth"].(map[string]any); auth["authenticated"] == true {
		t.Fatal("authenticated with no birdUI to ask")
	}
	code, f := call(t, srv, "GET", "/api/formats", "")
	if code != 200 || len(f["standards"].([]any)) != len(Standards) {
		t.Fatal(code)
	}
}

func TestChangesAreGated(t *testing.T) {
	srv, r, d := newAPI(t, false)
	for _, p := range []string{"/api/format", "/api/aoi", "/api/keep", "/api/revert", "/api/restart", "/api/resolve", "/api/preview"} {
		if code, _ := call(t, srv, "POST", p, `{}`); code != http.StatusForbidden {
			t.Errorf("%s: %d without a session", p, code)
		}
	}
	for _, p := range []string{"/api/config", "/api/log"} {
		if code, _ := call(t, srv, "GET", p, ""); code != http.StatusForbidden {
			t.Errorf("%s: %d without a session", p, code)
		}
	}
	if d.count() != 0 || r.Status().Trial != nil {
		t.Fatal("something happened")
	}
}

func TestTryKeepThroughTheAPI(t *testing.T) {
	srv, r, d := newAPI(t, true)
	code, res := call(t, srv, "POST", "/api/resolve", `{"format":{"kind":"custom","width":3840,"height":1080,"refresh":60}}`)
	if code != 200 || !strings.HasPrefix(res["modeline"].(string), `Modeline "3840x1080" 296.703`) {
		t.Fatal(code, res)
	}
	if notes, _ := res["notes"].([]any); len(notes) == 0 || !strings.Contains(notes[0].(string), "does not list this size") {
		t.Fatalf("no warning that the screen does not list 3840x1080: %v", res["notes"])
	}
	code, res = call(t, srv, "POST", "/api/format", `{"format":{"kind":"standard","standard":"720p50"}}`)
	if code != 200 || d.count() != 1 {
		t.Fatal(code, res)
	}
	if trial := res["runner"].(map[string]any)["trial"]; trial == nil {
		t.Fatal("no trial in the answer")
	}
	if code, _ := call(t, srv, "POST", "/api/keep", `{}`); code != 200 {
		t.Fatal(code)
	}
	if cfg, _ := r.Store.Load(); cfg.Format.Standard != "720p50" {
		t.Fatal("not kept")
	}
	if code, res := call(t, srv, "POST", "/api/keep", `{}`); code != http.StatusConflict {
		t.Fatal(code, res)
	}
}

func TestBadBodiesAreRefused(t *testing.T) {
	srv, _, d := newAPI(t, true)
	for body, why := range map[string]string{
		`{"format":{"kind":"standard","standard":"1080p61"}}`:                                 "unknown standard",
		`{"format":{"kind":"timing","timing":{"clock_khz":154000}}}`:                          "bad clock",
		`{"format":{"kind":"source"},"extra":1}`:                                              "unknown field",
		`{"aoi":{"on":true,"src":{"x":0,"y":0,"w":0,"h":0},"dst":{"x":0,"y":0,"w":1,"h":1}}}`: "wrong endpoint",
	} {
		if code, _ := call(t, srv, "POST", "/api/format", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", why, code)
		}
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/format", strings.NewReader(`{"format":{"kind":"source"}}`))
	req.Header.Set("Content-Type", "text/plain")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusBadRequest {
		t.Error("a form post got through without a preflight")
	}
	if d.count() != 0 {
		t.Fatal("a refused body restarted the decoder")
	}
}

func TestAOIAndPreviewThroughTheAPI(t *testing.T) {
	srv, r, _ := newAPI(t, true)
	aoi := `{"on":true,"src":{"x":500000,"y":0,"w":500000,"h":1000000},"dst":{"x":0,"y":0,"w":1000000,"h":1000000},"keep_aspect":false}`
	code, res := call(t, srv, "POST", "/api/preview", `{"aoi":`+aoi+`,"picture":[3840,1080],"screen":[1920,1080]}`)
	if code != 200 || res["plane"].(map[string]any)["sx"] != 1920.0 {
		t.Fatal(code, res)
	}
	if code, res = call(t, srv, "POST", "/api/aoi", `{"aoi":`+aoi+`}`); code != 200 {
		t.Fatal(code, res)
	}
	if cfg, _ := r.Store.Load(); !cfg.AOI.Active() {
		t.Fatal("AOI not saved")
	}
}

func TestPreflight(t *testing.T) {
	srv, _, _ := newAPI(t, false)
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/format", nil)
	req.Header.Set("Origin", "http://play.local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal(resp, err)
	}
}
