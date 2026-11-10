package main

import (
	"testing"
	"time"
)

func TestFormatCountdown(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Minute:                  "10:00",
		10*time.Minute - time.Millisecond: "10:00", // rounds up
		9*time.Minute + 59*time.Second:    "9:59",
		time.Second:                       "0:01",
		time.Millisecond:                  "0:01",
		0:                                 "0:00",
		-500 * time.Millisecond:           "0:00", // the second at zero
		-time.Second:                      "-0:01",
		-61 * time.Second:                 "-1:01",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03",
		-(time.Hour + time.Second):                "-1:00:01",
	}
	for d, want := range cases {
		if got := FormatCountdown(d); got != want {
			t.Errorf("FormatCountdown(%v) = %q, want %q", d, got, want)
		}
	}
	if FormatCountUp(61*time.Second+900*time.Millisecond) != "1:01" || FormatCountUp(-time.Second) != "0:00" {
		t.Error("FormatCountUp")
	}
}

func TestFormatClock(t *testing.T) {
	tm := time.Date(2026, 11, 10, 15, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		h24, sec bool
		want     string
	}{{true, true, "15:04:05"}, {true, false, "15:04"}, {false, true, "3:04:05 PM"}, {false, false, "3:04 PM"}} {
		if got := FormatClock(tm, c.h24, c.sec); got != c.want {
			t.Errorf("FormatClock(%v,%v) = %q", c.h24, c.sec, got)
		}
	}
}

func snapWith(st PlaybackStatus, tm Timer) Snapshot {
	return Snapshot{State: FeedLive, HaveStatus: true, HaveRoom: true, Status: st, Current: &tm, Timers: []Timer{tm}}
}

func TestTimerAt(t *testing.T) {
	now := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	ms := now.UnixMilli()
	tm := Timer{ID: "a", Name: "Talk", Minutes: 10, WrapUpYellow: 120, WrapUpRed: 30, Appearance: "COUNTDOWN"}
	min := int64(60_000)

	// Running with 5 minutes left.
	v := TimerAt(snapWith(PlaybackStatus{TimerID: "a", Running: true, Start: ms - 5*min, Finish: ms + 5*min}, tm), now, time.UTC, true)
	if v.Main != "5:00" || v.Phase != PhaseGreen || !v.Running || v.Progress < 0.49 || v.Progress > 0.51 {
		t.Errorf("running: %+v", v)
	}
	if v.YellowFrac != 0.8 || v.RedFrac != 0.95 {
		t.Errorf("zones %v %v", v.YellowFrac, v.RedFrac)
	}

	// Phases.
	for left, want := range map[time.Duration]Phase{
		121 * time.Second: PhaseGreen, 120 * time.Second: PhaseYellow, 31 * time.Second: PhaseYellow,
		30 * time.Second: PhaseRed, time.Millisecond: PhaseRed, 0: PhaseOver, -time.Minute: PhaseOver,
	} {
		st := PlaybackStatus{TimerID: "a", Running: true, Finish: ms + left.Milliseconds()}
		st.Start = st.Finish - 10*min
		if got := TimerAt(snapWith(st, tm), now, time.UTC, true).Phase; got != want {
			t.Errorf("%v left: phase %v, want %v", left, got, want)
		}
	}

	// Paused with 3:20 left, an hour after it was paused.
	pause := ms - 60*min
	v = TimerAt(snapWith(PlaybackStatus{TimerID: "a", Start: pause - 6*min - 40_000, Finish: pause + 3*min + 20_000, Pause: pause}, tm), now, time.UTC, true)
	if v.Main != "3:20" || v.Running || !v.Started {
		t.Errorf("paused: %+v", v)
	}
	m := BuildModel(snapWith(PlaybackStatus{TimerID: "a", Start: pause - 6*min - 40_000, Finish: pause + 3*min + 20_000, Pause: pause}, tm), now, DefaultViewOptions(), "")
	if m.State != "PAUSED" || !m.Dim {
		t.Errorf("paused model: state %q dim %v", m.State, m.Dim)
	}

	// Reset (Stagetimer sets start = pause): full length, not paused.
	v = TimerAt(snapWith(PlaybackStatus{TimerID: "a", Start: ms, Finish: ms + 10*min, Pause: ms}, tm), now, time.UTC, true)
	if v.Main != "10:00" || v.Started {
		t.Errorf("reset: %+v", v)
	}

	// Our clock 4 s slow: the offset puts it right.
	s := snapWith(PlaybackStatus{TimerID: "a", Running: true, Start: ms - 5*min, Finish: ms + 5*min}, tm)
	s.Offset = 4 * time.Second
	if v := TimerAt(s, now.Add(-4*time.Second), time.UTC, true); v.Main != "5:00" {
		t.Errorf("with offset: %q", v.Main)
	}

	// Appearances.
	london, _ := time.LoadLocation("Europe/London")
	run := PlaybackStatus{TimerID: "a", Running: true, Start: ms - 2*min, Finish: ms + 8*min}
	for app, want := range map[string][2]string{
		"COUNTDOWN":     {"8:00", ""},
		"COUNTUP":       {"2:00", ""},
		"TOD":           {"14:00:00", ""},
		"COUNTDOWN_TOD": {"8:00", "14:00"},
		"COUNTUP_TOD":   {"2:00", "14:00"},
		"HIDDEN":        {"", ""},
		"":              {"8:00", ""},
	} {
		tt := tm
		tt.Appearance = app
		v := TimerAt(snapWith(run, tt), now, london, true)
		if v.Main != want[0] || v.Sub != want[1] {
			t.Errorf("%q: main %q sub %q, want %q %q", app, v.Main, v.Sub, want[0], want[1])
		}
	}

	// No timer selected.
	if v := TimerAt(Snapshot{HaveStatus: true}, now, time.UTC, true); v.Has {
		t.Error("no timer, but Has")
	}
}

func TestAgenda(t *testing.T) {
	loc := time.UTC
	at := func(s string) *string { return &s }
	s := Snapshot{
		HaveStatus: true,
		Status:     PlaybackStatus{TimerID: "b"},
		Timers: []Timer{
			{ID: "a", Name: "Doors", Minutes: 30, StartTime: at("2026-11-10T09:00:00.000Z")},
			{ID: "b", Name: "Welcome", Minutes: 10},
			{ID: "c", Name: "Keynote", Duration: "0:45:00"},
			{ID: "d", Name: "Lunch", Type: "FINISH_TIME", StartTime: at("2026-11-10T11:00:00.000Z"), FinishTime: at("2026-11-10T12:15:00.000Z")},
			{ID: "e", Name: "Panel", Minutes: 40},
		},
	}
	rows := Agenda(s, loc)
	want := []struct {
		start    string
		estimate bool
		length   time.Duration
	}{
		{"09:00", false, 30 * time.Minute},
		{"09:30", true, 10 * time.Minute},
		{"09:40", true, 45 * time.Minute},
		{"11:00", false, 75 * time.Minute},
		{"12:15", true, 40 * time.Minute},
	}
	for i, w := range want {
		r := rows[i]
		if got := r.Start.Format("15:04"); got != w.start || r.Estimate != w.estimate || r.Length != w.length {
			t.Errorf("row %d (%s): %s est %v len %v; want %s %v %v", i, r.Timer.Name, got, r.Estimate, r.Length, w.start, w.estimate, w.length)
		}
	}
	if !rows[0].Past || !rows[1].Current || !rows[2].Next || rows[3].Next {
		t.Errorf("flags: %+v", rows)
	}

	// Nothing fixes the first start: no times at all until one is set.
	s.Timers[0].StartTime = nil
	rows = Agenda(s, loc)
	if !rows[0].Start.IsZero() || !rows[2].Start.IsZero() || rows[3].Start.Format("15:04") != "11:00" {
		t.Errorf("unanchored: %v %v %v", rows[0].Start, rows[2].Start, rows[3].Start)
	}
}

func TestParseHMS(t *testing.T) {
	for in, want := range map[string]time.Duration{"0:01:00": time.Minute, "1:30:00": 90 * time.Minute, "05:30": 5*time.Minute + 30*time.Second} {
		if d, ok := parseHMS(in); !ok || d != want {
			t.Errorf("parseHMS(%q) = %v %v", in, d, ok)
		}
	}
	for _, bad := range []string{"", "5", "a:b", "1:2:3:4", "-1:00"} {
		if _, ok := parseHMS(bad); ok {
			t.Errorf("parseHMS(%q) accepted", bad)
		}
	}
	// Duration wins over the fields; the fields stand in when it is absent.
	if (Timer{Duration: "0:02:00", Minutes: 9}).Length() != 2*time.Minute || (Timer{Hours: 1, Seconds: 5}).Length() != time.Hour+5*time.Second {
		t.Error("Length")
	}
}
