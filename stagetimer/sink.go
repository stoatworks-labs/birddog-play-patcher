package main

// Frames to HDMI: bdomt's video sink (omt/sink.go), video only.
//
// The only way to the panel on the PLAY is kmssink, and it takes NV12 and
// nothing else. bdcam proved the route on hardware:
//
//	fdsrc ! rawvideoparse format=nv12 ! queue leaky ! kmssink sync=false
//
// rawvideoparse turns the byte stream back into frames; sync=false shows each
// as it arrives. A timer's frames arrive when the picture changes — about once
// a second — and kmssink holds the last one on the plane in between, so
// nothing is sent while nothing changes.
//
// No mode is forced. The output stays at whatever birdUI set and kmssink
// scales our frame onto it, as bdplay's playback and bdomt do.

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const gstBin = "gst-launch-1.0"

var errSinkClosed = errors.New("the HDMI output is not running")

// FrameSink shows frames.
type FrameSink interface {
	Start(w, h int) error
	// Show sends one frame. It may keep img only until it returns.
	Show(img *image.RGBA) error
	Stop()
	Err() error
}

// videoPipeline is the gst-launch argument list for w×h NV12.
func videoPipeline(sinkElem []string, w, h int) []string {
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
	if len(sinkElem) == 0 {
		sinkElem = []string{"kmssink", "sync=false"}
	}
	return append(args, sinkElem...)
}

// GstSink pipes NV12 into gst-launch.
type GstSink struct {
	SinkElem []string // replaces "kmssink sync=false" (testing off a PLAY)
	Log      *Logger

	mu   sync.Mutex
	w, h int
	buf  []byte
	cmd  *exec.Cmd
	in   *os.File
	done chan struct{}
	tail *tailBuffer
	err  error
}

func (g *GstSink) Start(w, h int) error {
	g.Stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	_, _, size := NV12Layout(w, h)
	r, wr, err := os.Pipe()
	if err != nil {
		return err
	}
	setPipeSize(wr, size)
	args := videoPipeline(g.SinkElem, w, h)
	cmd := exec.Command(gstBin, args...)
	cmd.Stdin = r
	tb := &tailBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = tb, tb
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		r.Close()
		wr.Close()
		return fmt.Errorf("start %s (is GStreamer installed?): %w", gstBin, err)
	}
	r.Close()
	done := make(chan struct{})
	g.w, g.h, g.buf, g.cmd, g.in, g.done, g.tail, g.err = w, h, make([]byte, size), cmd, wr, done, tb, nil
	go func() {
		err := cmd.Wait()
		if err == nil {
			err = errors.New("exited")
		}
		g.mu.Lock()
		if g.done == done && g.err == nil {
			g.err = fmt.Errorf("%s %v: %s", gstBin, err, strings.TrimSpace(tb.String()))
		}
		g.mu.Unlock()
		close(done)
	}()
	g.Log.Info("video: %s %s", gstBin, strings.Join(args[1:], " "))
	return nil
}

func (g *GstSink) Show(img *image.RGBA) error {
	g.mu.Lock()
	in, buf, err := g.in, g.buf, g.err
	w, h := g.w, g.h
	g.mu.Unlock()
	if in == nil {
		return errSinkClosed
	}
	if err != nil {
		return err
	}
	if img.Rect.Dx() != w || img.Rect.Dy() != h {
		return fmt.Errorf("video: frame is %dx%d, pipeline wants %dx%d", img.Rect.Dx(), img.Rect.Dy(), w, h)
	}
	ToNV12(img, buf)
	if _, err := in.Write(buf); err != nil {
		g.mu.Lock()
		if g.err == nil {
			g.err = fmt.Errorf("video pipeline: %v (%s)", err, strings.TrimSpace(g.tail.String()))
		}
		err = g.err
		g.mu.Unlock()
		return err
	}
	return nil
}

func (g *GstSink) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

// Stop ends the pipeline politely (EOF, then SIGINT so gst-launch tears down
// and kmssink lets go of its plane), then firmly.
func (g *GstSink) Stop() {
	g.mu.Lock()
	cmd, in, done := g.cmd, g.in, g.done
	g.cmd, g.in, g.done = nil, nil, nil
	g.mu.Unlock()
	if cmd == nil {
		return
	}
	in.Close()
	select {
	case <-done:
		return
	case <-time.After(300 * time.Millisecond):
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
}

// PNGSink writes each frame as a PNG (and the latest as latest.png): the
// off-PLAY sink, for looking at the screens on a desk.
type PNGSink struct {
	Dir  string
	Keep int // numbered frames to write; 0 writes only latest.png

	mu sync.Mutex
	n  int
}

func (p *PNGSink) Start(w, h int) error { return os.MkdirAll(p.Dir, 0o755) }
func (p *PNGSink) Stop()                {}
func (p *PNGSink) Err() error           { return nil }

func (p *PNGSink) Show(img *image.RGBA) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	if p.Keep > 0 && p.n <= p.Keep {
		if err := writePNG(filepath.Join(p.Dir, fmt.Sprintf("frame-%04d.png", p.n)), img); err != nil {
			return err
		}
	}
	return writePNG(filepath.Join(p.Dir, "latest.png"), img)
}

func writePNG(path string, img image.Image) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(f, img); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
