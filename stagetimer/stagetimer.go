package main

// Stagetimer's Web API v1, as https://stagetimer.io/docs/api-v1/ documents it
// and as api.stagetimer.io answered on 2026-11-10.
//
// Two halves: plain HTTP GETs (every endpoint is a GET, mutations included —
// we only ever read), and a receive-only Socket.IO feed (socketio.go). The
// feed is what keeps the screen live; HTTP fills in what the feed does not
// carry (the whole rundown, for the agenda) and is the fallback when a venue
// network will not pass a WebSocket.
//
// The key goes in an Authorization header, never the query string, so it does
// not end up in a proxy's access log. 100 requests a minute per room key is
// the documented budget; Client counts what the server says is left.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultAPIBase = "https://api.stagetimer.io"

// PlaybackStatus is the transport state of the selected timer. Times are Unix
// milliseconds on the server's clock. The API carries no running countdown:
// it is finish minus now while running, finish minus pause while stopped.
type PlaybackStatus struct {
	TimerID    string `json:"timer_id"`
	Running    bool   `json:"running"`
	Start      int64  `json:"start"`
	Finish     int64  `json:"finish"`
	Pause      int64  `json:"pause"`
	ServerTime int64  `json:"server_time"`
}

type Room struct {
	ID       string `json:"_id"`
	Name     string `json:"name"`
	Blackout bool   `json:"blackout"`
	// FocusMessage is deprecated upstream and "not updated live"; a message
	// shown with focus=true is the current way, and is read from the message.
	FocusMessage bool   `json:"focus_message"`
	OnAir        bool   `json:"on_air"`
	Logo         string `json:"logo"`
	Timezone     string `json:"timezone"`
}

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type Timer struct {
	ID           string  `json:"_id"`
	Name         string  `json:"name"`
	Speaker      string  `json:"speaker"`
	Notes        string  `json:"notes"`
	Labels       []Label `json:"labels"`
	Appearance   string  `json:"appearance"` // COUNTDOWN COUNTUP TOD COUNTDOWN_TOD COUNTUP_TOD HIDDEN
	Type         string  `json:"type"`       // DURATION FINISH_TIME
	Duration     string  `json:"duration"`   // "0:01:00"
	Hours        int     `json:"hours"`
	Minutes      int     `json:"minutes"`
	Seconds      int     `json:"seconds"`
	WrapUpYellow int     `json:"wrap_up_yellow"` // seconds before the end
	WrapUpRed    int     `json:"wrap_up_red"`
	Trigger      string  `json:"trigger"`    // MANUAL LINKED SCHEDULED
	StartTime    *string `json:"start_time"` // only the time of day is meaningful
	FinishTime   *string `json:"finish_time"`
}

// Length is the timer's set duration.
func (t Timer) Length() time.Duration {
	if d, ok := parseHMS(t.Duration); ok {
		return d
	}
	return time.Duration(t.Hours)*time.Hour + time.Duration(t.Minutes)*time.Minute + time.Duration(t.Seconds)*time.Second
}

// parseHMS reads "H:MM:SS" (and "MM:SS").
func parseHMS(s string) (time.Duration, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total time.Duration
	unit := []time.Duration{time.Hour, time.Minute, time.Second}[3-len(parts):]
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		total += time.Duration(n) * unit[i]
	}
	return total, true
}

// ClockTime parses one of the timer's start_time/finish_time values.
func ClockTime(s *string) (time.Time, bool) {
	if s == nil || *s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, *s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type Message struct {
	ID        string `json:"_id"`
	Showing   bool   `json:"showing"`
	Text      string `json:"text"`
	Color     string `json:"color"` // white green red
	Bold      bool   `json:"bold"`
	Uppercase bool   `json:"uppercase"`
	// Focus is not in the documented payload. /show_message takes focus=true
	// and shows the message fullscreen, so if the server echoes it, honour it.
	Focus bool `json:"focus"`
}

// Credentials name a room and the key that may read it.
type Credentials struct {
	RoomID string
	APIKey string
	Base   string // DefaultAPIBase unless testing
}

func (c Credentials) base() string {
	if c.Base == "" {
		return DefaultAPIBase
	}
	return strings.TrimRight(c.Base, "/")
}

// APIError is Stagetimer's {"ok":false,"message":…} answer.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Stagetimer answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("Stagetimer: %s (HTTP %d)", e.Message, e.Status)
}

// Refused reports a key or room the server will not accept, which retrying
// will not fix. Every call is a GET with nothing in it but the room and the
// key, so a 400 is about one of those: on 2026-11-10 a wrong key in the
// header came back as 400 "authorization: Not a valid Bearer token".
func (e *APIError) Refused() bool {
	switch e.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

func isRefused(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Refused()
	}
	var ce *ConnectError
	return errors.As(err, &ce)
}

// Client is the HTTP half.
type Client struct {
	Creds Credentials
	HTTP  *http.Client
	// OnDate receives the server's Date header and when the response arrived,
	// for the clock offset.
	OnDate func(server, local time.Time)

	mu        sync.Mutex
	remaining int // RateLimit-Remaining, -1 if never seen
}

func NewClient(c Credentials, hc *http.Client) *Client {
	return &Client{Creds: c, HTTP: hc, remaining: -1}
}

// RateRemaining is what the last response said was left of the minute's budget.
func (c *Client) RateRemaining() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remaining
}

func (c *Client) get(ctx context.Context, endpoint string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("room_id", c.Creds.RoomID)
	u := c.Creds.base() + "/v1/" + endpoint + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Creds.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bdstage/"+version)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	arrived := time.Now()
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil && c.OnDate != nil {
		c.OnDate(d, arrived)
	}
	if v := resp.Header.Get("RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.mu.Lock()
			c.remaining = n
			c.mu.Unlock()
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var env struct {
		OK      bool            `json:"ok"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return &APIError{Status: resp.StatusCode, Message: "an answer that is not JSON"}
	}
	if !env.OK || resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Message: env.Message}
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// TestAuth checks the room ID and key together.
func (c *Client) TestAuth(ctx context.Context) error { return c.get(ctx, "test_auth", nil, nil) }

func (c *Client) Status(ctx context.Context) (PlaybackStatus, error) {
	var s PlaybackStatus
	return s, c.get(ctx, "get_status", nil, &s)
}

func (c *Client) Room(ctx context.Context) (Room, error) {
	var r Room
	return r, c.get(ctx, "get_room", nil, &r)
}

func (c *Client) Timers(ctx context.Context) ([]Timer, error) {
	var ts []Timer
	return ts, c.get(ctx, "get_all_timers", nil, &ts)
}

func (c *Client) Messages(ctx context.Context) ([]Message, error) {
	var out struct {
		Messages []Message `json:"messages"`
	}
	return out.Messages, c.get(ctx, "get_all_messages", nil, &out)
}
