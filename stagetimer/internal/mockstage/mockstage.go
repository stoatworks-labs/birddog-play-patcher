// Package mockstage is a stand-in for Stagetimer's API v1: the HTTP routes
// bdstage reads and the Socket.IO feed, built from the public documentation
// and the handshake api.stagetimer.io gave on 2026-11-10. It is what the tests
// and tools/mockstage drive bdstage against; it is not Stagetimer, and where
// the two differ, Stagetimer is right.
package mockstage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Timer struct {
	ID           string `json:"_id"`
	Model        string `json:"_model"`
	Name         string `json:"name"`
	Speaker      string `json:"speaker"`
	Notes        string `json:"notes"`
	Appearance   string `json:"appearance"`
	Type         string `json:"type"`
	Duration     string `json:"duration"`
	Hours        int    `json:"hours"`
	Minutes      int    `json:"minutes"`
	Seconds      int    `json:"seconds"`
	WrapUpYellow int    `json:"wrap_up_yellow"`
	WrapUpRed    int    `json:"wrap_up_red"`
	Trigger      string `json:"trigger"`
	StartTime    any    `json:"start_time"`
	FinishTime   any    `json:"finish_time"`
	Labels       []any  `json:"labels"`
}

func (t Timer) length() time.Duration {
	return time.Duration(t.Hours)*time.Hour + time.Duration(t.Minutes)*time.Minute + time.Duration(t.Seconds)*time.Second
}

type Status struct {
	Model      string `json:"_model"`
	TimerID    string `json:"timer_id"`
	Running    bool   `json:"running"`
	Start      int64  `json:"start"`
	Finish     int64  `json:"finish"`
	Pause      int64  `json:"pause"`
	ServerTime int64  `json:"server_time"`
}

type Room struct {
	ID           string `json:"_id"`
	Model        string `json:"_model"`
	Name         string `json:"name"`
	Blackout     bool   `json:"blackout"`
	FocusMessage bool   `json:"focus_message"`
	OnAir        bool   `json:"on_air"`
	Logo         string `json:"logo"`
	Timezone     string `json:"timezone"`
	Date         any    `json:"date"`
}

type Message struct {
	ID        string `json:"_id"`
	Model     string `json:"_model"`
	Showing   bool   `json:"showing"`
	Text      string `json:"text"`
	Color     string `json:"color"`
	Bold      bool   `json:"bold"`
	Uppercase bool   `json:"uppercase"`
	Focus     bool   `json:"focus,omitempty"`
}

type Server struct {
	RoomID string
	Key    string
	// PingInterval is what the engine OPEN advertises and how often it pings.
	PingInterval time.Duration
	// Clock lets a test move the server's time; time.Now by default.
	Clock func() time.Time

	mu       sync.Mutex
	room     Room
	timers   []Timer
	cur      int
	status   Status
	messages []Message
	clients  map[chan string]struct{}
	requests map[string]int
	blocked  bool
	down     bool
	pongs    int
}

// Pongs is how many pings clients have answered.
func (s *Server) Pongs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pongs
}

// SetBlocked makes the WebSocket upgrade fail, like a venue proxy that passes
// HTTPS and nothing else, and drops the sockets already up.
func (s *Server) SetBlocked(b bool) {
	s.mu.Lock()
	s.blocked = b
	s.mu.Unlock()
	if b {
		s.Kick()
	}
}

func New(roomID, key string) *Server {
	s := &Server{
		RoomID: roomID, Key: key, PingInterval: 25 * time.Second,
		room:     Room{ID: roomID, Model: "room", Name: "Mock room", Timezone: "Europe/London"},
		clients:  map[chan string]struct{}{},
		requests: map[string]int{},
	}
	s.timers = []Timer{
		s.timer("t1", "Welcome", "Host", 5, 60, 15),
		s.timer("t2", "Keynote", "Priya Raman", 25, 300, 60),
		s.timer("t3", "Panel", "Sam, Lena, Tom", 40, 300, 60),
	}
	s.messages = []Message{
		{ID: "m1", Model: "message", Text: "Five minutes", Color: "green", Bold: true},
		{ID: "m2", Model: "message", Text: "Wrap up", Color: "red", Bold: true, Uppercase: true},
	}
	s.selectLocked(0)
	return s
}

func (s *Server) timer(id, name, speaker string, minutes, yellow, red int) Timer {
	return Timer{ID: id, Model: "timer", Name: name, Speaker: speaker, Appearance: "COUNTDOWN", Type: "DURATION",
		Duration: fmt.Sprintf("0:%02d:00", minutes), Minutes: minutes, WrapUpYellow: yellow, WrapUpRed: red, Trigger: "MANUAL", Labels: []any{}}
}

func (s *Server) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Server) selectLocked(i int) {
	s.cur = i
	now := s.now().UnixMilli()
	l := s.timers[i].length().Milliseconds()
	s.status = Status{Model: "playback_status", TimerID: s.timers[i].ID, Start: now, Finish: now + l, Pause: now}
}

// Requests reports how many times each HTTP endpoint was called.
func (s *Server) Requests(endpoint string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[endpoint]
}

// Clients is how many sockets are connected.
func (s *Server) Clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// ------------------------------------------------------------------ control

func (s *Server) Start() {
	s.mu.Lock()
	now := s.now().UnixMilli()
	if !s.status.Running {
		if s.status.Pause != 0 {
			s.status.Finish += now - s.status.Pause
			s.status.Start += now - s.status.Pause
		}
		s.status.Running, s.status.Pause = true, 0
	}
	s.mu.Unlock()
	s.emitStatus()
}

func (s *Server) Stop() {
	s.mu.Lock()
	if s.status.Running {
		s.status.Running, s.status.Pause = false, s.now().UnixMilli()
	}
	s.mu.Unlock()
	s.emitStatus()
}

// SetRemaining moves the selected timer so remaining is left (running or not).
func (s *Server) SetRemaining(remaining time.Duration) {
	s.mu.Lock()
	ref := s.now().UnixMilli()
	if !s.status.Running {
		ref = s.status.Pause
	}
	l := s.timers[s.cur].length().Milliseconds()
	s.status.Finish = ref + remaining.Milliseconds()
	s.status.Start = s.status.Finish - l
	s.mu.Unlock()
	s.emitStatus()
}

func (s *Server) Next() {
	s.mu.Lock()
	if s.cur+1 < len(s.timers) {
		s.selectLocked(s.cur + 1)
	}
	s.mu.Unlock()
	s.emitAll()
}

func (s *Server) Flash(count int) { s.broadcast("flash", map[string]int{"count": count}) }

func (s *Server) ShowMessage(i int, focus bool) {
	s.mu.Lock()
	for j := range s.messages {
		s.messages[j].Showing = j == i
		s.messages[j].Focus = j == i && focus
	}
	m := s.messages[i]
	s.mu.Unlock()
	s.broadcast("message", m)
}

func (s *Server) HideMessage() {
	s.mu.Lock()
	var m Message
	for j := range s.messages {
		if s.messages[j].Showing {
			m = s.messages[j]
		}
		s.messages[j].Showing, s.messages[j].Focus = false, false
	}
	m.Showing = false
	s.mu.Unlock()
	s.broadcast("message", m)
}

func (s *Server) SetRoom(fn func(*Room)) {
	s.mu.Lock()
	fn(&s.room)
	r := s.room
	s.mu.Unlock()
	s.broadcast("room", r)
}

// RenameTimer edits a timer, as the Controller would, and says so.
func (s *Server) RenameTimer(i int, name string) {
	s.mu.Lock()
	s.timers[i].Name = name
	t := s.timers[i]
	isCur := i == s.cur
	s.mu.Unlock()
	if isCur {
		s.broadcast("current_timer", t)
	}
	s.broadcast("timers_changed", []map[string]string{{"timer_id": t.ID, "change": "updated"}})
}

// Kick drops every socket.
func (s *Server) Kick() {
	s.mu.Lock()
	for c := range s.clients {
		select {
		case c <- "1":
		default:
		}
	}
	s.mu.Unlock()
}

// --------------------------------------------------------------------- HTTP

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/socket.io/", s.handleSocket)
	mux.HandleFunc("/v1/", s.handleAPI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		down := s.down
		s.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// SetDown makes every request fail with 503 and drops the sockets: Stagetimer
// unreachable, or the venue's internet gone.
func (s *Server) SetDown(b bool) {
	s.mu.Lock()
	s.down = b
	s.mu.Unlock()
	if b {
		s.Kick()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("RateLimit-Remaining", "99")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// auth answers as api.stagetimer.io did on 2026-11-10: a wrong key in the
// header is a 400 about the Bearer token, a wrong one in the query string a
// 400 about api_key.
func (s *Server) auth(r *http.Request) (int, string) {
	key, msg := r.URL.Query().Get("api_key"), "api_key: Not a valid API key"
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		key, msg = strings.TrimPrefix(h, "Bearer "), "authorization: Not a valid Bearer token"
	}
	switch {
	case key == "":
		return 401, "No API key found in `api_key` query parameter OR in HTTP `Authorization` header"
	case key != s.Key:
		return 400, msg
	case r.URL.Query().Get("room_id") != s.RoomID:
		return 404, "Room not found"
	}
	return 0, ""
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	ep := strings.TrimPrefix(r.URL.Path, "/v1/")
	s.mu.Lock()
	s.requests[ep]++
	s.mu.Unlock()
	if code, msg := s.auth(r); code != 0 {
		writeJSON(w, code, map[string]any{"ok": false, "message": msg})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var data any
	switch ep {
	case "test_auth":
		data = nil
	case "get_status":
		st := s.status
		st.ServerTime = s.now().UnixMilli()
		data = st
	case "get_room":
		data = s.room
	case "get_all_timers":
		data = s.timers
	case "get_all_messages":
		data = map[string]any{"messages": s.messages}
	default:
		writeJSON(w, 404, map[string]any{"ok": false, "message": "not in the mock"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": ep, "data": data})
}

// ---------------------------------------------------------------- Socket.IO

func (s *Server) handleSocket(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	blocked := s.blocked
	s.mu.Unlock()
	if blocked || r.URL.Query().Get("transport") != "websocket" || r.URL.Query().Get("EIO") != "4" {
		http.Error(w, "blocked", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()
	open := fmt.Sprintf(`0{"sid":"mock","upgrades":[],"pingInterval":%d,"pingTimeout":20000,"maxPayload":1000000}`, s.PingInterval.Milliseconds())
	if ws.Write(ctx, websocket.MessageText, []byte(open)) != nil {
		return
	}
	_, b, err := ws.Read(ctx)
	if err != nil || !strings.HasPrefix(string(b), "40") {
		return
	}
	var a struct {
		RoomID string `json:"room_id"`
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal(b[2:], &a)
	// The bad-room wording is api.stagetimer.io's (2026-11-10); what it says
	// to a real room with a wrong key has not been seen.
	if a.RoomID != s.RoomID || a.APIKey != s.Key {
		msg := `44{"message":"Access Denied: Room not found"}`
		if a.RoomID == s.RoomID {
			msg = `44{"message":"Access Denied"}`
		}
		_ = ws.Write(ctx, websocket.MessageText, []byte(msg))
		ws.Close(websocket.StatusNormalClosure, "")
		return
	}
	_ = ws.Write(ctx, websocket.MessageText, []byte(`40{"sid":"mock-ns"}`))

	out := make(chan string, 64)
	s.mu.Lock()
	s.clients[out] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, out)
		s.mu.Unlock()
	}()
	for _, p := range s.statePackets() {
		if ws.Write(ctx, websocket.MessageText, []byte(p)) != nil {
			return
		}
	}
	// A client that does not answer a ping by the next one is dropped, as
	// Engine.IO drops it after pingTimeout.
	var pongMu sync.Mutex
	awaiting := false
	gone := make(chan struct{})
	go func() { // the client's pongs, and noticing it has gone
		defer close(gone)
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if string(b) == "3" {
				pongMu.Lock()
				awaiting = false
				pongMu.Unlock()
				s.mu.Lock()
				s.pongs++
				s.mu.Unlock()
			}
		}
	}()
	ping := time.NewTicker(s.PingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case <-ping.C:
			pongMu.Lock()
			missed := awaiting
			awaiting = true
			pongMu.Unlock()
			if missed {
				ws.Close(websocket.StatusPolicyViolation, "ping timeout")
				return
			}
			if ws.Write(ctx, websocket.MessageText, []byte("2")) != nil {
				return
			}
		case p := <-out:
			if p == "1" {
				_ = ws.Write(ctx, websocket.MessageText, []byte("1"))
				ws.Close(websocket.StatusNormalClosure, "")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := ws.Write(wctx, websocket.MessageText, []byte(p))
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func event(name string, data any) string {
	b, _ := json.Marshal([]any{name, data})
	return "42" + string(b)
}

func (s *Server) statePackets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.ServerTime = s.now().UnixMilli()
	var next any
	if s.cur+1 < len(s.timers) {
		next = s.timers[s.cur+1]
	}
	var msg any
	for _, m := range s.messages {
		if m.Showing {
			msg = m
		}
	}
	return []string{
		event("room", s.room),
		event("playback_status", st),
		event("current_timer", s.timers[s.cur]),
		event("next_timer", next),
		event("message", msg),
	}
}

func (s *Server) broadcast(name string, data any) {
	p := event(name, data)
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		select {
		case c <- p:
		default:
		}
	}
}

func (s *Server) emitStatus() {
	s.mu.Lock()
	st := s.status
	st.ServerTime = s.now().UnixMilli()
	s.mu.Unlock()
	s.broadcast("playback_status", st)
}

func (s *Server) emitAll() {
	pk := s.statePackets()
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		for _, p := range pk[1:] {
			select {
			case c <- p:
			default:
			}
		}
	}
}
