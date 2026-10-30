package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// recVideo is fileVideo with a way to look at what it was given.
type recVideo struct {
	fileVideo
	starts [][2]int
}

func (r *recVideo) snapshot() (frames, w, h int, last []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames, r.w, r.h, append([]byte(nil), r.buf...)
}

func (r *recVideo) Submit(buf []byte) error {
	r.mu.Lock()
	r.buf = append(r.buf[:0], buf...)
	r.mu.Unlock()
	return r.fileVideo.Submit(buf)
}

func (r *recVideo) Buffer() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return make([]byte, len(r.buf))
}

func (r *recVideo) Start(w, h int) error {
	r.mu.Lock()
	r.starts = append(r.starts, [2]int{w, h})
	r.mu.Unlock()
	return r.fileVideo.Start(w, h)
}

func (f *fileAudio) total() int { f.mu.Lock(); defer f.mu.Unlock(); return f.bytes }

type apiHarness struct {
	t     *testing.T
	srv   *httptest.Server
	video *recVideo
	audio *fileAudio
	disp  *noDisplay
	store *ConfigStore
	sess  *Session
}

func newAPIHarness(t *testing.T, vmxPath string, tweak func(*SessionDeps)) *apiHarness {
	t.Helper()
	h := &apiHarness{t: t, video: &recVideo{}, audio: &fileAudio{}, disp: &noDisplay{}}
	log := NewLogger()
	h.store = &ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	deps := SessionDeps{
		Display:  h.disp,
		NewVideo: func(Settings) VideoSink { return h.video },
		NewAudio: func(Settings) AudioSink { return h.audio },
		VMX:      func() (*VMX, error) { return LoadVMX(vmxPath) },
		Resolve:  asIs,
		Mode:     func() OutputMode { return OutputMode{Width: 256, Height: 128} },
		AudioOK:  true,
		Log:      log,
	}
	if tweak != nil {
		tweak(&deps)
	}
	h.sess = NewSession(deps)
	gate := NewGate("http://127.0.0.1:1")
	gate.AllowUnprotected = true
	api := &APIServer{Store: h.store, Session: h.sess, Discovery: &Discovery{Log: log, Browse: nil},
		Gate: gate, Sys: &sysSampler{}, Caps: Caps{RawVideo: true, Audio: true, VMX: true}, Version: "test", Log: log}
	h.srv = httptest.NewServer(api.Handler())
	t.Cleanup(func() { h.sess.Stop(); h.srv.Close() })
	return h
}

func (h *apiHarness) post(path, body string) (int, map[string]any) {
	h.t.Helper()
	resp, err := http.Post(h.srv.URL+path, "application/json", bytes.NewBufferString(body))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *apiHarness) status() statusResponse {
	h.t.Helper()
	resp, err := http.Get(h.srv.URL + "/api/status")
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var s statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// The whole receive path through the API: Show takes the display, the stream
// is decoded to NV12 at its own size after a black frame at the output's, its
// sound reaches the audio sink, a lost source is noticed, and Stop gives the
// display back and clears on-air.
func TestShowDecodeAndStopThroughTheAPI(t *testing.T) {
	path := testVMXPath(t)
	const w, h = 128, 64
	frames := [][]byte{encodeFlat(t, path, w, h, 180, 90, 200), encodeFlat(t, path, w, h, 60, 128, 128)}
	vh := VideoHeader{Codec: codecVMX1, Width: w, Height: h, FrameRateN: 60, FrameRateD: 1, ColorSpace: 709}
	sender := newFakeSender(t, vh, frames, "")
	a := newAPIHarness(t, path, nil)

	code, body := a.post("/api/show", `{"source":{"address":"`+sender.addr()+`"}}`)
	if code != http.StatusOK {
		t.Fatalf("show: %d %v", code, body)
	}
	if !a.disp.Held() {
		t.Fatal("show did not take the display")
	}
	waitFor(t, "live with frames and sound", 5*time.Second, func() bool {
		s := a.status().Session
		return s.State == StateLive && s.Decoded > 10 && a.audio.total() > 0
	})
	n, gotW, gotH, last := a.video.snapshot()
	if gotW != w || gotH != h || n < 10 {
		t.Fatalf("video sink at %dx%d with %d frames", gotW, gotH, n)
	}
	if !checkFlat(last, w, h, 180, 90, 200) && !checkFlat(last, w, h, 60, 128, 128) {
		t.Fatal("the last frame shown is neither of the frames sent")
	}
	a.video.mu.Lock()
	starts := append([][2]int(nil), a.video.starts...)
	a.video.mu.Unlock()
	if len(starts) != 2 || starts[0] != [2]int{256, 128} || starts[1] != [2]int{w, h} {
		t.Fatalf("output starts %v: want the output's mode (for black), then the source's size", starts)
	}
	s := a.status()
	if s.Session.SinkRestarts != 0 || s.Session.DecodeErrors != 0 || s.Session.AudioHz != 48000 || !s.OnAir {
		t.Fatalf("status: %+v", s)
	}
	if s.Saved.Address != sender.addr() {
		t.Fatalf("saved source: %+v", s.Saved)
	}

	sender.paused.Store(true)
	waitFor(t, "the loss to be noticed", 5*time.Second, func() bool { return a.status().Session.State == StateWaiting })
	if !a.disp.Held() {
		t.Fatal("a lost source must not give the display back")
	}
	sender.paused.Store(false)
	waitFor(t, "live again", 3*time.Second, func() bool { return a.status().Session.State == StateLive })

	if code, body := a.post("/api/stop", `{}`); code != http.StatusOK {
		t.Fatalf("stop: %d %v", code, body)
	}
	s = a.status()
	if s.Session.State != StateIdle || a.disp.Held() || s.OnAir {
		t.Fatalf("after stop: state %s, display held %v, on air %v", s.Session.State, a.disp.Held(), s.OnAir)
	}
}

func TestAPIRefusesWithoutASession(t *testing.T) {
	a := newAPIHarness(t, "", nil)
	gate := NewGate("http://127.0.0.1:1") // nothing there: the session cannot be checked
	api := &APIServer{Store: a.store, Session: a.sess, Gate: gate, Sys: &sysSampler{}, Log: NewLogger()}
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/show", "application/json", bytes.NewBufferString(`{"source":{"address":"10.0.0.1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("show without a session: %d, want 403", resp.StatusCode)
	}
	if a.disp.Held() {
		t.Fatal("the display was taken without a session")
	}
	resp, err = http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status is open, got %d", resp.StatusCode)
	}
}

func TestAPIRefusesBadRequests(t *testing.T) {
	a := newAPIHarness(t, "", nil)
	for _, c := range []struct{ path, body string }{
		{"/api/show", `{}`},
		{"/api/show", `{"source":{"address":"http://x"}}`},
		{"/api/show", `{"source":{"name":"x"},"extra":1}`},
		{"/api/config", `{"quality":"Ultra"}`},
		{"/api/config", `{"quality":"Low","threads":12}`},
	} {
		if code, _ := a.post(c.path, c.body); code != http.StatusBadRequest {
			t.Errorf("%s %s: got %d, want 400", c.path, c.body, code)
		}
	}
	// A form post (no preflight from a browser) is refused.
	resp, err := http.Post(a.srv.URL+"/api/show", "text/plain", bytes.NewBufferString(`{"source":{"address":"10.0.0.1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || a.disp.Held() {
		t.Fatalf("text/plain show: %d", resp.StatusCode)
	}
}

// The same path through the real gst-launch pipelines, with filesink where
// kmssink and alsasink would be: the pipes, rawvideoparse's layout and
// rawaudioparse. Runs where GStreamer is installed (GStreamer 1.14 in a Debian
// 10 root is the PLAY's own version); skipped elsewhere.
func TestShowThroughGStreamer(t *testing.T) {
	path := testVMXPath(t)
	if !gstHasElement("rawvideoparse") || !gstHasElement("rawaudioparse") {
		t.Skip("no GStreamer with rawvideoparse and rawaudioparse on this machine")
	}
	const w, h = 128, 64
	frames := [][]byte{encodeFlat(t, path, w, h, 180, 90, 200), encodeFlat(t, path, w, h, 60, 128, 128)}
	vh := VideoHeader{Codec: codecVMX1, Width: w, Height: h, FrameRateN: 60, FrameRateD: 1, ColorSpace: 709}
	sender := newFakeSender(t, vh, frames, "")

	dir := t.TempDir()
	vfile, afile := filepath.Join(dir, "video.nv12"), filepath.Join(dir, "audio.s16")
	opts := GstOptions{VideoSink: "filesink location=" + vfile, AudioSink: "filesink location=" + afile, Log: NewLogger()}
	a := newAPIHarness(t, path, func(d *SessionDeps) {
		d.NewVideo = func(Settings) VideoSink { return NewGstVideo(opts) }
		d.NewAudio = func(Settings) AudioSink { return NewGstAudio(opts) }
		// No mode: the first frame starts the output, so one filesink sees it all.
		d.Mode = func() OutputMode { return OutputMode{} }
	})
	if code, body := a.post("/api/show", `{"source":{"address":"`+sender.addr()+`"}}`); code != http.StatusOK {
		t.Fatalf("show: %d %v", code, body)
	}
	waitFor(t, "frames through the pipeline", 10*time.Second, func() bool {
		s := a.status().Session
		return s.State == StateLive && s.Shown > 60 && s.AudioHz == 48000
	})
	time.Sleep(500 * time.Millisecond)
	s := a.status().Session
	if s.SinkRestarts != 0 || s.AudioError != "" || s.Error != "" {
		t.Fatalf("status: %+v", s)
	}
	a.post("/api/stop", `{}`)

	_, _, size := NV12Layout(w, h)
	video, err := os.ReadFile(vfile)
	if err != nil {
		t.Fatal(err)
	}
	if len(video) < 30*size || len(video)%size != 0 {
		t.Fatalf("video file is %d bytes: want a whole number of %d-byte frames, at least 30", len(video), size)
	}
	last := video[len(video)-size:]
	if !checkFlat(last, w, h, 180, 90, 200) && !checkFlat(last, w, h, 60, 128, 128) {
		t.Fatal("the last frame through GStreamer is neither of the frames sent")
	}
	audio, err := os.ReadFile(afile)
	if err != nil {
		t.Fatal(err)
	}
	if len(audio) < 48000*4/5 { // 200 ms of S16 stereo
		t.Fatalf("audio file is %d bytes", len(audio))
	}
}
