package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stoatworks-labs/bd-play-stagetimer/internal/mockstage"
)

func TestParseEvent(t *testing.T) {
	cases := []struct {
		in, name, data string
		ok             bool
	}{
		{`["playback_status",{"running":true}]`, "playback_status", `{"running":true}`, true},
		{`/v1,["room",{"name":"A"}]`, "room", `{"name":"A"}`, true},
		{`12["flash",{"count":2}]`, "flash", `{"count":2}`, true},
		{`/ns,7["message",null]`, "message", `null`, true},
		{`["timers_changed"]`, "timers_changed", `null`, true},
		{`["a",1,2]`, "a", `1`, true},
		{`[]`, "", "", false},
		{`[1,2]`, "", "", false},
		{`/nocomma`, "", "", false},
		{`not json`, "", "", false},
	}
	for _, c := range cases {
		name, data, ok := parseEvent([]byte(c.in))
		if ok != c.ok || name != c.name || (ok && string(data) != c.data) {
			t.Errorf("parseEvent(%s) = %q %s %v, want %q %s %v", c.in, name, data, ok, c.name, c.data, c.ok)
		}
	}
}

func TestSocketURL(t *testing.T) {
	cases := map[string]string{
		"https://api.stagetimer.io":    "wss://api.stagetimer.io/v1/socket.io/?EIO=4&transport=websocket",
		"https://api.stagetimer.io/":   "wss://api.stagetimer.io/v1/socket.io/?EIO=4&transport=websocket",
		"http://127.0.0.1:8097":        "ws://127.0.0.1:8097/v1/socket.io/?EIO=4&transport=websocket",
		"https://proxy.example/stage/": "wss://proxy.example/stage/v1/socket.io/?EIO=4&transport=websocket",
	}
	for in, want := range cases {
		got, err := socketURL(in)
		if err != nil || got != want {
			t.Errorf("socketURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := socketURL("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

// fixture is a mock Stagetimer on a test server.
type fixture struct {
	mock *mockstage.Server
	srv  *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	m := mockstage.New("room1", "key-abcdef-1234")
	m.PingInterval = 100 * time.Millisecond
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	return &fixture{mock: m, srv: srv}
}

func (f *fixture) creds() Credentials {
	return Credentials{RoomID: "room1", APIKey: "key-abcdef-1234", Base: f.srv.URL}
}

func fastFeed(c Credentials) *Feed {
	f := NewFeed(c, newHTTPClient(), NewLogger())
	f.PollEvery, f.PollFor = 50*time.Millisecond, 2*time.Second
	f.RefusedWait, f.FirstBackoff, f.MaxBackoff = 200*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	f.TimerDebounce = 50 * time.Millisecond
	return f
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestClientEndpoints(t *testing.T) {
	fx := newFixture(t)
	cl := NewClient(fx.creds(), newHTTPClient())
	ctx := context.Background()
	if err := cl.TestAuth(ctx); err != nil {
		t.Fatal(err)
	}
	room, err := cl.Room(ctx)
	if err != nil || room.Name != "Mock room" || room.Timezone != "Europe/London" {
		t.Fatalf("room %+v %v", room, err)
	}
	ts, err := cl.Timers(ctx)
	if err != nil || len(ts) != 3 || ts[1].Name != "Keynote" || ts[1].Length() != 25*time.Minute {
		t.Fatalf("timers %+v %v", ts, err)
	}
	st, err := cl.Status(ctx)
	if err != nil || st.TimerID != "t1" || st.Running || st.ServerTime == 0 {
		t.Fatalf("status %+v %v", st, err)
	}
	ms, err := cl.Messages(ctx)
	if err != nil || len(ms) != 2 {
		t.Fatalf("messages %+v %v", ms, err)
	}
	if cl.RateRemaining() != 99 {
		t.Errorf("rate remaining %d", cl.RateRemaining())
	}

	bad := NewClient(Credentials{RoomID: "room1", APIKey: "wrong-key-000", Base: fx.srv.URL}, newHTTPClient())
	err = bad.TestAuth(ctx)
	if !isRefused(err) {
		t.Fatalf("bad key: %v, want refused", err)
	}
	other := NewClient(Credentials{RoomID: "nope", APIKey: "key-abcdef-1234", Base: fx.srv.URL}, newHTTPClient())
	if err := other.TestAuth(ctx); !isRefused(err) {
		t.Fatalf("bad room: %v, want refused", err)
	}
}

func TestFeedLive(t *testing.T) {
	fx := newFixture(t)
	f := fastFeed(fx.creds())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)

	waitFor(t, "live with state", 3*time.Second, func() bool {
		s := f.Snapshot()
		return s.State == FeedLive && s.HaveRoom && s.HaveStatus && s.Current != nil && len(s.Timers) == 3
	})
	s := f.Snapshot()
	if s.Current.ID != "t1" || s.Next == nil || s.Next.ID != "t2" || s.Message != nil {
		t.Fatalf("initial state: cur %+v next %+v msg %+v", s.Current, s.Next, s.Message)
	}

	fx.mock.SetRemaining(90 * time.Second)
	fx.mock.Start()
	waitFor(t, "running", 2*time.Second, func() bool { return f.Snapshot().Status.Running })
	tv := TimerAt(f.Snapshot(), time.Now(), time.UTC, true)
	if tv.Main != "1:30" && tv.Main != "1:29" {
		t.Errorf("countdown reads %q, want about 1:30", tv.Main)
	}

	fx.mock.ShowMessage(1, true)
	waitFor(t, "message", 2*time.Second, func() bool { m := f.Snapshot().Message; return m != nil && m.Showing && m.Focus })
	fx.mock.HideMessage()
	waitFor(t, "message hidden", 2*time.Second, func() bool { m := f.Snapshot().Message; return m != nil && !m.Showing })

	fx.mock.Flash(2)
	waitFor(t, "flash", 2*time.Second, func() bool { return f.Snapshot().FlashCount == 2 })

	fx.mock.SetRoom(func(r *mockstage.Room) { r.Blackout = true })
	waitFor(t, "blackout", 2*time.Second, func() bool { return f.Snapshot().Room.Blackout })

	// An edit to the rundown: the socket says timers_changed and the feed
	// reads the rundown again over HTTP.
	before := fx.mock.Requests("get_all_timers")
	fx.mock.RenameTimer(2, "Panel: what changed")
	waitFor(t, "rundown re-read", 3*time.Second, func() bool {
		s := f.Snapshot()
		return fx.mock.Requests("get_all_timers") > before && len(s.Timers) == 3 && s.Timers[2].Name == "Panel: what changed"
	})

	fx.mock.Next()
	waitFor(t, "next timer", 2*time.Second, func() bool { s := f.Snapshot(); return s.Current != nil && s.Current.ID == "t2" })

	// Pings were answered: the mock drops a client that misses one.
	waitFor(t, "pongs", 2*time.Second, func() bool { return fx.mock.Pongs() >= 3 })
	if f.Snapshot().State != FeedLive {
		t.Errorf("state %s after pings", f.Snapshot().State)
	}

	// Dropped by the server: back up, with the state resent.
	fx.mock.Kick()
	waitFor(t, "reconnected", 3*time.Second, func() bool { return fx.mock.Clients() == 1 && f.Snapshot().State == FeedLive })
}

func TestFeedRefused(t *testing.T) {
	fx := newFixture(t)
	c := fx.creds()
	c.APIKey = "not-the-key-1"
	f := fastFeed(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	waitFor(t, "refused", 3*time.Second, func() bool { return f.Snapshot().State == FeedRefused })
	if s := f.Snapshot(); !strings.Contains(s.Detail, "Access Denied") {
		t.Errorf("detail %q", s.Detail)
	}
	m := BuildModel(f.Snapshot(), time.Now(), DefaultViewOptions(), "hint")
	if !strings.Contains(m.Notice, "refused") {
		t.Errorf("notice %q", m.Notice)
	}
}

func TestFeedFallsBackToPolling(t *testing.T) {
	fx := newFixture(t)
	fx.mock.SetBlocked(true)
	f := fastFeed(fx.creds())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)

	waitFor(t, "polling with state", 5*time.Second, func() bool {
		s := f.Snapshot()
		return s.State == FeedPolling && s.HaveStatus && s.HaveRoom && s.Current != nil && s.Current.ID == "t1"
	})
	fx.mock.Next()
	fx.mock.ShowMessage(0, false)
	waitFor(t, "polled changes", 3*time.Second, func() bool {
		s := f.Snapshot()
		return s.Current != nil && s.Current.ID == "t2" && s.Next != nil && s.Next.ID == "t3" && s.Message != nil && s.Message.Text == "Five minutes"
	})

	// The socket comes back: after the polling spell the feed is live again.
	fx.mock.SetBlocked(false)
	waitFor(t, "live again", 5*time.Second, func() bool { return f.Snapshot().State == FeedLive })
}

func TestFeedOfflineKeepsCounting(t *testing.T) {
	fx := newFixture(t)
	f := fastFeed(fx.creds())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	fx.mock.SetRemaining(10 * time.Minute)
	fx.mock.Start()
	waitFor(t, "running", 3*time.Second, func() bool { s := f.Snapshot(); return s.State == FeedLive && s.Status.Running })

	fx.mock.SetDown(true)
	waitFor(t, "lost", 3*time.Second, func() bool { st := f.Snapshot().State; return st != FeedLive })
	s := f.Snapshot()
	tv := TimerAt(s, time.Now(), time.UTC, true)
	if !tv.Running || tv.Remaining < 9*time.Minute+50*time.Second {
		t.Errorf("after losing the server: running %v, %q", tv.Running, tv.Main)
	}
	// Whichever way the feed is trying (socket or HTTP), a while later the
	// timer is still there, with a mark that it is not being confirmed.
	m := BuildModel(s, time.Now().Add(11*time.Second), DefaultViewOptions(), "")
	if m.Notice != "" || m.Conn == "" || !m.HasTimer {
		t.Errorf("want the timer with a reconnecting mark, got state %s notice %q conn %q", s.State, m.Notice, m.Conn)
	}
	waitFor(t, "polling, failing", 5*time.Second, func() bool {
		s := f.Snapshot()
		return s.State == FeedPolling && strings.Contains(s.Detail, "503")
	})
	m = BuildModel(f.Snapshot(), time.Now().Add(11*time.Second), DefaultViewOptions(), "")
	if m.Conn == "" {
		t.Error("failing polls: no reconnecting mark")
	}
	fx.mock.SetDown(false)
	waitFor(t, "back", 6*time.Second, func() bool {
		s := f.Snapshot()
		return s.State == FeedLive || (s.State == FeedPolling && !strings.Contains(s.Detail, "503"))
	})
}

func TestOffsetTracker(t *testing.T) {
	now := time.Now()
	var o offsetTracker
	if o.Offset() != 0 {
		t.Fatal("empty tracker has an offset")
	}
	// Server 3 s ahead; Date headers are whole seconds, so ±0.5 s.
	for i := 0; i < 5; i++ {
		srv := now.Add(3*time.Second + time.Duration(i)*137*time.Millisecond)
		o.addDate(srv.Truncate(time.Second), now)
	}
	if d := o.Offset(); d < 2*time.Second || d > 4*time.Second {
		t.Fatalf("date-only offset %v", d)
	}
	// server_time agreeing with the Date estimate: the largest sample wins
	// (each undershoots by its network delay).
	o.addServerTime(now.Add(3*time.Second-80*time.Millisecond).UnixMilli(), now)
	o.addServerTime(now.Add(3*time.Second-20*time.Millisecond).UnixMilli(), now)
	if d := o.Offset(); d < 2950*time.Millisecond || d > 3*time.Second {
		t.Fatalf("with server_time: %v", d)
	}

	// server_time far from the Date estimate (a stale status, say) is ignored.
	var p offsetTracker
	p.addDate(now.Add(time.Second).Truncate(time.Second), now)
	p.addServerTime(now.Add(-40*time.Second).UnixMilli(), now)
	if d := p.Offset(); d < 0 || d > 2*time.Second {
		t.Fatalf("stale server_time trusted: %v", d)
	}
}

func TestSnapshotJSONHasNoSecrets(t *testing.T) {
	s := Snapshot{State: FeedLive, Room: Room{Name: "Secret room"}, Offset: 1500 * time.Millisecond}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Secret") || !strings.Contains(string(b), `"clock_offset_ms":1500`) || strings.Contains(string(b), "last_data") {
		t.Errorf("snapshot JSON %s", b)
	}
}
