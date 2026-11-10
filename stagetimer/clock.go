package main

// From the last status to what the screen says now.
//
// Stagetimer's API gives start, finish and pause on the server's clock and
// leaves the counting to the client: remaining is finish − now while running
// and finish − pause while stopped. A countdown shows whole seconds rounded
// up, so a ten-minute timer reads 10:00 for its first second and 0:01 for its
// last, then 0:00 for one second at zero, then counts the overtime up with a
// minus sign. The wrap-up colours come from the timer's own thresholds,
// in seconds before the end.

import (
	"fmt"
	"strings"
	"time"
)

type Phase int

const (
	PhaseNone   Phase = iota // no timer selected
	PhaseGreen               // plenty of time
	PhaseYellow              // inside the yellow wrap-up
	PhaseRed                 // inside the red wrap-up
	PhaseOver                // past zero
)

func (p Phase) String() string {
	return [...]string{"none", "green", "yellow", "red", "overtime"}[p]
}

type TimerView struct {
	Has        bool
	Timer      Timer
	Appearance string
	Running    bool
	Started    bool // has run at all since it was last reset
	Remaining  time.Duration
	Elapsed    time.Duration
	Total      time.Duration
	Phase      Phase
	Main       string // the big text
	Sub        string // a second line, for the *_TOD appearances
	Progress   float64
	YellowFrac float64 // where the yellow zone starts, as a fraction of Total
	RedFrac    float64
}

// ServerNow is our clock corrected to the server's.
func ServerNow(s Snapshot, now time.Time) time.Time { return now.Add(s.Offset) }

// TimerAt works out the selected timer's state at now (our clock).
func TimerAt(s Snapshot, now time.Time, loc *time.Location, h24 bool) TimerView {
	v := TimerView{}
	if !s.HaveStatus || s.Status.TimerID == "" {
		return v
	}
	v.Has = true
	if s.Current != nil && s.Current.ID == s.Status.TimerID {
		v.Timer = *s.Current
	} else if cur, _ := currentAndNext(s.Timers, s.Status.TimerID); cur != nil {
		v.Timer = *cur
	}
	v.Appearance = strings.ToUpper(v.Timer.Appearance)
	if v.Appearance == "" {
		v.Appearance = "COUNTDOWN"
	}
	st := s.Status
	srv := ServerNow(s, now).UnixMilli()
	v.Running = st.Running
	v.Total = time.Duration(st.Finish-st.Start) * time.Millisecond
	if v.Total <= 0 {
		v.Total = v.Timer.Length()
	}
	switch {
	case st.Running:
		v.Remaining = time.Duration(st.Finish-srv) * time.Millisecond
		v.Started = true
	case st.Pause != 0:
		v.Remaining = time.Duration(st.Finish-st.Pause) * time.Millisecond
		v.Started = st.Pause > st.Start
	default:
		v.Remaining = v.Total
	}
	if st.Finish == 0 && st.Start == 0 {
		v.Remaining = v.Total
	}
	v.Elapsed = v.Total - v.Remaining
	if v.Total > 0 {
		v.Progress = clamp01(float64(v.Elapsed) / float64(v.Total))
		y := time.Duration(v.Timer.WrapUpYellow) * time.Second
		r := time.Duration(v.Timer.WrapUpRed) * time.Second
		v.YellowFrac, v.RedFrac = 1, 1
		if y > 0 {
			v.YellowFrac = clamp01(1 - float64(y)/float64(v.Total))
		}
		if r > 0 {
			v.RedFrac = clamp01(1 - float64(r)/float64(v.Total))
		}
	}
	v.Phase = phaseOf(v.Remaining, v.Timer)

	tod := ServerNow(s, now).In(loc)
	switch v.Appearance {
	case "COUNTUP":
		v.Main = FormatCountUp(v.Elapsed)
	case "TOD":
		v.Main = FormatClock(tod, h24, true)
	case "COUNTDOWN_TOD":
		v.Main = FormatCountdown(v.Remaining)
		v.Sub = FormatClock(tod, h24, false)
	case "COUNTUP_TOD":
		v.Main = FormatCountUp(v.Elapsed)
		v.Sub = FormatClock(tod, h24, false)
	case "HIDDEN":
		v.Main = ""
	default:
		v.Main = FormatCountdown(v.Remaining)
	}
	return v
}

func phaseOf(remaining time.Duration, t Timer) Phase {
	switch {
	case remaining <= 0:
		return PhaseOver
	case t.WrapUpRed > 0 && remaining <= time.Duration(t.WrapUpRed)*time.Second:
		return PhaseRed
	case t.WrapUpYellow > 0 && remaining <= time.Duration(t.WrapUpYellow)*time.Second:
		return PhaseYellow
	}
	return PhaseGreen
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// FormatCountdown shows remaining time: whole seconds rounded up while there
// is time left, and the overtime with a minus once there is not.
func FormatCountdown(d time.Duration) string {
	if d > 0 {
		secs := int64((d + time.Second - 1) / time.Second)
		return hms(secs)
	}
	over := int64(-d / time.Second)
	if over == 0 {
		return hms(0)
	}
	return "-" + hms(over)
}

// FormatCountUp shows elapsed time, rounded down.
func FormatCountUp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return hms(int64(d / time.Second))
}

func hms(secs int64) string {
	h, m, s := secs/3600, secs/60%60, secs%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// FormatClock is the time of day.
func FormatClock(t time.Time, h24, seconds bool) string {
	switch {
	case h24 && seconds:
		return t.Format("15:04:05")
	case h24:
		return t.Format("15:04")
	case seconds:
		return t.Format("3:04:05 PM")
	default:
		return t.Format("3:04 PM")
	}
}

// RoomLocation is the room's time zone, else the box's.
func RoomLocation(s Snapshot) *time.Location {
	if s.HaveRoom && s.Room.Timezone != "" {
		if loc, err := time.LoadLocation(s.Room.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// AgendaRow is one rundown line with its planned start worked out.
type AgendaRow struct {
	Timer    Timer
	Start    time.Time // zero when nothing fixes it
	Length   time.Duration
	Current  bool
	Next     bool
	Past     bool
	Estimate bool // Start was chained from the row before, not set
}

// Agenda lays the rundown out. A timer with its own start time keeps it; one
// without starts when the one before it ends, if that is known — which is how
// a rundown reads on paper.
func Agenda(s Snapshot, loc *time.Location) []AgendaRow {
	rows := make([]AgendaRow, len(s.Timers))
	cur := -1
	for i, t := range s.Timers {
		rows[i] = AgendaRow{Timer: t, Length: t.Length()}
		if t.ID == s.Status.TimerID && s.HaveStatus {
			cur = i
		}
	}
	var prevEnd time.Time
	for i := range rows {
		if st, ok := ClockTime(rows[i].Timer.StartTime); ok {
			rows[i].Start = st.In(loc)
		} else if !prevEnd.IsZero() {
			rows[i].Start, rows[i].Estimate = prevEnd, true
		}
		if ft, ok := ClockTime(rows[i].Timer.FinishTime); ok && strings.EqualFold(rows[i].Timer.Type, "FINISH_TIME") {
			prevEnd = ft.In(loc)
			if !rows[i].Start.IsZero() && prevEnd.After(rows[i].Start) {
				rows[i].Length = prevEnd.Sub(rows[i].Start)
			}
		} else if !rows[i].Start.IsZero() {
			prevEnd = rows[i].Start.Add(rows[i].Length)
		} else {
			prevEnd = time.Time{}
		}
		rows[i].Current = i == cur
		rows[i].Next = cur >= 0 && i == cur+1
		rows[i].Past = cur >= 0 && i < cur
	}
	return rows
}
