package main

// Receiving one OMT source.
//
// libomt opens two TCP connections to a sender, one for video (plus the
// metadata and tally traffic) and one for audio, and so do we: a sender only
// sends on a connection what that connection subscribed to, and keeping audio
// off the video socket means a 400 KB frame never holds up 10 ms of sound.
//
// The protocol note's one piece of advice is that the receiver must never stop
// reading, or the sender's queue backs up into latency. So each connection has a
// goroutine that does nothing but read. Video frames go into a one-slot
// mailbox: if the decoder has not taken the last frame by the time the next one
// arrives, the old one is dropped and counted. On this CPU that is the
// difference between falling behind gracefully and falling seconds behind.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// videoFrame is a received, still-compressed video frame. Data aliases buf.
type videoFrame struct {
	hdr  VideoHeader
	ts   int64
	data []byte
	buf  []byte
}

// mailbox hands the newest video frame from the reader to the decoder.
type mailbox struct {
	mu      sync.Mutex
	pending *videoFrame
	free    []*videoFrame
	notify  chan struct{}
	dropped atomic.Uint64
}

func newMailbox() *mailbox { return &mailbox{notify: make(chan struct{}, 1)} }

// get returns a buffer for the reader to fill.
func (m *mailbox) get() *videoFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.free); n > 0 {
		f := m.free[n-1]
		m.free = m.free[:n-1]
		return f
	}
	return &videoFrame{}
}

// put publishes a filled frame, displacing (and counting) any the decoder has
// not taken yet.
func (m *mailbox) put(f *videoFrame) {
	m.mu.Lock()
	if m.pending != nil {
		m.dropped.Add(1)
		m.free = append(m.free, m.pending)
	}
	m.pending = f
	m.mu.Unlock()
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

// take returns the newest frame, or nil.
func (m *mailbox) take() *videoFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.pending
	m.pending = nil
	return f
}

// release returns a frame's buffer once the decoder is done with it.
func (m *mailbox) release(f *videoFrame) {
	if f == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.free) < 3 {
		m.free = append(m.free, f)
	}
}

// audioFrame is one received audio frame, already converted for the HDMI sink.
type audioFrame struct {
	rate int
	pcm  []byte // S16LE, two channels interleaved
}

// ReceiverStats is the receive side of /api/status.
type ReceiverStats struct {
	Connected     bool       `json:"connected"`
	ConnectedTo   string     `json:"connected_to,omitempty"`
	Since         time.Time  `json:"since,omitzero"`
	VideoFrames   uint64     `json:"video_frames"`
	AudioFrames   uint64     `json:"audio_frames"`
	Bytes         uint64     `json:"bytes"`
	Dropped       uint64     `json:"dropped"`
	AudioDropped  uint64     `json:"audio_dropped"`
	Reconnects    uint64     `json:"reconnects"`
	Video         *VideoInfo `json:"video,omitempty"`
	Audio         *AudioInfo `json:"audio,omitempty"`
	Sender        SenderInfo `json:"sender"`
	Redirected    string     `json:"redirected_to,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorTime time.Time  `json:"last_error_time,omitzero"`
}

// VideoInfo describes the stream as the sender last described it.
type VideoInfo struct {
	Codec      string  `json:"codec"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FrameRate  float64 `json:"frame_rate"`
	Interlaced bool    `json:"interlaced"`
	Alpha      bool    `json:"alpha"`
	ColorSpace int     `json:"color_space"`
}

// AudioInfo describes the audio as the sender last described it.
type AudioInfo struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
}

// ReceiverConfig is what a Receiver needs to know.
type ReceiverConfig struct {
	Source  Source
	Audio   bool
	Quality Quality
	Tally   bool
	// Resolve turns a source into dialable host:port addresses.
	Resolve func(ctx context.Context, s Source) ([]string, error)
	Log     *Logger
}

// Receiver keeps one source connected until its context ends.
type Receiver struct {
	cfg   ReceiverConfig
	video *mailbox
	audio chan audioFrame

	videoFrames, audioFrames, bytes, audioDropped, reconnects atomic.Uint64

	mu        sync.Mutex
	connected bool
	connTo    string
	since     time.Time
	vinfo     *VideoInfo
	ainfo     *AudioInfo
	sender    SenderInfo
	redirect  string
	lastErr   string
	lastErrAt time.Time
	videoConn net.Conn
	sendTally bool
}

func NewReceiver(cfg ReceiverConfig) *Receiver {
	if cfg.Quality == "" {
		cfg.Quality = QualityDefault
	}
	return &Receiver{
		cfg:       cfg,
		video:     newMailbox(),
		audio:     make(chan audioFrame, 32),
		sendTally: cfg.Tally,
	}
}

// Video is the mailbox the decoder reads.
func (r *Receiver) Video() *mailbox { return r.video }

// Audio delivers converted audio frames; it is never closed.
func (r *Receiver) Audio() <-chan audioFrame { return r.audio }

func (r *Receiver) Stats() ReceiverStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ReceiverStats{
		Connected:     r.connected,
		ConnectedTo:   r.connTo,
		Since:         r.since,
		VideoFrames:   r.videoFrames.Load(),
		AudioFrames:   r.audioFrames.Load(),
		Bytes:         r.bytes.Load(),
		Dropped:       r.video.dropped.Load(),
		AudioDropped:  r.audioDropped.Load(),
		Reconnects:    r.reconnects.Load(),
		Video:         r.vinfo,
		Audio:         r.ainfo,
		Sender:        r.sender,
		Redirected:    r.redirect,
		LastError:     r.lastErr,
		LastErrorTime: r.lastErrAt,
	}
}

func (r *Receiver) setErr(err error) {
	r.mu.Lock()
	r.lastErr = err.Error()
	r.lastErrAt = time.Now()
	r.mu.Unlock()
}

// SetTally sends program tally to the sender (or clears it). The sender lights
// whatever it lights when any receiver says its output is on air.
func (r *Receiver) SetTally(on bool) {
	r.mu.Lock()
	r.sendTally = on
	c := r.videoConn
	r.mu.Unlock()
	if c != nil {
		msg := msgTallyNone
		if on {
			msg = msgTallyProgram
		}
		_ = writeMsg(c, msg)
	}
}

// Run connects, receives and reconnects until ctx ends.
func (r *Receiver) Run(ctx context.Context) {
	backoff := time.Second
	first := true
	var lastLogged string
	var lastLoggedAt time.Time
	for ctx.Err() == nil {
		if !first {
			r.reconnects.Add(1)
		}
		first = false
		start := time.Now()
		err := r.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// The same failure every few seconds through an outage is logged
			// once a minute, or it would push everything else out of the log.
			if msg := err.Error(); msg != lastLogged || time.Since(lastLoggedAt) > time.Minute {
				r.cfg.Log.Warn("omt: %s: %v", r.cfg.Source.Label(), err)
				lastLogged, lastLoggedAt = msg, time.Now()
			}
			r.setErr(err)
		}
		// A connection that lasted a while earns a quick retry; one that keeps
		// failing backs off to five seconds, so a vanished sender is polled
		// rather than hammered.
		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// session is one connection attempt: dial, subscribe, read until something
// breaks.
func (r *Receiver) session(ctx context.Context) error {
	src := r.cfg.Source
	r.mu.Lock()
	redirect := r.redirect
	if r.redirect != "" {
		src = Source{Name: r.redirect}
		if strings.Contains(r.redirect, "://") || isHostPort(r.redirect) {
			src = Source{Address: r.redirect}
		}
	}
	r.mu.Unlock()

	addrs, err := r.cfg.Resolve(ctx, src)
	if err != nil {
		return err
	}
	vc, err := dialAny(ctx, addrs)
	if err != nil {
		return err
	}
	var ac net.Conn
	if r.cfg.Audio {
		// The audio connection goes to the address the video one reached.
		if ac, err = dialAny(ctx, []string{vc.RemoteAddr().String()}); err != nil {
			vc.Close()
			return fmt.Errorf("audio connection: %w", err)
		}
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Closing the sockets is what unblocks the readers.
	go func() {
		<-sctx.Done()
		vc.Close()
		if ac != nil {
			ac.Close()
		}
	}()

	r.mu.Lock()
	tally := r.sendTally
	r.mu.Unlock()
	hello := []string{msgSubscribeMetadata, msgSubscribeVideo, qualityMessage(r.cfg.Quality)}
	if tally {
		hello = append(hello, msgTallyProgram)
	} else {
		hello = append(hello, msgTallyNone)
	}
	for _, m := range hello {
		if err := writeMsg(vc, m); err != nil {
			return fmt.Errorf("subscribe: %w", err)
		}
	}
	if ac != nil {
		if err := writeMsg(ac, msgSubscribeAudio); err != nil {
			return fmt.Errorf("subscribe audio: %w", err)
		}
	}

	r.mu.Lock()
	r.connected = true
	r.connTo = vc.RemoteAddr().String()
	r.since = time.Now()
	r.videoConn = vc
	r.mu.Unlock()
	r.cfg.Log.Info("omt: connected to %s at %s", src.Label(), vc.RemoteAddr())
	defer func() {
		r.mu.Lock()
		r.connected = false
		r.videoConn = nil
		r.mu.Unlock()
	}()

	errc := make(chan error, 2)
	go func() { errc <- r.readVideo(vc) }()
	if ac != nil {
		go func() { errc <- r.readAudio(ac) }()
	}
	err = <-errc
	r.mu.Lock()
	redirected := r.redirect != redirect
	r.mu.Unlock()
	if redirected {
		// handleMetadata closed the socket to follow a redirect; the read
		// error that caused is not a fault.
		return nil
	}
	if errors.Is(err, io.EOF) {
		err = errors.New("the sender closed the connection")
	}
	return err
}

func (r *Receiver) readVideo(c net.Conn) error {
	for {
		vf := r.video.get()
		f, buf, err := readFrame(c, vf.buf)
		vf.buf = buf
		if err != nil {
			r.video.release(vf)
			return err
		}
		r.bytes.Add(uint64(headerLen + len(buf)))
		switch f.Type {
		case frameVideo:
			r.videoFrames.Add(1)
			vf.hdr, vf.ts, vf.data = f.Video, f.Timestamp, f.Data
			r.noteVideo(f.Video)
			r.video.put(vf)
		case frameMetadata:
			r.handleMetadata(metadataText(f.Data))
			r.video.release(vf)
		default:
			r.video.release(vf)
		}
	}
}

func (r *Receiver) readAudio(c net.Conn) error {
	var buf []byte
	for {
		f, b, err := readFrame(c, buf)
		buf = b
		if err != nil {
			return err
		}
		r.bytes.Add(uint64(headerLen + len(buf)))
		switch f.Type {
		case frameAudio:
			r.audioFrames.Add(1)
			r.noteAudio(f.Audio)
			if f.Audio.Codec != codecFPA1 {
				continue
			}
			pcm, err := fpa1ToS16Stereo(f.Audio, f.Data, nil)
			if err != nil {
				r.setErr(err)
				continue
			}
			select {
			case r.audio <- audioFrame{rate: f.Audio.SampleRate, pcm: pcm}:
			default:
				r.audioDropped.Add(1)
			}
		case frameMetadata:
			r.handleMetadata(metadataText(f.Data))
		}
	}
}

func (r *Receiver) noteVideo(h VideoHeader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.vinfo; v != nil && v.Width == h.Width && v.Height == h.Height &&
		v.Codec == fourCCString(h.Codec) && v.Interlaced == (h.Flags&videoInterlaced != 0) &&
		v.ColorSpace == h.ColorSpace {
		return
	}
	fps := 0.0
	if h.FrameRateD > 0 {
		fps = float64(h.FrameRateN) / float64(h.FrameRateD)
	}
	r.vinfo = &VideoInfo{
		Codec:      fourCCString(h.Codec),
		Width:      h.Width,
		Height:     h.Height,
		FrameRate:  fps,
		Interlaced: h.Flags&videoInterlaced != 0,
		Alpha:      h.Flags&videoAlpha != 0,
		ColorSpace: h.ColorSpace,
	}
}

func (r *Receiver) noteAudio(h AudioHeader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.ainfo; a != nil && a.SampleRate == h.SampleRate && a.Channels == h.Channels {
		return
	}
	r.ainfo = &AudioInfo{Codec: fourCCString(h.Codec), SampleRate: h.SampleRate, Channels: h.Channels}
}

func (r *Receiver) handleMetadata(xml string) {
	switch {
	case strings.HasPrefix(xml, msgSenderInfoPrefix):
		info := parseSenderInfo(xml)
		r.mu.Lock()
		r.sender = info
		r.mu.Unlock()
	case strings.HasPrefix(xml, msgRedirectPrefix):
		// A sender can hand its receivers to another source (an empty address
		// hands them back). Follow it on the next connection.
		to := xmlAttr(xml, "NewAddress")
		r.mu.Lock()
		changed := to != r.redirect
		r.redirect = to
		c := r.videoConn
		r.mu.Unlock()
		if changed {
			if to == "" {
				r.cfg.Log.Info("omt: redirect cleared; back to %s", r.cfg.Source.Label())
			} else {
				r.cfg.Log.Info("omt: sender redirected us to %s", to)
			}
			if c != nil {
				c.Close()
			}
		}
	}
}

func writeMsg(c net.Conn, xml string) error {
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.Write(encodeMetadata(xml))
	_ = c.SetWriteDeadline(time.Time{})
	return err
}

// dialAny tries each address in turn, two seconds apiece (libomtnet's
// NETWORK_CONNECTION_TIMEOUT), and returns the first that answers.
func dialAny(ctx context.Context, addrs []string) (net.Conn, error) {
	if len(addrs) == 0 {
		return nil, errors.New("no address to connect to")
	}
	var last error
	for _, a := range addrs {
		d := net.Dialer{Timeout: 2 * time.Second, KeepAlive: 5 * time.Second}
		c, err := d.DialContext(ctx, "tcp", a)
		if err == nil {
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetNoDelay(true)
				// libomtnet asks for 8 MB; the kernel caps it at rmem_max, and
				// anything over a frame or two is headroom against a stall.
				_ = tc.SetReadBuffer(4 << 20)
			}
			return c, nil
		}
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("could not connect: %w", last)
}
