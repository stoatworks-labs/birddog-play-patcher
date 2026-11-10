package main

// Just enough Socket.IO to read Stagetimer's real-time feed.
//
// The server is Socket.IO v4 (Engine.IO protocol 4) at /v1/socket.io — the
// polling handshake on 2026-11-10 answered
// 0{"sid":…,"upgrades":["websocket"],"pingInterval":25000,"pingTimeout":20000,"maxPayload":1000000}.
// We skip the polling transport and open the WebSocket directly, which Engine.IO
// allows and which avoids the load balancer's sticky cookie entirely.
//
// The parts of the protocol a receive-only client meets:
//
//	0{json}       engine OPEN: ping interval and timeout
//	2 / 3         engine PING from the server / our PONG
//	1             engine CLOSE
//	40{json}      socket CONNECT — ours carries the auth object, the server's the sid
//	44{json}      socket CONNECT_ERROR — {"message":…}: a refused key lands here
//	41            socket DISCONNECT
//	42[name,data] socket EVENT (optionally "/ns," and an ack id before the array)
//
// Everything else (binary events, acks, the 6 NOOP) is ignored. Stagetimer
// documents the socket as receive-only, so we never emit.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ConnectError is the server refusing the socket's CONNECT (44).
type ConnectError struct{ Message string }

func (e *ConnectError) Error() string {
	if e.Message == "" {
		return "Stagetimer refused the connection"
	}
	return "Stagetimer refused the connection: " + e.Message
}

type sioOpen struct {
	SID          string `json:"sid"`
	PingInterval int    `json:"pingInterval"`
	PingTimeout  int    `json:"pingTimeout"`
	MaxPayload   int    `json:"maxPayload"`
}

// sioConn is one connected socket.
type sioConn struct {
	ws   *websocket.Conn
	open sioOpen
}

// socketURL turns an API base (https://api.stagetimer.io) into the feed's
// WebSocket address.
func socketURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("API address %q is not http(s)", base)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/socket.io/"
	u.RawQuery = "EIO=4&transport=websocket"
	return u.String(), nil
}

// dialSocketIO opens the WebSocket, reads the engine OPEN, sends CONNECT with
// auth and waits for the answer.
func dialSocketIO(ctx context.Context, base string, auth any, hc *http.Client) (*sioConn, error) {
	wsURL, err := socketURL(base)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("User-Agent", "bdstage/"+version)
	ws, _, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPClient: hc, HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("websocket: %w", err)
	}
	// maxPayload is a megabyte; coder/websocket's default read limit is 32 KB,
	// which one rundown with long notes can exceed.
	ws.SetReadLimit(2 << 20)
	c := &sioConn{ws: ws}

	typ, b, err := ws.Read(dctx)
	if err != nil {
		ws.CloseNow()
		return nil, fmt.Errorf("websocket: no handshake: %w", err)
	}
	if typ != websocket.MessageText || len(b) == 0 || b[0] != '0' {
		ws.CloseNow()
		return nil, fmt.Errorf("websocket: unexpected handshake %q", clip(string(b), 80))
	}
	if err := json.Unmarshal(b[1:], &c.open); err != nil {
		ws.CloseNow()
		return nil, fmt.Errorf("websocket: bad handshake: %v", err)
	}
	if c.open.PingInterval <= 0 {
		c.open.PingInterval = 25000
	}
	if c.open.PingTimeout <= 0 {
		c.open.PingTimeout = 20000
	}

	payload, err := json.Marshal(auth)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	if err := ws.Write(dctx, websocket.MessageText, append([]byte("40"), payload...)); err != nil {
		ws.CloseNow()
		return nil, fmt.Errorf("websocket: %w", err)
	}
	for {
		_, b, err := ws.Read(dctx)
		if err != nil {
			ws.CloseNow()
			return nil, fmt.Errorf("websocket: no answer to connect: %w", err)
		}
		pk := string(b)
		switch {
		case pk == "2":
			_ = ws.Write(dctx, websocket.MessageText, []byte("3"))
		case strings.HasPrefix(pk, "40"):
			return c, nil
		case strings.HasPrefix(pk, "44"):
			ws.Close(websocket.StatusNormalClosure, "")
			var m struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal([]byte(strings.TrimPrefix(pk, "44")), &m)
			if m.Message == "" {
				m.Message = strings.TrimPrefix(pk, "44")
			}
			return nil, &ConnectError{Message: m.Message}
		case pk == "1" || strings.HasPrefix(pk, "41"):
			ws.CloseNow()
			return nil, errors.New("Stagetimer closed the connection during connect")
		}
		// Anything else before CONNECT is answered (an early event) is dropped;
		// the server resends the state on connect.
	}
}

var errSocketClosed = errors.New("Stagetimer closed the connection")

// Run reads until the connection ends, answering pings, and hands each event
// to fn. It returns why it stopped; never nil.
func (c *sioConn) Run(ctx context.Context, fn func(name string, data json.RawMessage)) error {
	defer c.ws.CloseNow()
	// A server that has not pinged within interval + timeout is gone.
	deadline := time.Duration(c.open.PingInterval+c.open.PingTimeout) * time.Millisecond
	for {
		rctx, cancel := context.WithTimeout(ctx, deadline)
		typ, b, err := c.ws.Read(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("no ping from Stagetimer for %v", deadline)
			}
			return fmt.Errorf("websocket: %w", err)
		}
		if typ != websocket.MessageText || len(b) == 0 {
			continue
		}
		switch b[0] {
		case '2': // ping
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.ws.Write(wctx, websocket.MessageText, []byte("3"))
			cancel()
			if err != nil {
				return fmt.Errorf("websocket: %w", err)
			}
		case '1':
			return errSocketClosed
		case '4':
			if len(b) < 2 {
				continue
			}
			switch b[1] {
			case '1':
				return errSocketClosed
			case '4':
				var m struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(b[2:], &m)
				return &ConnectError{Message: m.Message}
			case '2':
				name, data, ok := parseEvent(b[2:])
				if ok {
					fn(name, data)
				}
			}
		}
	}
}

func (c *sioConn) Close() { c.ws.Close(websocket.StatusNormalClosure, "") }

// parseEvent reads an EVENT body: [/namespace,][ackid]["name", data...].
func parseEvent(b []byte) (string, json.RawMessage, bool) {
	s := b
	if len(s) > 0 && s[0] == '/' {
		i := 0
		for i < len(s) && s[i] != ',' {
			i++
		}
		if i == len(s) {
			return "", nil, false
		}
		s = s[i+1:]
	}
	for len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = s[1:]
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(s, &arr); err != nil || len(arr) == 0 {
		return "", nil, false
	}
	var name string
	if err := json.Unmarshal(arr[0], &name); err != nil || name == "" {
		return "", nil, false
	}
	if len(arr) < 2 {
		return name, json.RawMessage("null"), true
	}
	return name, arr[1], true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
