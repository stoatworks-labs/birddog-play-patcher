package main

// Taking HDMI from the PLAY's own decoder, and always giving it back.
//
// PPApp holds DRM master on /dev/dri/card0 for as long as it runs and the
// kernel allows one master, so showing OMT means stopping BirdDog's decode
// stack, and stopping OMT means starting it again. This is bdplay's handover
// (player/display.go), with the traps it learned on hardware kept intact:
//
//   - BirdDogRunner.service is Restart=always, so PPApp is stopped through
//     systemctl, never killed, or systemd brings it straight back mid-frame.
//   - PPApp aborts in its own NDI teardown often enough that cycling it gets a
//     unit rate-limited, after which a plain `start` is refused and HDMI stays
//     dark. Restore runs `reset-failed` first and then checks PPApp really came
//     back, because `systemctl start` returning 0 only means systemd queued it.
//   - If BirdDogRunner was already stopped when we took over, someone meant
//     that; it stays stopped when we let go.
//
// And bdcam's marker: while we hold the display, /run/bdomt-took-display
// exists, and bd-omt.service's ExecStopPost restarts BirdDogRunner if it finds
// the marker. That covers the one exit this process cannot clean up after —
// being killed outright. /run is tmpfs, so a reboot clears it.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	birdDogRunner = "BirdDogRunner.service"
	tookMarker    = "/run/bdomt-took-display"
)

// DisplayOwner is what the session needs from the handover; tests fake it.
type DisplayOwner interface {
	Acquire() error
	Release()
	Held() bool
}

// Display owns the handover of DRM master between PPApp and us.
type Display struct {
	mu        sync.Mutex
	held      bool
	wasActive bool
	marker    string
	log       *Logger
}

func NewDisplay(log *Logger) *Display { return &Display{log: log, marker: tookMarker} }

// Acquire stops BirdDog's decode stack and waits for PPApp to let go of DRM.
func (d *Display) Acquire() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.held {
		return nil
	}
	d.wasActive = serviceActive(birdDogRunner)
	if d.wasActive {
		// The marker goes down before the stop, so a kill at any point after
		// this leaves systemd a note to put the decoder back.
		if err := os.WriteFile(d.marker, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
			d.log.Warn("could not write %s (%v); a kill -9 now would leave HDMI dark until reboot", d.marker, err)
		}
		d.log.Info("stopping %s to take the display", birdDogRunner)
		if out, err := exec.Command("systemctl", "stop", birdDogRunner).CombinedOutput(); err != nil {
			os.Remove(d.marker)
			return fmt.Errorf("stop %s: %v: %s", birdDogRunner, err, strings.TrimSpace(string(out)))
		}
	} else if processRunning("gst-launch-1.0") {
		// BirdDogRunner is down and a GStreamer pipeline is up: bdplay or
		// bdcam has the display. Their kmssink is DRM master, ours would fail
		// to open, and stopping them is not ours to do.
		return fmt.Errorf("another module (USB player or UVC converter) is using the HDMI output; stop it first")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processRunning("PPApp") {
			d.held = true
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	d.held = true
	d.restoreLocked()
	return fmt.Errorf("PPApp still running 10s after stopping %s", birdDogRunner)
}

// Release restarts BirdDog's decode stack if we stopped it.
func (d *Display) Release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restoreLocked()
}

func (d *Display) restoreLocked() {
	if !d.held {
		return
	}
	d.held = false
	if !d.wasActive {
		d.log.Info("%s was not running before we took the display; leaving it stopped", birdDogRunner)
		return
	}
	defer os.Remove(d.marker)
	d.log.Info("restarting %s", birdDogRunner)
	if out, err := exec.Command("systemctl", "reset-failed", birdDogRunner).CombinedOutput(); err != nil {
		d.log.Warn("reset-failed %s: %v: %s", birdDogRunner, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "start", birdDogRunner).CombinedOutput(); err != nil {
		d.log.Error("FAILED to restart %s: %v: %s — HDMI will stay dark until it starts (systemctl reset-failed %s && systemctl start %s)",
			birdDogRunner, err, strings.TrimSpace(string(out)), birdDogRunner, birdDogRunner)
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if processRunning("PPApp") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	d.log.Error("%s was started but PPApp is not running after 15s — HDMI output will be dark. Check 'journalctl -u %s'",
		birdDogRunner, birdDogRunner)
}

func (d *Display) Held() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.held
}

func serviceActive(unit string) bool {
	out, _ := exec.Command("systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out)) == "active"
}

func processRunning(name string) bool {
	// -x: the exact process name, so a shell mentioning it does not match.
	return exec.Command("pgrep", "-x", name).Run() == nil
}

// noDisplay is the handover for running off a PLAY (--display=none): there is
// no PPApp to stop, so it only records that the output is "ours".
type noDisplay struct {
	mu   sync.Mutex
	held bool
}

func (n *noDisplay) Acquire() error { n.mu.Lock(); n.held = true; n.mu.Unlock(); return nil }
func (n *noDisplay) Release()       { n.mu.Lock(); n.held = false; n.mu.Unlock() }
func (n *noDisplay) Held() bool     { n.mu.Lock(); defer n.mu.Unlock(); return n.held }

// OutputMode is the HDMI mode PPApp left the output in, read from the Rockchip
// VOP's debug summary. Falls back to 1080p.
type OutputMode struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func DetectMode() OutputMode {
	def := OutputMode{1920, 1080}
	b, err := os.ReadFile("/sys/kernel/debug/dri/0/summary")
	if err != nil {
		return def
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Display mode:") {
			continue
		}
		var w, h int
		if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "Display mode:")), "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
			return OutputMode{w, h}
		}
	}
	return def
}
