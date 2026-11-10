package main

// Sample rooms, for --demo (PNGs of every screen, for the README and for
// looking at a layout change without a PLAY) and for the render tests.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func strp(s string) *string { return &s }

// demoSnapshot is a conference morning, ten minutes into its second talk.
func demoSnapshot(now time.Time) Snapshot {
	loc, _ := time.LoadLocation("Europe/London")
	day := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 9, 0, 0, 0, loc)
	at := func(h, m int) *string {
		return strp(day.Add(time.Duration(h-9)*time.Hour + time.Duration(m)*time.Minute).UTC().Format(time.RFC3339))
	}
	timers := []Timer{
		{ID: "t1", Name: "Doors and welcome", Speaker: "Front of house", Duration: "0:15:00", Minutes: 15, WrapUpYellow: 120, WrapUpRed: 30, Appearance: "COUNTDOWN", StartTime: at(9, 0)},
		{ID: "t2", Name: "Keynote: Building for the room", Speaker: "Priya Raman", Duration: "0:25:00", Minutes: 25, WrapUpYellow: 300, WrapUpRed: 60, Appearance: "COUNTDOWN",
			Labels: []Label{{Name: "Keynote", Color: "#6366f1"}}},
		{ID: "t3", Name: "Panel: Live production on a budget", Speaker: "Sam Okafor, Lena Fischer, Tom Hale", Duration: "0:40:00", Minutes: 40, WrapUpYellow: 300, WrapUpRed: 60, Appearance: "COUNTDOWN",
			Labels: []Label{{Name: "Panel", Color: "#0ea5e9"}, {Name: "Q&A", Color: "#f59e0b"}}},
		{ID: "t4", Name: "Coffee", Duration: "0:20:00", Minutes: 20, Appearance: "COUNTDOWN"},
		{ID: "t5", Name: "Workshop: Timecode without tears", Speaker: "Morgan Ellis", Duration: "0:45:00", Minutes: 45, WrapUpYellow: 300, WrapUpRed: 60, Appearance: "COUNTDOWN"},
		{ID: "t6", Name: "Lunch", Duration: "1:00:00", Hours: 1, Appearance: "COUNTDOWN"},
	}
	start := now.Add(-10*time.Minute - 23*time.Second)
	return Snapshot{
		State: FeedLive, Since: now.Add(-time.Hour), LastData: now,
		HaveRoom: true, HaveStatus: true,
		Room:    Room{ID: "room", Name: "Main stage", Timezone: "Europe/London"},
		Status:  PlaybackStatus{TimerID: "t2", Running: true, Start: start.UnixMilli(), Finish: start.Add(25 * time.Minute).UnixMilli(), ServerTime: now.UnixMilli()},
		Current: &timers[1], Next: &timers[2], Timers: timers,
	}
}

type demoScene struct {
	name string
	opts ViewOptions
	edit func(*Snapshot, time.Time)
}

func demoScenes() []demoScene {
	base := DefaultViewOptions()
	with := func(f func(*ViewOptions)) ViewOptions { o := base; f(&o); return o }
	setRemaining := func(s *Snapshot, now time.Time, rem time.Duration) {
		s.Status.Finish = now.Add(rem).UnixMilli()
		s.Status.Start = s.Status.Finish - 25*time.Minute.Milliseconds()
	}
	return []demoScene{
		{"timer", base, nil},
		{"timer-next", with(func(o *ViewOptions) { o.ShowNext = true }), nil},
		{"timer-yellow", base, func(s *Snapshot, now time.Time) { setRemaining(s, now, 4*time.Minute+12*time.Second) }},
		{"timer-red", base, func(s *Snapshot, now time.Time) { setRemaining(s, now, 42*time.Second) }},
		{"timer-overtime", base, func(s *Snapshot, now time.Time) { setRemaining(s, now, -83*time.Second) }},
		{"timer-paused", base, func(s *Snapshot, now time.Time) {
			s.Status.Running = false
			s.Status.Pause = now.UnixMilli()
		}},
		{"timer-message", base, func(s *Snapshot, now time.Time) {
			s.Message = &Message{ID: "m1", Showing: true, Text: "Five minutes for questions, then wrap up please", Color: "green", Bold: true}
			s.Room.OnAir = true
		}},
		{"timer-focus", base, func(s *Snapshot, now time.Time) {
			s.Message = &Message{ID: "m2", Showing: true, Text: "Wrap up now", Color: "red", Bold: true, Uppercase: true, Focus: true}
		}},
		{"timer-tod", base, func(s *Snapshot, now time.Time) {
			t := *s.Current
			t.Appearance = "COUNTDOWN_TOD"
			s.Current = &t
		}},
		{"agenda", with(func(o *ViewOptions) { o.View = ViewAgenda }), nil},
		{"agenda-message", with(func(o *ViewOptions) { o.View = ViewAgenda }), func(s *Snapshot, now time.Time) {
			s.Message = &Message{ID: "m1", Showing: true, Text: "Lunch is in the atrium today", Color: "white"}
		}},
		{"split", with(func(o *ViewOptions) { o.View = ViewSplit }), nil},
		{"split-message", with(func(o *ViewOptions) { o.View = ViewSplit }), func(s *Snapshot, now time.Time) {
			s.Message = &Message{ID: "m1", Showing: true, Text: "Mic 3 is muted", Color: "red", Bold: true}
			s.Room.OnAir = true
		}},
		{"clock", with(func(o *ViewOptions) { o.View = ViewClock }), nil},
		{"connecting", base, func(s *Snapshot, now time.Time) { *s = Snapshot{State: FeedConnecting, Since: now} }},
		{"refused", base, func(s *Snapshot, now time.Time) {
			*s = Snapshot{State: FeedRefused, Since: now, Detail: "Stagetimer: authorization: Not a valid Bearer token (HTTP 400)"}
		}},
		{"reconnecting", base, func(s *Snapshot, now time.Time) { s.State, s.Since = FeedOffline, now.Add(-time.Minute) }},
	}
}

// runDemo writes every demo scene at w×h into dir.
func runDemo(dir string, w, h int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	now := time.Now()
	hint := "Settings: http://192.168.1.50/ › AV Setup › Stagetimer"
	for _, sc := range demoScenes() {
		snap := demoSnapshot(now)
		if sc.edit != nil {
			sc.edit(&snap, now)
		}
		t0 := time.Now()
		img := NewRenderer(w, h).Draw(BuildModel(snap, now, sc.opts, hint))
		took := time.Since(t0)
		path := filepath.Join(dir, sc.name+".png")
		if err := writePNG(path, img); err != nil {
			return err
		}
		fmt.Printf("%-16s %s  (%v)\n", sc.name, path, took.Round(time.Millisecond))
	}
	return nil
}
