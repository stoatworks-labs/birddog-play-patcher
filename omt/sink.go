package main

// Getting decoded frames and sound out of the box.
//
// The only way to the panel on the PLAY is kmssink, and it takes NV12 and
// nothing else. bdcam proved the route on hardware: raw frames down a pipe into
// a gst-launch process that only scans out,
//
//	fdsrc ! rawvideoparse format=nv12 ! queue leaky ! kmssink sync=false
//
// rawvideoparse is what turns a byte stream back into frames (fdsrc with bare
// caps fails to preroll), and sync=false because frames off a pipe carry no
// timestamps worth syncing to: each is shown as it is decoded. Sound takes the
// same shape into the HDMI card with rawaudioparse and alsasink.
//
// Unlike bdcam's, this pipeline does not force a mode by default. The output
// stays at whatever resolution birdUI set and kmssink scales the source onto
// the plane, as bdplay's playback does — so a 720p source on a 1080p screen
// does not flick the TV into a new mode. "Match output to source" turns
// force-modesetting back on.
//
// Each sink has its own writer goroutine and a small buffer pool, so a slow
// pipe costs a dropped frame rather than a stalled decoder.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const gstBin = "gst-launch-1.0"

// hdmiAudioDevice is card 1 on the PLAY: "rockchiphdmi". Card 0 is the SoC's
// own i2s, which goes nowhere on this board.
const hdmiAudioDevice = "hw:1,0"

var errSinkClosed = errors.New("sink is not running")

// VideoSink shows NV12 frames.
type VideoSink interface {
	// Start (re)starts the output for w×h frames.
	Start(w, h int) error
	// Buffer returns a frame-sized buffer to decode into, or nil if every
	// buffer is still queued (the caller drops the frame).
	Buffer() []byte
	// Submit queues a buffer from Buffer for display.
	Submit(buf []byte) error
	// Recycle returns an unused buffer from Buffer.
	Recycle(buf []byte)
	Size() (w, h int)
	Stop()
	// Err reports why the output stopped, if it stopped by itself.
	Err() error
}

// AudioSink plays interleaved S16LE stereo.
type AudioSink interface {
	Start(rate int) error
	Write(pcm []byte) error
	Rate() int
	Stop()
	Err() error
}

// GstOptions are the pipeline knobs; the element names are overridable so the
// same pipelines run off a PLAY with fakesink.
type GstOptions struct {
	VideoSink   string // default "kmssink"
	AudioSink   string // default "alsasink device=hw:1,0"
	MatchOutput bool   // kmssink force-modesetting
	Log         *Logger
}

func (o GstOptions) videoSinkElem() []string {
	if o.VideoSink != "" {
		return strings.Fields(o.VideoSink)
	}
	el := []string{"kmssink", "sync=false"}
	if o.MatchOutput {
		el = append(el, "force-modesetting=true")
	}
	return el
}

func (o GstOptions) audioSinkElem() []string {
	if o.AudioSink != "" {
		return strings.Fields(o.AudioSink)
	}
	// A 100 ms ring buffer: enough to ride out a late packet, little enough
	// that sound does not lag the picture by much (nothing here lines the two
	// up; see README).
	return []string{"alsasink", "device=" + hdmiAudioDevice, "sync=false",
		"buffer-time=100000", "latency-time=10000"}
}

// videoPipeline is the gst-launch argument list for w×h NV12.
func videoPipeline(o GstOptions, w, h int) []string {
	_, _, size := NV12Layout(w, h)
	args := []string{
		"-q",
		"fdsrc", "fd=0", fmt.Sprintf("blocksize=%d", size),
		"!", "rawvideoparse", "use-sink-caps=false", "format=nv12",
		fmt.Sprintf("width=%d", w), fmt.Sprintf("height=%d", h),
		"framerate=60/1",
		"!", "queue", "max-size-buffers=2", "leaky=downstream",
		"!",
	}
	return append(args, o.videoSinkElem()...)
}

// audioPipeline is the gst-launch argument list for S16LE stereo at rate.
func audioPipeline(o GstOptions, rate int) []string {
	args := []string{
		"-q",
		"fdsrc", "fd=0", "blocksize=4096",
		"!", "rawaudioparse", "use-sink-caps=false", "format=pcm", "pcm-format=s16le",
		fmt.Sprintf("sample-rate=%d", rate), "num-channels=2", "interleaved=true",
		"!", "audioconvert", "!", "audioresample",
		"!",
	}
	return append(args, o.audioSinkElem()...)
}

// proc is one supervised gst-launch with its stdin.
type proc struct {
	cmd    *exec.Cmd
	stdin  *os.File
	done   chan struct{}
	stderr *tailBuffer
	err    error
}

func startProc(args []string, pipeSize int) (*proc, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	if pipeSize > 0 {
		setPipeSize(w, pipeSize)
	}
	cmd := exec.Command(gstBin, args...)
	cmd.Stdin = r
	tb := &tailBuffer{max: 4096}
	cmd.Stderr = tb
	cmd.Stdout = tb
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("start %s (is GStreamer installed?): %w", gstBin, err)
	}
	r.Close()
	p := &proc{cmd: cmd, stdin: w, done: make(chan struct{}), stderr: tb}
	go func() {
		err := cmd.Wait()
		if err == nil {
			err = errors.New("exited")
		}
		p.err = fmt.Errorf("%s %v: %s", gstBin, err, strings.TrimSpace(tb.String()))
		close(p.done)
	}()
	return p, nil
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// stop ends the pipeline politely (EOF, then SIGINT so gst-launch tears the
// pipeline down and kmssink releases its plane), then firmly.
func (p *proc) stop() {
	p.stdin.Close()
	select {
	case <-p.done:
		return
	case <-time.After(300 * time.Millisecond):
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGINT)
	select {
	case <-p.done:
		return
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	<-p.done
}

// gstVideo is the kmssink output.
type gstVideo struct {
	opts GstOptions

	mu    sync.Mutex
	w, h  int
	size  int
	p     *proc
	queue chan []byte
	free  chan []byte
	err   error
	wg    sync.WaitGroup
}

func NewGstVideo(o GstOptions) *gstVideo { return &gstVideo{opts: o} }

func (g *gstVideo) Start(w, h int) error {
	g.Stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	_, _, size := NV12Layout(w, h)
	p, err := startProc(videoPipeline(g.opts, w, h), size)
	if err != nil {
		return err
	}
	g.w, g.h, g.size, g.p, g.err = w, h, size, p, nil
	g.queue = make(chan []byte, 1)
	g.free = make(chan []byte, 3)
	for i := 0; i < 3; i++ {
		g.free <- make([]byte, size)
	}
	g.wg.Add(1)
	go g.writer(p, g.queue, g.free)
	g.opts.Log.Info("video: %s", strings.Join(videoPipeline(g.opts, w, h)[1:], " "))
	return nil
}

func (g *gstVideo) writer(p *proc, queue, free chan []byte) {
	defer g.wg.Done()
	for buf := range queue {
		_, err := p.stdin.Write(buf)
		free <- buf
		if err != nil {
			g.mu.Lock()
			if g.err == nil {
				g.err = fmt.Errorf("video pipeline: %v", err)
			}
			g.mu.Unlock()
			// Drain so Submit never blocks on a dead pipe.
			for b := range queue {
				free <- b
			}
			return
		}
	}
}

func (g *gstVideo) Buffer() []byte {
	g.mu.Lock()
	free := g.free
	g.mu.Unlock()
	if free == nil {
		return nil
	}
	select {
	case b := <-free:
		return b
	default:
		return nil
	}
}

func (g *gstVideo) Recycle(buf []byte) {
	g.mu.Lock()
	free, size := g.free, g.size
	g.mu.Unlock()
	if free != nil && len(buf) == size {
		select {
		case free <- buf:
		default:
		}
	}
}

func (g *gstVideo) Submit(buf []byte) error {
	g.mu.Lock()
	p, q, err, size := g.p, g.queue, g.err, g.size
	g.mu.Unlock()
	if p == nil {
		return errSinkClosed
	}
	if err != nil {
		return err
	}
	if p.exited() {
		return p.err
	}
	if len(buf) != size {
		return fmt.Errorf("video: buffer is %d bytes, pipeline wants %d", len(buf), size)
	}
	q <- buf
	return nil
}

func (g *gstVideo) Size() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.w, g.h
}

func (g *gstVideo) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	if g.p != nil && g.p.exited() {
		return g.p.err
	}
	return nil
}

func (g *gstVideo) Stop() {
	g.mu.Lock()
	p, q := g.p, g.queue
	g.p, g.queue, g.free = nil, nil, nil
	g.w, g.h = 0, 0
	g.mu.Unlock()
	if p == nil {
		return
	}
	close(q)
	p.stop()
	g.wg.Wait()
}

// gstAudio is the alsasink output, behind a queue capped by duration so sound
// never builds up seconds of delay behind a stalled sink.
type gstAudio struct {
	opts GstOptions

	mu      sync.Mutex
	rate    int
	p       *proc
	pending [][]byte
	bytes   int
	cond    *sync.Cond
	closed  bool
	err     error
	dropped uint64
	wg      sync.WaitGroup
}

func NewGstAudio(o GstOptions) *gstAudio {
	a := &gstAudio{opts: o}
	a.cond = sync.NewCond(&a.mu)
	return a
}

// maxQueued is how much sound may wait for the sink: 120 ms.
func (a *gstAudio) maxQueued() int { return a.rate * 4 * 120 / 1000 }

func (a *gstAudio) Start(rate int) error {
	a.Stop()
	if rate <= 0 || rate > 192000 {
		return fmt.Errorf("audio: unsupported sample rate %d", rate)
	}
	// A small pipe keeps the kernel from holding a third of a second of sound.
	p, err := startProc(audioPipeline(a.opts, rate), 8192)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.rate, a.p, a.pending, a.bytes, a.closed, a.err = rate, p, nil, 0, false, nil
	a.mu.Unlock()
	a.wg.Add(1)
	go a.writer(p)
	a.opts.Log.Info("audio: %s", strings.Join(audioPipeline(a.opts, rate)[1:], " "))
	return nil
}

func (a *gstAudio) writer(p *proc) {
	defer a.wg.Done()
	for {
		a.mu.Lock()
		for len(a.pending) == 0 && !a.closed {
			a.cond.Wait()
		}
		if a.closed {
			a.mu.Unlock()
			return
		}
		buf := a.pending[0]
		a.pending = a.pending[1:]
		a.bytes -= len(buf)
		a.mu.Unlock()
		if _, err := p.stdin.Write(buf); err != nil {
			a.mu.Lock()
			if a.err == nil {
				a.err = fmt.Errorf("audio pipeline: %v", err)
			}
			a.mu.Unlock()
			return
		}
	}
}

func (a *gstAudio) Write(pcm []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.p == nil {
		return errSinkClosed
	}
	if a.err != nil {
		return a.err
	}
	if a.p.exited() {
		return a.p.err
	}
	// Over budget: drop the oldest, so what plays is as current as possible.
	for a.bytes+len(pcm) > a.maxQueued() && len(a.pending) > 0 {
		a.bytes -= len(a.pending[0])
		a.pending = a.pending[1:]
		a.dropped++
	}
	a.pending = append(a.pending, pcm)
	a.bytes += len(pcm)
	a.cond.Signal()
	return nil
}

func (a *gstAudio) Rate() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rate
}

func (a *gstAudio) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	if a.p != nil && a.p.exited() {
		return a.p.err
	}
	return nil
}

func (a *gstAudio) Stop() {
	a.mu.Lock()
	p := a.p
	a.p = nil
	a.rate = 0
	a.closed = true
	a.cond.Broadcast()
	a.mu.Unlock()
	if p == nil {
		return
	}
	p.stop()
	a.wg.Wait()
}

// tailBuffer keeps the last max bytes written to it: a pipeline's stderr, for
// the error message when it dies.
type tailBuffer struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ")
}

// gstHasElement asks gst-inspect whether an element exists.
func gstHasElement(name string) bool {
	return exec.Command("gst-inspect-1.0", "--exists", name).Run() == nil
}

// fileVideo writes NV12 frames to a file (or discards them): the --receive
// harness's sink, for checking decode without a display.
type fileVideo struct {
	mu     sync.Mutex
	out    io.Writer
	w, h   int
	buf    []byte
	frames int
	limit  int
	sums   []uint64
}

func (f *fileVideo) Start(w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _, size := NV12Layout(w, h)
	f.w, f.h, f.buf = w, h, make([]byte, size)
	return nil
}

func (f *fileVideo) Buffer() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf
}

func (f *fileVideo) Recycle([]byte) {}

func (f *fileVideo) Submit(buf []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames++
	f.sums = append(f.sums, fnv64(buf))
	if f.out != nil && (f.limit == 0 || f.frames <= f.limit) {
		_, err := f.out.Write(buf)
		return err
	}
	return nil
}

func (f *fileVideo) Size() (int, int) { f.mu.Lock(); defer f.mu.Unlock(); return f.w, f.h }
func (f *fileVideo) Stop()            {}
func (f *fileVideo) Err() error       { return nil }

type fileAudio struct {
	mu    sync.Mutex
	out   io.Writer
	rate  int
	bytes int
}

func (f *fileAudio) Start(rate int) error { f.mu.Lock(); f.rate = rate; f.mu.Unlock(); return nil }
func (f *fileAudio) Write(pcm []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes += len(pcm)
	if f.out != nil {
		_, err := f.out.Write(pcm)
		return err
	}
	return nil
}
func (f *fileAudio) Rate() int  { f.mu.Lock(); defer f.mu.Unlock(); return f.rate }
func (f *fileAudio) Stop()      {}
func (f *fileAudio) Err() error { return nil }

func fnv64(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}
