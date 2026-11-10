package main

// The screen: the feed, the renderer, the HDMI sink and the handover, run
// together.
//
// The feed lives a little apart from the rest. It runs while the timer is on
// HDMI, and also for a minute after anyone looks at the tab's preview, so the
// settings can be checked on a laptop before the PLAY's own picture is given
// up. Changing the room restarts the feed; changing how it looks does not
// even do that — the loop reads the options every tick.

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net"
	"net/http"
	"sync"
	"time"
)

type ScreenPhase string

const (
	ScreenIdle     ScreenPhase = "idle"     // the PLAY's own decoder has HDMI
	ScreenStarting ScreenPhase = "starting" // taking the display
	ScreenShowing  ScreenPhase = "showing"
	ScreenFailed   ScreenPhase = "failed"
)

type ScreenDeps struct {
	Display DisplayOwner
	NewSink func() FrameSink
	Mode    func() OutputMode
	HTTP    *http.Client
	Log     *Logger
	// Tick is how often the picture is reconsidered; 100 ms by default.
	Tick time.Duration
	// Hint is the settings address shown on the setup screens.
	Hint func() string
}

type ScreenStatus struct {
	Phase   ScreenPhase `json:"phase"`
	Detail  string      `json:"detail,omitempty"`
	Since   time.Time   `json:"since"`
	Raster  string      `json:"raster,omitempty"`
	Output  string      `json:"output,omitempty"`
	Frames  int64       `json:"frames"`
	Feed    *Snapshot   `json:"feed,omitempty"`
	View    string      `json:"view"`
	Preview bool        `json:"feed_for_preview,omitempty"`
}

type Screen struct {
	d ScreenDeps

	mu      sync.Mutex
	phase   ScreenPhase
	detail  string
	since   time.Time
	creds   Credentials
	opts    ViewOptions
	w, h    int
	output  OutputMode
	frames  int64
	loopEnd context.CancelFunc
	loopWG  sync.WaitGroup

	feed       *Feed
	feedCreds  Credentials
	feedCancel context.CancelFunc
	previewAt  time.Time
}

func NewScreen(d ScreenDeps) *Screen {
	if d.Tick == 0 {
		d.Tick = 100 * time.Millisecond
	}
	if d.Hint == nil {
		d.Hint = func() string { return "" }
	}
	s := &Screen{d: d, phase: ScreenIdle, since: time.Now(), opts: DefaultViewOptions()}
	go s.reapFeed()
	return s
}

func (s *Screen) setPhaseLocked(p ScreenPhase, detail string) {
	if s.phase != p || s.detail != detail {
		s.since = time.Now()
		if detail != "" {
			s.d.Log.Info("screen: %s — %s", p, detail)
		} else {
			s.d.Log.Info("screen: %s", p)
		}
	}
	s.phase, s.detail = p, detail
}

// SetOptions changes how the screen looks, live.
func (s *Screen) SetOptions(o ViewOptions) {
	s.mu.Lock()
	s.opts = o
	s.mu.Unlock()
}

// Showing reports whether the timer has HDMI.
func (s *Screen) Showing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loopEnd != nil
}

// Start puts the room on HDMI, or, if it is already there, switches room.
func (s *Screen) Start(c Credentials, o ViewOptions) error {
	if c.RoomID == "" || c.APIKey == "" {
		return errors.New("set the room ID and API key first")
	}
	s.mu.Lock()
	s.opts = o
	s.creds = c
	if s.loopEnd != nil {
		s.ensureFeedLocked(c)
		s.mu.Unlock()
		return nil
	}
	s.setPhaseLocked(ScreenStarting, "taking HDMI from the PLAY's decoder")
	s.mu.Unlock()

	if err := s.d.Display.Acquire(); err != nil {
		s.mu.Lock()
		s.setPhaseLocked(ScreenFailed, err.Error())
		s.mu.Unlock()
		return err
	}
	mode := s.d.Mode()
	w, h := renderSize(mode)
	sink := s.d.NewSink()
	if err := sink.Start(w, h); err != nil {
		s.d.Display.Release()
		s.mu.Lock()
		s.setPhaseLocked(ScreenFailed, err.Error())
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.w, s.h, s.output, s.frames = w, h, mode, 0
	s.ensureFeedLocked(c)
	ctx, cancel := context.WithCancel(context.Background())
	s.loopEnd = cancel
	s.setPhaseLocked(ScreenShowing, "")
	s.loopWG.Add(1)
	go s.loop(ctx, sink, w, h)
	return nil
}

// Stop gives HDMI back.
func (s *Screen) Stop() {
	s.mu.Lock()
	end := s.loopEnd
	s.loopEnd = nil
	s.mu.Unlock()
	if end != nil {
		end()
		s.loopWG.Wait()
		s.d.Display.Release()
	}
	s.mu.Lock()
	s.setPhaseLocked(ScreenIdle, "")
	s.mu.Unlock()
}

// renderSize is the frame we draw for the output mode. Above 1080p we draw
// 1080p and let the plane scale it up: a 4K RGBA frame is 33 MB per change
// on four A53s, and a countdown gains nothing from it.
func renderSize(m OutputMode) (int, int) {
	w, h := m.Width, m.Height
	if w <= 0 || h <= 0 {
		return 1920, 1080
	}
	if w > 1920 || h > 1080 {
		f := min(1920/float64(w), 1080/float64(h))
		w, h = int(float64(w)*f), int(float64(h)*f)
	}
	return w &^ 1, h &^ 1
}

func (s *Screen) loop(ctx context.Context, sink FrameSink, w, h int) {
	defer s.loopWG.Done()
	defer sink.Stop()
	r := NewRenderer(w, h)
	t := time.NewTicker(s.d.Tick)
	defer t.Stop()
	var lastKey string
	var lastSent time.Time
	var failures []time.Time
	for {
		s.mu.Lock()
		feed, opts := s.feed, s.opts
		s.mu.Unlock()
		snap := Snapshot{State: FeedConnecting, Since: time.Now()}
		if feed != nil {
			snap = feed.Snapshot()
		}
		m := BuildModel(snap, time.Now(), opts, s.d.Hint())
		// Resend now and then even when nothing changed: cheap insurance
		// against a plane that lost its frame.
		if k := m.Key(); k != lastKey || time.Since(lastSent) > 30*time.Second {
			if err := sink.Show(r.Draw(m)); err != nil {
				failures = append(failures, time.Now())
				for len(failures) > 0 && time.Since(failures[0]) > time.Minute {
					failures = failures[1:]
				}
				s.d.Log.Error("screen: %v", err)
				if len(failures) >= 3 {
					// A pipeline that will not run is a dark screen; give the
					// display back rather than hold it.
					s.mu.Lock()
					s.setPhaseLocked(ScreenFailed, "the HDMI output keeps failing: "+err.Error())
					end := s.loopEnd
					s.loopEnd = nil
					s.mu.Unlock()
					if end != nil {
						end()
					}
					// gst-launch first: PPApp cannot take DRM master
					// while kmssink still holds it.
					sink.Stop()
					s.d.Display.Release()
					return
				}
				sink.Stop()
				if err := sink.Start(w, h); err != nil {
					s.d.Log.Error("screen: restarting the output: %v", err)
				}
				lastKey = ""
			} else {
				lastKey, lastSent = k, time.Now()
				s.mu.Lock()
				s.frames++
				s.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ensureFeedLocked runs a feed for c, replacing one for other credentials.
func (s *Screen) ensureFeedLocked(c Credentials) {
	if s.feed != nil && s.feedCreds == c {
		return
	}
	if s.feedCancel != nil {
		s.feedCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := NewFeed(c, s.d.HTTP, s.d.Log)
	s.feed, s.feedCreds, s.feedCancel = f, c, cancel
	go f.Run(ctx)
}

// Preview renders what the screen would show now, at w×h, starting the feed
// for c if nothing is showing.
func (s *Screen) Preview(c Credentials, o ViewOptions, w, h int) *image.RGBA {
	s.mu.Lock()
	if s.loopEnd != nil {
		o = s.opts
	} else if c.RoomID != "" && c.APIKey != "" {
		s.ensureFeedLocked(c)
	}
	s.previewAt = time.Now()
	feed := s.feed
	s.mu.Unlock()
	snap := Snapshot{State: FeedIdle, Since: time.Now()}
	m := Model{}
	if feed != nil {
		snap = feed.Snapshot()
		m = BuildModel(snap, time.Now(), o, s.d.Hint())
	} else {
		m = Model{View: o.View, Notice: "Set the room ID and API key", NoticeDetail: "They are on the room’s Controller page in Stagetimer, under the API menu.", Hint: s.d.Hint()}
	}
	m.Mirror = "" // the preview reads the right way round
	img := NewRenderer(w, h).Draw(m)
	return img
}

// reapFeed stops a feed nobody is using.
func (s *Screen) reapFeed() {
	for range time.Tick(10 * time.Second) {
		s.mu.Lock()
		if s.loopEnd == nil && s.feed != nil && time.Since(s.previewAt) > time.Minute {
			s.feedCancel()
			s.feed, s.feedCancel, s.feedCreds = nil, nil, Credentials{}
			s.d.Log.Info("stagetimer: feed stopped (not on HDMI, no preview open)")
		}
		s.mu.Unlock()
	}
}

func (s *Screen) Status() ScreenStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ScreenStatus{Phase: s.phase, Detail: s.detail, Since: s.since, Frames: s.frames, View: s.opts.View}
	if s.loopEnd != nil {
		st.Raster = fmt.Sprintf("%dx%d", s.w, s.h)
		st.Output = fmt.Sprintf("%dx%d", s.output.Width, s.output.Height)
	}
	if s.feed != nil {
		snap := s.feed.Snapshot()
		st.Feed = &snap
		st.Preview = s.loopEnd == nil
	}
	return st
}

// settingsHint names where the settings are, for the setup screens.
func settingsHint() string {
	if ip := firstIPv4(); ip != "" {
		return "Settings: http://" + ip + "/ › AV Setup › Stagetimer"
	}
	return "Settings: the PLAY’s web UI › AV Setup › Stagetimer"
}

func firstIPv4() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if v4 := ipn.IP.To4(); v4 != nil && !v4.IsLinkLocalUnicast() {
					return v4.String()
				}
			}
		}
	}
	return ""
}
