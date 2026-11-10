package main

// What is on screen, as data. The renderer draws a Model; the screen loop
// compares one Model's Key with the last and only draws (and converts, and
// pipes to HDMI) when they differ — which for a running countdown is about
// once a second, instead of ten times.

import (
	"fmt"
	"strings"
	"time"
)

// View names, as the config and the tab spell them.
const (
	ViewTimer  = "timer"  // the speaker's countdown: Stagetimer's Viewer
	ViewAgenda = "agenda" // the rundown, with the current item counting down
	ViewSplit  = "split"  // the countdown beside the rundown
	ViewClock  = "clock"  // the time of day in the room's time zone
)

var viewNames = []string{ViewTimer, ViewAgenda, ViewSplit, ViewClock}

func validView(v string) bool {
	for _, n := range viewNames {
		if n == v {
			return true
		}
	}
	return false
}

// ViewOptions are the display settings.
type ViewOptions struct {
	View         string `json:"view"`
	ShowTitle    bool   `json:"show_title"`
	ShowSpeaker  bool   `json:"show_speaker"`
	ShowProgress bool   `json:"show_progress"`
	ShowNext     bool   `json:"show_next"`
	ShowClock    bool   `json:"show_clock"`
	ShowMessages bool   `json:"show_messages"`
	Clock24h     bool   `json:"clock_24h"`
	Mirror       string `json:"mirror"` // "", "h", "v", "hv"
}

func DefaultViewOptions() ViewOptions {
	return ViewOptions{
		View: ViewTimer, ShowTitle: true, ShowSpeaker: true, ShowProgress: true,
		ShowNext: false, ShowClock: true, ShowMessages: true, Clock24h: true,
	}
}

type MsgModel struct {
	Show  bool
	Text  string
	Color string
	Bold  bool
	Focus bool
}

type RowModel struct {
	Start     string
	Estimate  bool
	Title     string
	Speaker   string
	Length    string
	Labels    []Label
	Current   bool
	Next      bool
	Past      bool
	Countdown string
	Phase     Phase
}

type Model struct {
	View   string
	Mirror string

	Blank bool // Stagetimer's blackout
	Flash bool // the inverted half of a flash
	OnAir bool
	Conn  string // a small connection warning, or ""

	Notice, NoticeDetail, Hint string // a whole-screen status instead of a view

	RoomName string
	Clock    string // the corner clock
	BigClock string
	DateLine string

	HasTimer     bool
	Title        string
	Speaker      string
	Next         string
	Main         string
	Sub          string
	State        string // PAUSED
	Phase        Phase
	Dim          bool
	ShowProgress bool
	Progress     int // thousandths
	YellowAt     int
	RedAt        int

	Msg  MsgModel
	Rows []RowModel
}

// Key identifies what the model would draw.
func (m Model) Key() string { return fmt.Sprintf("%+v", m) }

// BuildModel decides what the screen shows at now.
func BuildModel(s Snapshot, now time.Time, o ViewOptions, hint string) Model {
	m := Model{View: o.View, Mirror: o.Mirror, Hint: hint}
	if !validView(m.View) {
		m.View = ViewTimer
	}
	loc := RoomLocation(s)
	srv := ServerNow(s, now).In(loc)

	switch {
	case s.State == FeedRefused:
		m.Notice = "Stagetimer refused this room ID or API key"
		m.NoticeDetail = strings.TrimPrefix(s.Detail, "Stagetimer: ")
		return m
	case !s.HaveStatus && !s.HaveRoom:
		switch s.State {
		case FeedOffline:
			m.Notice = "Can’t reach Stagetimer"
			m.NoticeDetail = s.Detail + " — trying again"
		default:
			m.Notice = "Connecting to Stagetimer…"
		}
		return m
	}

	if s.HaveRoom && s.Room.Blackout {
		m.Blank = true
		return m
	}
	// The countdown keeps running from the last status either way; this only
	// says it is no longer being confirmed. Polling that keeps failing counts:
	// its state stays "polling", but nothing new has arrived.
	switch {
	case (s.State == FeedOffline || s.State == FeedConnecting) && now.Sub(s.Since) > 4*time.Second:
		m.Conn = "Reconnecting to Stagetimer"
	case s.State == FeedPolling && now.Sub(s.LastData) > 10*time.Second:
		m.Conn = "Reconnecting to Stagetimer"
	}
	if s.FlashCount > 0 {
		el := now.Sub(s.FlashAt)
		if el >= 0 && el < time.Duration(s.FlashCount)*time.Second && el%time.Second < 500*time.Millisecond {
			m.Flash = true
		}
	}
	m.OnAir = s.Room.OnAir
	m.RoomName = s.Room.Name
	if o.ShowClock {
		m.Clock = FormatClock(srv, o.Clock24h, false)
	}
	if m.View == ViewClock {
		m.BigClock = FormatClock(srv, o.Clock24h, true)
		m.DateLine = srv.Format("Monday 2 January")
	}

	tv := TimerAt(s, now, loc, o.Clock24h)
	if tv.Has {
		m.HasTimer = true
		if o.ShowTitle {
			m.Title = tv.Timer.Name
		}
		if o.ShowSpeaker {
			m.Speaker = tv.Timer.Speaker
		}
		if o.ShowNext && s.Next != nil && s.Next.Name != "" {
			m.Next = "Next: " + s.Next.Name
		}
		m.Main, m.Sub, m.Phase = tv.Main, tv.Sub, tv.Phase
		m.Dim = !tv.Running
		if !tv.Running && tv.Started {
			m.State = "PAUSED"
		}
		if o.ShowProgress && tv.Total > 0 && tv.Appearance != "TOD" && tv.Appearance != "HIDDEN" {
			m.ShowProgress = true
			m.Progress = int(shownProgress(tv) * 1000)
			m.YellowAt = int(tv.YellowFrac * 1000)
			m.RedAt = int(tv.RedFrac * 1000)
		}
	}

	if o.ShowMessages && s.Message != nil && s.Message.Showing && strings.TrimSpace(s.Message.Text) != "" {
		text := strings.TrimSpace(s.Message.Text)
		if s.Message.Uppercase {
			text = strings.ToUpper(text)
		}
		m.Msg = MsgModel{Show: true, Text: text, Color: strings.ToLower(s.Message.Color), Bold: s.Message.Bold, Focus: s.Message.Focus}
	}

	if m.View == ViewAgenda || m.View == ViewSplit {
		for _, row := range Agenda(s, loc) {
			rm := RowModel{
				Title: row.Timer.Name, Speaker: row.Timer.Speaker, Labels: row.Timer.Labels,
				Length: FormatCountUp(row.Length), Current: row.Current, Next: row.Next, Past: row.Past,
				Estimate: row.Estimate,
			}
			if !row.Start.IsZero() {
				rm.Start = FormatClock(row.Start, o.Clock24h, false)
			}
			if row.Current && tv.Has {
				rm.Countdown = FormatCountdown(tv.Remaining)
				rm.Phase = tv.Phase
			}
			m.Rows = append(m.Rows, rm)
		}
	}
	return m
}

// shownProgress is the bar's fill for the seconds on screen, so the bar moves
// when the digits do and not between: a thousandth of a five-minute timer is
// 0.3 s, and every change of the picture is a 3 MB frame down the pipe to HDMI.
func shownProgress(tv TimerView) float64 {
	if tv.Total <= 0 {
		return tv.Progress
	}
	if tv.Remaining <= 0 {
		return 1
	}
	rem := (tv.Remaining + time.Second - 1) / time.Second * time.Second
	return clamp01(1 - float64(rem)/float64(tv.Total))
}
