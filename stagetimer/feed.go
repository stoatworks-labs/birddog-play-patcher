package main

// The feed: one room's state, kept current.
//
// The socket is the normal path. On connect Stagetimer sends the room, the
// playback status, the current and next timer and the active message, and
// after that every change as it happens. It does not send the rundown, so the
// agenda is read over HTTP on connect and again whenever the socket says the
// timers changed (debounced: someone editing a rundown fires dozens).
//
// If the WebSocket will not stay up — some venue networks pass HTTPS and
// nothing else — the feed reads the same state over HTTP every two seconds for
// a couple of minutes, then tries the socket again. That costs about 56 of the
// key's 100 requests a minute, so it is the fallback, not the plan.
//
// While nothing arrives the countdown keeps running from the last status, as
// Stagetimer's own viewer does; the screen marks the connection instead of
// freezing.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

type FeedState string

const (
	FeedIdle       FeedState = "idle"
	FeedConnecting FeedState = "connecting"
	FeedLive       FeedState = "live"    // the socket is up
	FeedPolling    FeedState = "polling" // reading over HTTP instead
	FeedOffline    FeedState = "offline" // lost; trying again
	FeedRefused    FeedState = "refused" // the room ID or key was refused
)

// Snapshot is the room as last heard. Timers is replaced, never edited in
// place, so a copy of the struct is safe to read.
type Snapshot struct {
	State    FeedState `json:"state"`
	Detail   string    `json:"detail,omitempty"`
	Since    time.Time `json:"since"`
	LastData time.Time `json:"last_data"`

	HaveRoom   bool           `json:"-"`
	HaveStatus bool           `json:"-"`
	Room       Room           `json:"-"`
	Status     PlaybackStatus `json:"-"`
	Current    *Timer         `json:"-"`
	Next       *Timer         `json:"-"`
	Timers     []Timer        `json:"-"`
	Message    *Message       `json:"-"`
	FlashAt    time.Time      `json:"-"`
	FlashCount int            `json:"-"`
	// Offset is the server's clock minus ours.
	Offset time.Duration `json:"offset_ms"`
}

// MarshalJSON reports the offset in milliseconds.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type view struct {
		State    FeedState  `json:"state"`
		Detail   string     `json:"detail,omitempty"`
		Since    time.Time  `json:"since"`
		LastData *time.Time `json:"last_data,omitempty"`
		OffsetMS int64      `json:"clock_offset_ms"`
		Timers   int        `json:"timers"`
	}
	var last *time.Time
	if !s.LastData.IsZero() {
		last = &s.LastData
	}
	return json.Marshal(view{s.State, s.Detail, s.Since, last, s.Offset.Milliseconds(), len(s.Timers)})
}

type Feed struct {
	Creds Credentials
	HTTP  *http.Client
	Log   *Logger

	// Timing knobs, shortened by the tests.
	PollEvery     time.Duration
	PollFor       time.Duration
	RefusedWait   time.Duration
	FirstBackoff  time.Duration
	MaxBackoff    time.Duration
	TimerDebounce time.Duration

	mu     sync.Mutex
	snap   Snapshot
	clock  offsetTracker
	kick   chan struct{}
	client *Client
	seen   map[string]bool
}

func NewFeed(c Credentials, hc *http.Client, log *Logger) *Feed {
	f := &Feed{
		Creds: c, HTTP: hc, Log: log,
		PollEvery: 2 * time.Second, PollFor: 2 * time.Minute,
		RefusedWait: time.Minute, FirstBackoff: time.Second, MaxBackoff: 30 * time.Second,
		TimerDebounce: 1500 * time.Millisecond,
		kick:          make(chan struct{}, 1),
		seen:          map[string]bool{},
	}
	f.snap = Snapshot{State: FeedIdle, Since: time.Now()}
	f.client = NewClient(c, hc)
	f.client.OnDate = f.clock.addDate
	return f
}

func (f *Feed) Snapshot() Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	s.Offset = f.clock.Offset()
	return s
}

func (f *Feed) setState(st FeedState, detail string) {
	f.mu.Lock()
	changed := f.snap.State != st || f.snap.Detail != detail
	if f.snap.State != st {
		f.snap.Since = time.Now()
	}
	f.snap.State, f.snap.Detail = st, detail
	f.mu.Unlock()
	if changed {
		if detail != "" {
			f.Log.Info("stagetimer: %s — %s", st, detail)
		} else {
			f.Log.Info("stagetimer: %s", st)
		}
	}
}

// Run keeps the feed going until ctx ends.
func (f *Feed) Run(ctx context.Context) {
	backoff := f.FirstBackoff
	socketFails := 0
	for ctx.Err() == nil {
		f.setState(FeedConnecting, "")
		liveFor, err := f.runSocket(ctx)
		if ctx.Err() != nil {
			return
		}
		if isRefused(err) {
			f.setState(FeedRefused, err.Error())
			sleepCtx(ctx, f.RefusedWait)
			continue
		}
		if liveFor > time.Minute {
			socketFails, backoff = 0, f.FirstBackoff
		}
		socketFails++
		if socketFails >= 3 {
			f.Log.Warn("stagetimer: the socket failed %d times (%v); reading over HTTP for a while", socketFails, err)
			if perr := f.poll(ctx, f.PollFor); isRefused(perr) {
				f.setState(FeedRefused, perr.Error())
				sleepCtx(ctx, f.RefusedWait)
			}
			// One more socket failure goes straight back to polling.
			socketFails = 2
			continue
		}
		f.setState(FeedOffline, err.Error())
		sleepCtx(ctx, backoff)
		if backoff *= 2; backoff > f.MaxBackoff {
			backoff = f.MaxBackoff
		}
	}
}

func (f *Feed) runSocket(ctx context.Context) (time.Duration, error) {
	auth := map[string]string{"room_id": f.Creds.RoomID, "api_key": f.Creds.APIKey}
	conn, err := dialSocketIO(ctx, f.Creds.base(), auth, f.HTTP)
	if err != nil {
		return 0, err
	}
	up := time.Now()
	f.setState(FeedLive, "")
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go f.refreshTimers(sctx)
	err = conn.Run(sctx, f.onEvent)
	return time.Since(up), err
}

func (f *Feed) onEvent(name string, data json.RawMessage) {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap.LastData = now
	switch name {
	case "playback_status":
		var st PlaybackStatus
		if json.Unmarshal(data, &st) == nil {
			f.snap.Status, f.snap.HaveStatus = st, true
			f.clock.addServerTime(st.ServerTime, now)
		}
	case "room":
		var r Room
		if json.Unmarshal(data, &r) == nil {
			f.snap.Room, f.snap.HaveRoom = r, true
		}
	case "message":
		f.snap.Message = nil
		var m Message
		if string(data) != "null" && json.Unmarshal(data, &m) == nil {
			f.snap.Message = &m
		}
	case "current_timer", "next_timer":
		var t *Timer
		if string(data) != "null" {
			var v Timer
			if json.Unmarshal(data, &v) == nil {
				t = &v
			}
		}
		if name == "current_timer" {
			f.snap.Current = t
		} else {
			f.snap.Next = t
		}
		if t != nil {
			f.snap.Timers = replaceTimer(f.snap.Timers, *t)
		}
	case "flash":
		var fl struct {
			Count int `json:"count"`
		}
		_ = json.Unmarshal(data, &fl)
		if fl.Count <= 0 {
			fl.Count = 3
		}
		f.snap.FlashAt, f.snap.FlashCount = now, fl.Count
	case "timers_changed":
		select {
		case f.kick <- struct{}{}:
		default:
		}
	case "messages_changed":
		// The active message arrives as its own "message" event.
	default:
		if !f.seen[name] {
			f.seen[name] = true
			f.Log.Info("stagetimer: ignoring event %q", name)
		}
	}
}

// replaceTimer returns ts with t swapped in by ID (a copy; ts is shared with
// snapshots already handed out).
func replaceTimer(ts []Timer, t Timer) []Timer {
	for i := range ts {
		if ts[i].ID == t.ID {
			out := append([]Timer(nil), ts...)
			out[i] = t
			return out
		}
	}
	return ts
}

// refreshTimers reads the rundown now, again after each burst of
// timers_changed, and every five minutes regardless.
func (f *Feed) refreshTimers(ctx context.Context) {
	var last time.Time
	for {
		if wait := 3*time.Second - time.Since(last); wait > 0 && !last.IsZero() {
			sleepCtx(ctx, wait)
		}
		if ctx.Err() != nil {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		ts, err := f.client.Timers(rctx)
		cancel()
		last = time.Now()
		if err != nil {
			if ctx.Err() == nil {
				f.Log.Warn("stagetimer: reading the rundown: %v", err)
			}
		} else {
			f.mu.Lock()
			f.snap.Timers = ts
			f.mu.Unlock()
		}
		retry := 5 * time.Minute
		if err != nil {
			retry = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		case <-f.kick:
			// Let the burst finish.
			t := time.NewTimer(f.TimerDebounce)
		drain:
			for {
				select {
				case <-f.kick:
				case <-t.C:
					break drain
				case <-ctx.Done():
					t.Stop()
					return
				}
			}
		}
	}
}

// poll reads the room over HTTP for d.
func (f *Feed) poll(ctx context.Context, d time.Duration) error {
	f.setState(FeedPolling, "the WebSocket is not getting through; reading over HTTP")
	end := time.Now().Add(d)
	var lastRoom, lastMsg, lastTimers time.Time
	for time.Now().Before(end) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := f.pollOnce(rctx, &lastRoom, &lastMsg, &lastTimers)
		cancel()
		if err != nil {
			if isRefused(err) {
				return err
			}
			f.setState(FeedPolling, "reading over HTTP: "+err.Error())
		} else {
			f.setState(FeedPolling, "the WebSocket is not getting through; reading over HTTP")
		}
		every := f.PollEvery
		if r := f.client.RateRemaining(); r >= 0 && r < 15 {
			every *= 3 // leave the budget to whatever else uses this key
		}
		sleepCtx(ctx, every)
	}
	return nil
}

func (f *Feed) pollOnce(ctx context.Context, lastRoom, lastMsg, lastTimers *time.Time) error {
	now := time.Now()
	slow := 5 * f.PollEvery / 2
	st, err := f.client.Status(ctx)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.snap.Status, f.snap.HaveStatus, f.snap.LastData = st, true, time.Now()
	f.clock.addServerTime(st.ServerTime, time.Now())
	f.mu.Unlock()

	if now.Sub(*lastRoom) >= slow {
		r, err := f.client.Room(ctx)
		if err != nil {
			return err
		}
		*lastRoom = now
		f.mu.Lock()
		f.snap.Room, f.snap.HaveRoom = r, true
		f.mu.Unlock()
	}
	if now.Sub(*lastMsg) >= slow {
		ms, err := f.client.Messages(ctx)
		if err != nil {
			return err
		}
		*lastMsg = now
		var active *Message
		for i := range ms {
			if ms[i].Showing {
				m := ms[i]
				active = &m
				break
			}
		}
		f.mu.Lock()
		f.snap.Message = active
		f.mu.Unlock()
	}
	if now.Sub(*lastTimers) >= 15*f.PollEvery {
		ts, err := f.client.Timers(ctx)
		if err != nil {
			return err
		}
		*lastTimers = now
		f.mu.Lock()
		f.snap.Timers = ts
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.snap.Current, f.snap.Next = currentAndNext(f.snap.Timers, f.snap.Status.TimerID)
	f.mu.Unlock()
	return nil
}

// currentAndNext finds the selected timer in the rundown, and the one after.
func currentAndNext(ts []Timer, id string) (*Timer, *Timer) {
	for i := range ts {
		if ts[i].ID == id {
			cur := ts[i]
			if i+1 < len(ts) {
				nxt := ts[i+1]
				return &cur, &nxt
			}
			return &cur, nil
		}
	}
	return nil, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// offsetTracker estimates the server's clock minus ours, so the countdown is
// right on a PLAY whose own clock is not.
//
// Two sources. The HTTP Date header is honest but whole seconds, so each one
// is ±0.5 s. server_time in the playback status is milliseconds, and if it is
// the server's clock at sending, each sample undershoots by the network delay
// — so the largest recent one is the best. The API does not say server_time is
// "now" rather than "when this status last changed", so it is only trusted
// while it agrees with the Date header to within 1.5 s.
type offsetTracker struct {
	mu   sync.Mutex
	date []offsetSample
	st   []offsetSample
}

type offsetSample struct {
	est time.Duration
	at  time.Time
}

func pushSample(s []offsetSample, v offsetSample) []offsetSample {
	s = append(s, v)
	if len(s) > 6 {
		s = s[len(s)-6:]
	}
	return s
}

func (o *offsetTracker) addDate(server, local time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.date = pushSample(o.date, offsetSample{server.Add(500 * time.Millisecond).Sub(local), local})
}

func (o *offsetTracker) addServerTime(ms int64, local time.Time) {
	if ms <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.st = pushSample(o.st, offsetSample{time.UnixMilli(ms).Sub(local), local})
}

func recent(s []offsetSample, within time.Duration) []time.Duration {
	var out []time.Duration
	for _, v := range s {
		if time.Since(v.at) <= within {
			out = append(out, v.est)
		}
	}
	return out
}

func (o *offsetTracker) Offset() time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	dates := recent(o.date, 30*time.Minute)
	sts := recent(o.st, 10*time.Minute)
	var date time.Duration
	if len(dates) > 0 {
		sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })
		date = dates[len(dates)/2]
	}
	if len(sts) > 0 {
		best := sts[0]
		for _, v := range sts {
			if v > best {
				best = v
			}
		}
		// A clock that jumped (NTP at boot) leaves old samples wrong; with a
		// spread that large, believe only the newest.
		lo, hi := sts[0], sts[0]
		for _, v := range sts {
			lo, hi = min(lo, v), max(hi, v)
		}
		if hi-lo > 2*time.Second {
			best = sts[len(sts)-1]
		}
		if len(dates) == 0 || absDur(best-date) < 1500*time.Millisecond {
			return best
		}
	}
	return date
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
