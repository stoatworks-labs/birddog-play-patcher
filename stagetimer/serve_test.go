package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUI is birdUI's /settings: the System page for the right session, the
// login page otherwise.
func fakeUI(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value == "good-session" {
			io.WriteString(w, "<html>… Reset System Default …</html>")
			return
		}
		io.WriteString(w, `<html><div id="div_login_box"></div></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fakeDisplay struct {
	mu                sync.Mutex
	held              bool
	acquires, release int
	fail              error
}

func (d *fakeDisplay) Acquire() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail != nil {
		return d.fail
	}
	d.held = true
	d.acquires++
	return nil
}
func (d *fakeDisplay) Release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.held {
		d.release++
	}
	d.held = false
}
func (d *fakeDisplay) Held() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.held }
func (d *fakeDisplay) counts() (int, int, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.acquires, d.release, d.held
}

type fakeSink struct {
	mu      sync.Mutex
	frames  int
	last    *image.RGBA
	failing bool
	stopped int
}

func (s *fakeSink) Start(w, h int) error { return nil }
func (s *fakeSink) Stop()                { s.mu.Lock(); s.stopped++; s.mu.Unlock() }
func (s *fakeSink) Err() error           { return nil }
func (s *fakeSink) Show(img *image.RGBA) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing {
		return errors.New("pipeline died")
	}
	s.frames++
	s.last = img
	return nil
}
func (s *fakeSink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.frames }

type apiFixture struct {
	*fixture
	api     *APIServer
	srv     *httptest.Server
	display *fakeDisplay
	sink    *fakeSink
	log     *Logger
}

func newAPIFixture(t *testing.T) *apiFixture {
	fx := newFixture(t)
	ui := fakeUI(t)
	log := NewLogger()
	d := &fakeDisplay{}
	sink := &fakeSink{}
	hc := newHTTPClient()
	screen := NewScreen(ScreenDeps{
		Display: d, NewSink: func() FrameSink { return sink }, HTTP: hc, Log: log,
		Mode: func() OutputMode { return OutputMode{1280, 720} }, Tick: 20 * time.Millisecond,
	})
	store := &ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	api := &APIServer{Store: store, Screen: screen, Gate: NewGate(ui.URL), Caps: Caps{RawVideo: true}, Version: "test", Log: log, HTTP: hc}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(func() { screen.Stop(); srv.Close() })
	return &apiFixture{fixture: fx, api: api, srv: srv, display: d, sink: sink, log: log}
}

func (a *apiFixture) do(t *testing.T, method, path, session string, body any) (int, map[string]any, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, raw
}

const key = "key-abcdef-1234"

func TestAPIGate(t *testing.T) {
	a := newAPIFixture(t)
	for _, p := range []string{"/api/config", "/api/log", "/api/preview.png"} {
		if code, _, _ := a.do(t, "GET", p, "", nil); code != http.StatusForbidden {
			t.Errorf("%s without a session: %d", p, code)
		}
		if code, _, _ := a.do(t, "GET", p, "stale", nil); code != http.StatusForbidden {
			t.Errorf("%s with a bad session: %d", p, code)
		}
		if code, _, _ := a.do(t, "GET", p, "good-session", nil); code != http.StatusOK {
			t.Errorf("%s with a session: %d", p, code)
		}
	}
	if code, _, _ := a.do(t, "POST", "/api/show", "", map[string]any{}); code != http.StatusForbidden {
		t.Errorf("show without a session: %d", code)
	}
	_, st, _ := a.do(t, "GET", "/api/status", "", nil)
	if auth := st["auth"].(map[string]any); auth["authenticated"] != false || auth["device_protected"] != true {
		t.Errorf("status auth %v", auth)
	}
}

func TestAPICORS(t *testing.T) {
	a := newAPIFixture(t)
	req, _ := http.NewRequest("OPTIONS", a.srv.URL+"/api/config", nil)
	req.Header.Set("Origin", "http://192.168.1.50")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://192.168.1.50" ||
		resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestAPIConfigKeepsTheKeyPrivate(t *testing.T) {
	a := newAPIFixture(t)
	s := "good-session"
	code, out, _ := a.do(t, "POST", "/api/config", s, map[string]any{
		"room_id": "https://stagetimer.io/r/room1/", "api_key": key, "api_base": a.fixture.srv.URL + "/", "display": DefaultViewOptions(), "resume": true,
	})
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	cv := out["config"].(map[string]any)
	if cv["room_id"] != "room1" || cv["api_key_set"] != true || cv["api_key_hint"] != "…1234" || cv["api_base"] != a.fixture.srv.URL {
		t.Errorf("saved view %v", cv)
	}

	// The key is never sent back, from any endpoint.
	for _, p := range []string{"/api/config", "/api/status", "/api/log"} {
		_, _, raw := a.do(t, "GET", p, s, nil)
		if strings.Contains(string(raw), key) {
			t.Errorf("%s returns the key: %s", p, raw)
		}
	}
	_, _, raw := a.do(t, "GET", "/api/status", "", nil)
	if strings.Contains(string(raw), "room1") {
		t.Errorf("the open status names the room: %s", raw)
	}

	// An empty key keeps the saved one; a new view applies.
	opts := DefaultViewOptions()
	opts.View = ViewAgenda
	code, out, _ = a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "room1", "api_key": "", "api_base": a.fixture.srv.URL, "display": opts, "resume": false})
	if code != 200 {
		t.Fatalf("resave: %d %v", code, out)
	}
	cfg, _ := a.api.Store.Load()
	if cfg.APIKey != key || cfg.Display.View != ViewAgenda || cfg.Resume {
		t.Errorf("after resave: %+v", cfg)
	}

	// Bad input is refused and changes nothing.
	code, _, _ = a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "room 1", "display": opts})
	if code != 400 {
		t.Errorf("bad room: %d", code)
	}
	if cfg2, _ := a.api.Store.Load(); cfg2 != cfg {
		t.Errorf("a refused save changed the config: %+v", cfg2)
	}
	code, _, _ = a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "room1", "surprise": 1, "display": opts})
	if code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	req, _ := http.NewRequest("POST", a.srv.URL+"/api/config", strings.NewReader(`room_id=x`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: s})
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 400 {
		t.Errorf("a form post (no preflight) was not refused: %v %v", resp.StatusCode, err)
	}

	// Clearing the room clears its key.
	code, _, _ = a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "", "api_key": "", "display": opts})
	if cfg, _ := a.api.Store.Load(); code != 200 || cfg.APIKey != "" || cfg.RoomID != "" {
		t.Errorf("clearing the room: %d %+v", code, cfg)
	}

	if fi, err := os.Stat(a.api.Store.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("config file mode %v %v", fi.Mode(), err)
	}
	for _, l := range a.log.Lines() {
		if strings.Contains(l.Text, key) {
			t.Errorf("the key reached the log: %q", l.Text)
		}
	}
}

func TestAPITest(t *testing.T) {
	a := newAPIFixture(t)
	s := "good-session"
	_, out, _ := a.do(t, "POST", "/api/test", s, map[string]any{"room_id": "room1", "api_key": key, "api_base": a.fixture.srv.URL})
	if out["ok"] != true || out["room"] != "Mock room" || out["timers"] != float64(3) {
		t.Errorf("good test: %v", out)
	}
	_, out, _ = a.do(t, "POST", "/api/test", s, map[string]any{"room_id": "room1", "api_key": "wrong-key-00", "api_base": a.fixture.srv.URL})
	if out["ok"] != false || !strings.Contains(out["error"].(string), "Not a valid Bearer token") {
		t.Errorf("bad key: %v", out)
	}
	// The saved key is used when the field is left empty for the saved room.
	a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "room1", "api_key": key, "api_base": a.fixture.srv.URL, "display": DefaultViewOptions()})
	_, out, _ = a.do(t, "POST", "/api/test", s, map[string]any{"room_id": "room1", "api_key": "", "api_base": a.fixture.srv.URL})
	if out["ok"] != true {
		t.Errorf("saved key: %v", out)
	}
	code, _, _ := a.do(t, "POST", "/api/test", s, map[string]any{"room_id": "other", "api_key": ""})
	if code != 400 {
		t.Errorf("no key for another room: %d", code)
	}
}

func TestAPIShowAndStop(t *testing.T) {
	a := newAPIFixture(t)
	s := "good-session"
	if code, _, _ := a.do(t, "POST", "/api/show", s, map[string]any{}); code != 400 {
		t.Errorf("show unconfigured: %d", code)
	}
	a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "room1", "api_key": key, "api_base": a.fixture.srv.URL, "display": DefaultViewOptions(), "resume": true})
	code, out, _ := a.do(t, "POST", "/api/show", s, map[string]any{})
	if code != 200 {
		t.Fatalf("show: %d %v", code, out)
	}
	waitFor(t, "frames with the timer", 3*time.Second, func() bool {
		st := a.api.Screen.Status()
		return a.sink.count() >= 2 && st.Feed != nil && st.Feed.State == FeedLive
	})
	if acq, _, held := a.display.counts(); acq != 1 || !held {
		t.Errorf("display: %d acquires, held %v", acq, held)
	}
	if cfg, _ := a.api.Store.Load(); !cfg.Showing {
		t.Error("showing not recorded")
	}
	_, st, _ := a.do(t, "GET", "/api/status", "", nil)
	if sc := st["screen"].(map[string]any); sc["phase"] != "showing" || sc["raster"] != "1280x720" {
		t.Errorf("status screen %v", sc)
	}

	// A change of view applies to what is showing.
	n := a.sink.count()
	opts := DefaultViewOptions()
	opts.View = ViewClock
	a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "room1", "api_key": "", "api_base": a.fixture.srv.URL, "display": opts, "resume": true})
	waitFor(t, "a new frame", 2*time.Second, func() bool { return a.sink.count() > n })

	// The preview while showing is what is on screen, at 960×540.
	req, _ := http.NewRequest("GET", a.srv.URL+"/api/preview.png?view=agenda", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: s})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(resp.Body)
	resp.Body.Close()
	if err != nil || img.Bounds().Dx() != 960 || img.Bounds().Dy() != 540 {
		t.Fatalf("preview: %v %v", err, img)
	}

	if code, _, _ := a.do(t, "POST", "/api/stop", s, map[string]any{}); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	if _, rel, held := a.display.counts(); rel != 1 || held {
		t.Errorf("after stop: %d releases, held %v", rel, held)
	}
	if cfg, _ := a.api.Store.Load(); cfg.Showing {
		t.Error("showing still recorded after stop")
	}
}

func TestShowRefusedWhenTheDisplayIsTaken(t *testing.T) {
	a := newAPIFixture(t)
	s := "good-session"
	a.do(t, "POST", "/api/config", s, map[string]any{"room_id": "room1", "api_key": key, "api_base": a.fixture.srv.URL, "display": DefaultViewOptions()})
	a.display.fail = errors.New("another module (USB player, UVC converter or OMT receiver) is using the HDMI output; stop it first")
	code, out, _ := a.do(t, "POST", "/api/show", s, map[string]any{})
	if code != http.StatusConflict || !strings.Contains(out["error"].(string), "another module") {
		t.Errorf("show with the display taken: %d %v", code, out)
	}
	if a.api.Screen.Showing() || a.api.Screen.Status().Phase != ScreenFailed {
		t.Errorf("screen %v", a.api.Screen.Status())
	}
	if cfg, _ := a.api.Store.Load(); cfg.Showing {
		t.Error("a refused show was recorded as showing")
	}
}

func TestScreenGivesUpAFailingOutput(t *testing.T) {
	fx := newFixture(t)
	d := &fakeDisplay{}
	sink := &fakeSink{failing: true}
	screen := NewScreen(ScreenDeps{
		Display: d, NewSink: func() FrameSink { return sink }, HTTP: newHTTPClient(), Log: NewLogger(),
		Mode: func() OutputMode { return OutputMode{640, 360} }, Tick: 10 * time.Millisecond,
	})
	if err := screen.Start(fx.creds(), DefaultViewOptions()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed", 3*time.Second, func() bool { return screen.Status().Phase == ScreenFailed })
	waitFor(t, "display released", 3*time.Second, func() bool { _, rel, held := d.counts(); return rel == 1 && !held })
	if screen.Showing() {
		t.Error("still showing after giving up")
	}
	screen.Stop() // harmless afterwards
	if _, rel, _ := d.counts(); rel != 1 {
		t.Errorf("released %d times", rel)
	}
}

func TestPreviewWithoutARoom(t *testing.T) {
	screen := NewScreen(ScreenDeps{Display: &fakeDisplay{}, NewSink: func() FrameSink { return &fakeSink{} }, HTTP: newHTTPClient(), Log: NewLogger(),
		Mode: func() OutputMode { return OutputMode{1920, 1080} }})
	img := screen.Preview(Credentials{}, DefaultViewOptions(), 960, 540)
	if img.Rect.Dx() != 960 || distinct(img) < 2 {
		t.Error("blank preview")
	}
	if screen.Status().Feed != nil {
		t.Error("a feed started with no credentials")
	}
}
