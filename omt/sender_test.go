package main

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSender is an OMT sender on loopback: it records every control message a
// receiver sends, and streams video or audio down a connection once that
// connection subscribes to it, the way libomtnet's OMTSend does.
type fakeSender struct {
	t      *testing.T
	ln     net.Listener
	vh     VideoHeader
	video  [][]byte // payloads, cycled
	info   string   // sent on connect when set
	paused atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
	msgs  [][]string
}

func newFakeSender(t *testing.T, vh VideoHeader, video [][]byte, info string) *fakeSender {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSender{t: t, ln: ln, vh: vh, video: video, info: info}
	go s.serve()
	t.Cleanup(s.close)
	return s
}

func (s *fakeSender) addr() string { return s.ln.Addr().String() }

func (s *fakeSender) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		idx := len(s.conns)
		s.conns = append(s.conns, c)
		s.msgs = append(s.msgs, nil)
		s.mu.Unlock()
		go s.handle(idx, c)
	}
}

func (s *fakeSender) handle(idx int, c net.Conn) {
	var wmu sync.Mutex
	write := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, err := c.Write(b)
		return err
	}
	if s.info != "" {
		_ = write(encodeMetadata(s.info))
	}
	var buf []byte
	for {
		f, b, err := readFrame(c, buf)
		buf = b
		if err != nil {
			return
		}
		if f.Type != frameMetadata {
			continue
		}
		m := metadataText(f.Data)
		s.mu.Lock()
		s.msgs[idx] = append(s.msgs[idx], m)
		s.mu.Unlock()
		switch m {
		case msgSubscribeVideo:
			go s.stream(write, func(i int) []byte {
				return wireVideo(int64(i)*166667, s.vh, s.video[i%len(s.video)], nil)
			})
		case msgSubscribeAudio:
			go s.stream(write, func(i int) []byte {
				return wireAudio(int64(i)*100000, AudioHeader{Codec: codecFPA1, SampleRate: 48000,
					SamplesPerChannel: 480, Channels: 2, ActiveChannels: 3}, sine480(i))
			})
		}
	}
}

// stream sends a frame every 10 ms until the connection goes.
func (s *fakeSender) stream(write func([]byte) error, frame func(i int) []byte) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		<-tick.C
		if s.paused.Load() {
			continue
		}
		if write(frame(i)) != nil {
			return
		}
	}
}

// send puts a control message on connection idx.
func (s *fakeSender) send(idx int, xml string) {
	s.mu.Lock()
	c := s.conns[idx]
	s.mu.Unlock()
	if _, err := c.Write(encodeMetadata(xml)); err != nil {
		s.t.Errorf("send: %v", err)
	}
}

func (s *fakeSender) messages(idx int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx >= len(s.msgs) {
		return nil
	}
	return append([]string(nil), s.msgs[idx]...)
}

func (s *fakeSender) close() {
	s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

// sine480 is 10 ms of a 1 kHz tone, left at half the right's level.
func sine480(frame int) []byte {
	out := make([]byte, 0, 480*4*2)
	for _, amp := range []float64{0.25, 0.5} {
		for i := 0; i < 480; i++ {
			v := amp * math.Sin(2*math.Pi*1000*float64(frame*480+i)/48000)
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(float32(v)))
		}
	}
	return out
}

func asIs(_ context.Context, s Source) ([]string, error) { return []string{s.Address}, nil }

func waitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReceiverSubscribesReceivesAndFollowsRedirect(t *testing.T) {
	vh := VideoHeader{Codec: codecVMX1, Width: 64, Height: 32, FrameRateN: 60, FrameRateD: 1, ColorSpace: 709}
	s1 := newFakeSender(t, vh, [][]byte{[]byte("frame-a"), []byte("frame-b")},
		`<OMTInfo ProductName="Fake" Manufacturer="Stoatworks" Version="1.0" />`)
	rx := NewReceiver(ReceiverConfig{Source: Source{Address: s1.addr()}, Audio: true, Quality: QualityLow,
		Tally: true, Resolve: asIs, Log: NewLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rx.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "video and audio", 3*time.Second, func() bool {
		st := rx.Stats()
		return st.Connected && st.VideoFrames > 2 && st.AudioFrames > 2
	})

	// The video connection carries everything but audio, in libomt's order;
	// the audio connection asks for audio alone.
	want0 := []string{msgSubscribeMetadata, msgSubscribeVideo, `<OMTSettings Quality="Low" />`, msgTallyProgram}
	if got := s1.messages(0); strings.Join(got, "|") != strings.Join(want0, "|") {
		t.Fatalf("video connection got %q, want %q", got, want0)
	}
	if got := s1.messages(1); len(got) != 1 || got[0] != msgSubscribeAudio {
		t.Fatalf("audio connection got %q", got)
	}

	<-rx.Video().notify
	f := rx.Video().take()
	if f == nil || f.hdr != vh || (string(f.data) != "frame-a" && string(f.data) != "frame-b") {
		t.Fatalf("video frame: %+v", f)
	}
	rx.Video().release(f)

	a := <-rx.Audio()
	if a.rate != 48000 || len(a.pcm) != 480*4 {
		t.Fatalf("audio frame: %d Hz, %d bytes", a.rate, len(a.pcm))
	}

	st := rx.Stats()
	if st.Sender != (SenderInfo{Product: "Fake", Manufacturer: "Stoatworks", Version: "1.0"}) {
		t.Fatalf("sender info: %+v", st.Sender)
	}
	if st.Video == nil || st.Video.Width != 64 || st.Video.FrameRate != 60 || st.Audio == nil || st.Audio.Channels != 2 {
		t.Fatalf("stream info: %+v %+v", st.Video, st.Audio)
	}

	rx.SetTally(false)
	waitFor(t, "tally off", 2*time.Second, func() bool {
		m := s1.messages(0)
		return len(m) > 0 && m[len(m)-1] == msgTallyNone
	})

	// A redirect hands us to another sender; the next connection goes there.
	s2 := newFakeSender(t, vh, [][]byte{[]byte("frame-c")}, "")
	s1.send(0, `<OMTRedirect NewAddress="`+s2.addr()+`" />`)
	waitFor(t, "the redirect", 5*time.Second, func() bool {
		st := rx.Stats()
		return st.Connected && st.ConnectedTo == s2.addr() && st.Redirected == s2.addr()
	})
	waitFor(t, "frames from the new sender", 2*time.Second, func() bool {
		<-rx.Video().notify
		f := rx.Video().take()
		defer rx.Video().release(f)
		return f != nil && string(f.data) == "frame-c"
	})
	if m := s2.messages(0); len(m) < 4 || m[3] != msgTallyNone {
		t.Fatalf("the new sender should get the tally as it now stands: %q", m)
	}
}

func TestReceiverRetriesAVanishedSender(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listening: connection refused

	rx := NewReceiver(ReceiverConfig{Source: Source{Address: addr}, Resolve: asIs, Log: NewLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rx.Run(ctx); close(done) }()
	waitFor(t, "a failed attempt", 3*time.Second, func() bool { return rx.Stats().LastError != "" })
	if rx.Stats().Connected {
		t.Fatal("connected to nothing")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
