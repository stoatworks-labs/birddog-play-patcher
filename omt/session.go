package main

// The session: one source on HDMI, from taking the display to giving it back.
//
//	idle      PPApp has HDMI; nothing of ours is running
//	waiting   the display is ours and black (or holding the last frame), and
//	          the source is not delivering — not yet connected, or gone
//	live      frames are reaching the screen
//	error     something stopped the session; PPApp has HDMI back
//
// Three goroutines per session: the receiver (receiver.go), the decode loop
// here, which alone touches the video sink, and the audio loop, which alone
// touches the audio sink. Stopping cancels them and waits, so a sink is never
// used after Stop has returned and the display is only released once nothing
// of ours is drawing on it.
//
// Switching source while live keeps the display: the receiver and loops are
// replaced, PPApp is not restarted in between. Every restart of PPApp is a
// chance for it to abort in its own NDI teardown (player/AGENTS.md has the
// numbers), so the session cycles it as little as it can.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type State string

const (
	StateIdle    State = "idle"
	StateWaiting State = "waiting"
	StateLive    State = "live"
	StateError   State = "error"
)

// Settings are the per-session choices from config.json.
type Settings struct {
	Quality     Quality
	Audio       bool
	Tally       bool
	Threads     int
	HoldOnLoss  bool // keep the last frame when the source stops; else go black
	MatchOutput bool
}

// SessionDeps are the session's outside world; tests replace each.
type SessionDeps struct {
	Display  DisplayOwner
	NewVideo func(Settings) VideoSink
	NewAudio func(Settings) AudioSink
	VMX      func() (*VMX, error)
	Resolve  func(context.Context, Source) ([]string, error)
	Mode     func() OutputMode
	// AudioOK is false when the box cannot play sound (no rawaudioparse).
	AudioOK bool
	Log     *Logger
}

// Session runs at most one source at a time.
type Session struct {
	deps SessionDeps
	op   sync.Mutex // serialises Start and Stop

	mu       sync.Mutex
	state    State
	src      Source
	settings Settings
	rx       *Receiver
	cancel   context.CancelFunc
	done     chan struct{}
	since    time.Time
	lastErr  string
	errAt    time.Time
	out      struct{ w, h int }
	audioErr string
	audioHz  int

	decoded, decodeErrs, shown, sinkDrops, sinkRestarts atomic.Uint64
	decodeMicros                                        atomic.Int64 // smoothed
	fps                                                 atomic.Value // float64
	lastFrame                                           atomic.Int64 // unix nanos
	threads                                             atomic.Int64
}

func NewSession(d SessionDeps) *Session {
	s := &Session{deps: d, state: StateIdle}
	s.fps.Store(0.0)
	return s
}

// Start shows src on HDMI, taking the display if it is not already ours.
func (s *Session) Start(src Source, st Settings) error {
	if src.IsZero() {
		return errors.New("no source chosen")
	}
	s.op.Lock()
	defer s.op.Unlock()

	s.stopLoops()
	if err := s.deps.Display.Acquire(); err != nil {
		s.fail(err)
		return err
	}
	if !s.deps.AudioOK {
		st.Audio = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	rx := NewReceiver(ReceiverConfig{
		Source:  src,
		Audio:   st.Audio,
		Quality: st.Quality,
		Tally:   st.Tally,
		Resolve: s.deps.Resolve,
		Log:     s.deps.Log,
	})
	done := make(chan struct{})

	s.mu.Lock()
	s.state, s.src, s.settings = StateWaiting, src, st
	s.rx, s.cancel, s.done = rx, cancel, done
	s.since = time.Now()
	s.lastErr, s.audioErr, s.audioHz = "", "", 0
	s.mu.Unlock()
	s.decoded.Store(0)
	s.decodeErrs.Store(0)
	s.shown.Store(0)
	s.sinkDrops.Store(0)
	s.sinkRestarts.Store(0)
	s.lastFrame.Store(0)
	s.fps.Store(0.0)

	s.deps.Log.Info("session: showing %s (quality %s, audio %v, tally %v)", src.Label(), st.Quality, st.Audio, st.Tally)
	go s.run(ctx, cancel, rx, st, done)
	return nil
}

// Stop ends the session and gives HDMI back to PPApp.
func (s *Session) Stop() {
	s.op.Lock()
	defer s.op.Unlock()
	s.stopLoops()
	held := s.deps.Display.Held()
	s.deps.Display.Release()
	s.mu.Lock()
	was := s.state
	s.state = StateIdle
	s.mu.Unlock()
	if was != StateIdle || held {
		s.deps.Log.Info("session: stopped")
	}
}

// stopLoops cancels the running loops and waits for them; the display stays.
func (s *Session) stopLoops() {
	s.mu.Lock()
	rx, cancel, done := s.rx, s.cancel, s.done
	s.rx, s.cancel, s.done = nil, nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	// Tell the sender we are off air before the socket goes, so its tally
	// does not wait for a TCP timeout.
	rx.SetTally(false)
	cancel()
	<-done
}

func (s *Session) fail(err error) {
	s.deps.Log.Error("session: %v", err)
	s.mu.Lock()
	s.state = StateError
	s.lastErr = err.Error()
	s.errAt = time.Now()
	s.mu.Unlock()
}

func (s *Session) setErr(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.errAt = time.Now()
	s.mu.Unlock()
}

// run supervises one session's goroutines. If the decode loop gives up (the
// display cannot be driven), the session ends and PPApp gets HDMI back.
func (s *Session) run(ctx context.Context, cancel context.CancelFunc, rx *Receiver, st Settings, done chan struct{}) {
	defer close(done)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); rx.Run(ctx) }()
	if st.Audio {
		wg.Add(1)
		go func() { defer wg.Done(); s.audioLoop(ctx, rx, st) }()
	}
	err := s.decodeLoop(ctx, rx, st)
	cancel()
	wg.Wait()
	if err == nil || ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return
	}
	// A fatal output error. Release here, unless Start/Stop already moved on
	// (they hold op and are waiting on done, so they cannot be mid-way).
	s.fail(err)
	go func() {
		s.op.Lock()
		defer s.op.Unlock()
		s.mu.Lock()
		current := s.done == done
		if current {
			s.rx, s.cancel, s.done = nil, nil, nil
		}
		s.mu.Unlock()
		if current {
			s.deps.Display.Release()
		}
	}()
}

// errOutput is returned when HDMI output keeps failing.
var errOutput = errors.New("the HDMI output pipeline keeps failing")

func (s *Session) decodeLoop(ctx context.Context, rx *Receiver, st Settings) error {
	video := s.deps.NewVideo(st)
	defer video.Stop()
	mb := rx.Video()

	// restart (re)starts the output at a size. A restart for a failure (why
	// set) counts towards giving up; a new size from the source does not, or a
	// sender that changes format a few times would end the session.
	var restarts []time.Time
	restart := func(w, h int, why error) error {
		if why != nil {
			now := time.Now()
			keep := restarts[:0]
			for _, t := range restarts {
				if now.Sub(t) < 30*time.Second {
					keep = append(keep, t)
				}
			}
			restarts = append(keep, now)
			s.sinkRestarts.Add(1)
			s.deps.Log.Warn("video: %v; restarting the pipeline", why)
			if len(restarts) > 5 {
				return fmt.Errorf("%w: %v", errOutput, why)
			}
		}
		if err := video.Start(w, h); err != nil {
			return fmt.Errorf("%w: %v", errOutput, err)
		}
		s.mu.Lock()
		s.out.w, s.out.h = w, h
		s.mu.Unlock()
		return nil
	}

	// Black at the output's own size straight away, so taking the display
	// looks like a cut rather than a freeze of whatever PPApp last drew.
	// When the mode cannot be read, the first frame starts the output instead.
	mode := s.deps.Mode()
	if mode.Width > 0 && mode.Height > 0 {
		if err := restart(mode.Width, mode.Height, nil); err != nil {
			return err
		}
		showBlack(video)
	}

	var vmx *VMX
	var dec *Decoder
	defer func() { dec.Close() }()
	var blacked bool
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var fpsAt time.Time
	var fpsShown uint64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			now := time.Now()
			if !fpsAt.IsZero() {
				n := s.shown.Load()
				s.fps.Store(float64(n-fpsShown) / now.Sub(fpsAt).Seconds())
				fpsShown = n
			} else {
				fpsShown = s.shown.Load()
			}
			fpsAt = now
			if err := video.Err(); err != nil {
				w, h := video.Size()
				if w == 0 {
					w, h = mode.Width, mode.Height
				}
				if w == 0 {
					continue
				}
				if err := restart(w, h, err); err != nil {
					return err
				}
				showBlack(video)
			}
			last := s.lastFrame.Load()
			if last != 0 && now.Sub(time.Unix(0, last)) > 2*time.Second {
				s.mu.Lock()
				if s.state == StateLive {
					s.state = StateWaiting
					s.deps.Log.Warn("session: no frames from %s for 2s", s.src.Label())
				}
				s.mu.Unlock()
				if !st.HoldOnLoss && !blacked {
					showBlack(video)
					blacked = true
				}
			}
			continue
		case <-mb.notify:
		}

		f := mb.take()
		if f == nil {
			continue
		}
		err := func() error {
			defer mb.release(f)
			h := f.hdr
			if h.Codec != codecVMX1 {
				return fmt.Errorf("the sender is sending %s video; only VMX1 is supported", fourCCString(h.Codec))
			}
			if h.Flags&videoInterlaced != 0 {
				return errors.New("interlaced sources are not supported yet; set the sender to progressive")
			}
			if dec == nil || dec.W != h.Width || dec.H != h.Height || dec.ColorSpace != h.ColorSpace {
				if vmx == nil {
					v, err := s.deps.VMX()
					if err != nil {
						return err
					}
					vmx = v
				}
				dec.Close()
				dec = nil
				d, err := vmx.NewDecoder(h.Width, h.Height, h.ColorSpace, st.Threads)
				if err != nil {
					return err
				}
				dec = d
				s.threads.Store(int64(d.Threads()))
				s.deps.Log.Info("video: decoding %dx%d on %d threads", h.Width, h.Height, d.Threads())
			}
			if w, hh := video.Size(); w != h.Width || hh != h.Height {
				if err := restart(h.Width, h.Height, nil); err != nil {
					return err
				}
			}
			buf := video.Buffer()
			if buf == nil {
				s.sinkDrops.Add(1)
				return nil
			}
			t0 := time.Now()
			if err := dec.DecodeNV12(f.data, buf); err != nil {
				video.Recycle(buf)
				s.decodeErrs.Add(1)
				return err
			}
			us := time.Since(t0).Microseconds()
			prev := s.decodeMicros.Load()
			if prev == 0 {
				s.decodeMicros.Store(us)
			} else {
				s.decodeMicros.Store((prev*15 + us) / 16)
			}
			s.decoded.Add(1)
			if err := video.Submit(buf); err != nil {
				if err := restart(h.Width, h.Height, err); err != nil {
					return err
				}
				return nil
			}
			s.shown.Add(1)
			s.lastFrame.Store(time.Now().UnixNano())
			blacked = false
			s.mu.Lock()
			if s.state == StateWaiting {
				s.state = StateLive
				s.lastErr = ""
				s.deps.Log.Info("session: live (%dx%d)", h.Width, h.Height)
			}
			s.mu.Unlock()
			return nil
		}()
		if err != nil {
			if errors.Is(err, errOutput) {
				return err
			}
			s.setErr(err)
		}
	}
}

// showBlack puts a black frame on the output at its current size.
func showBlack(v VideoSink) {
	w, h := v.Size()
	if w == 0 {
		return
	}
	buf := v.Buffer()
	if buf == nil {
		return
	}
	_, uvOff, _ := NV12Layout(w, h)
	for i := 0; i < uvOff; i++ {
		buf[i] = 16
	}
	for i := uvOff; i < len(buf); i++ {
		buf[i] = 128
	}
	_ = v.Submit(buf)
}

func (s *Session) audioLoop(ctx context.Context, rx *Receiver, st Settings) {
	audio := s.deps.NewAudio(st)
	defer audio.Stop()
	var retryAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case af := <-rx.Audio():
			if audio.Rate() != af.rate || audio.Err() != nil {
				if time.Now().Before(retryAt) {
					continue
				}
				if err := audio.Start(af.rate); err != nil {
					s.mu.Lock()
					s.audioErr = err.Error()
					s.mu.Unlock()
					s.deps.Log.Warn("audio: %v", err)
					retryAt = time.Now().Add(5 * time.Second)
					continue
				}
				s.mu.Lock()
				s.audioErr, s.audioHz = "", af.rate
				s.mu.Unlock()
			}
			if err := audio.Write(af.pcm); err != nil {
				s.mu.Lock()
				s.audioErr = err.Error()
				s.mu.Unlock()
			}
		}
	}
}

// SessionStatus is the session's part of /api/status.
type SessionStatus struct {
	State        State          `json:"state"`
	Source       Source         `json:"source"`
	Since        time.Time      `json:"since,omitzero"`
	Error        string         `json:"error,omitempty"`
	ErrorTime    time.Time      `json:"error_time,omitzero"`
	DisplayHeld  bool           `json:"display_held"`
	Output       *OutputMode    `json:"output,omitempty"`
	FPS          float64        `json:"fps"`
	Decoded      uint64         `json:"decoded"`
	DecodeErrors uint64         `json:"decode_errors"`
	DecodeMS     float64        `json:"decode_ms"`
	Threads      int            `json:"threads,omitempty"`
	Shown        uint64         `json:"shown"`
	SinkDropped  uint64         `json:"sink_dropped"`
	SinkRestarts uint64         `json:"sink_restarts"`
	Audio        bool           `json:"audio"`
	AudioHz      int            `json:"audio_hz,omitempty"`
	AudioError   string         `json:"audio_error,omitempty"`
	Receiver     *ReceiverStats `json:"receiver,omitempty"`
}

func (s *Session) Status() SessionStatus {
	s.mu.Lock()
	st := SessionStatus{
		State:       s.state,
		Source:      s.src,
		Since:       s.since,
		Error:       s.lastErr,
		ErrorTime:   s.errAt,
		DisplayHeld: s.deps.Display.Held(),
		Audio:       s.settings.Audio,
		AudioHz:     s.audioHz,
		AudioError:  s.audioErr,
	}
	if s.out.w > 0 && s.rx != nil {
		st.Output = &OutputMode{Width: s.out.w, Height: s.out.h}
	}
	rx := s.rx
	s.mu.Unlock()
	if st.State == StateIdle {
		st.Since = time.Time{}
	}
	if rx != nil {
		rs := rx.Stats()
		st.Receiver = &rs
		st.FPS = round1(s.fps.Load().(float64))
		st.Decoded = s.decoded.Load()
		st.DecodeErrors = s.decodeErrs.Load()
		st.DecodeMS = round1(float64(s.decodeMicros.Load()) / 1000)
		st.Threads = int(s.threads.Load())
		st.Shown = s.shown.Load()
		st.SinkDropped = s.sinkDrops.Load()
		st.SinkRestarts = s.sinkRestarts.Load()
	}
	return st
}

// Current reports the source being shown, if any.
func (s *Session) Current() (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src, s.rx != nil
}
