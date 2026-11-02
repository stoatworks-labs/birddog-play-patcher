package main

// Restarting the PLAY's own decoder, which is how a new format takes.
//
// PPApp reads the screen's mode list when it starts, so a change of format is
// shim.conf rewritten and BirdDogRunner restarted; an AOI change needs neither,
// the shim picks it up within half a second. The restart keeps the traps
// bdplay and bdomt learned (omt/display.go):
//
//   - BirdDogRunner.service is Restart=always: go through systemctl, never
//     kill PPApp.
//   - PPApp aborts in its own NDI teardown often enough to get the unit
//     rate-limited, so reset-failed comes first, and a restart is only done
//     once PPApp is seen running again, because systemctl returning 0 only
//     means systemd queued it.
//   - try-restart, never start: BirdDogRunner stopped means another module
//     (the OMT receiver, the USB player, the UVC converter) has HDMI, or
//     someone meant it to be off. Not ours to undo.

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const birdDogRunner = "BirdDogRunner.service"

// Decoder is what the runner needs from the PLAY's decoder; tests fake it.
type Decoder interface {
	// Running reports whether BirdDogRunner is up.
	Running() bool
	// Restart restarts it if it is running, and waits for PPApp.
	Restart() error
	// PID is PPApp's process ID, 0 when it is not running, -1 when there is
	// no PPApp to check the shim's status against (off a PLAY).
	PID() int
}

type systemdDecoder struct{ log *Logger }

func (d systemdDecoder) Running() bool {
	out, _ := exec.Command("systemctl", "is-active", birdDogRunner).Output()
	return strings.TrimSpace(string(out)) == "active"
}

func (d systemdDecoder) PID() int {
	// -x: the exact process name, so a shell mentioning it does not match.
	out, err := exec.Command("pgrep", "-x", "PPApp").Output()
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.Fields(string(out) + " 0")[0])
	return pid
}

func (d systemdDecoder) Restart() error {
	if !d.Running() {
		return errDecoderStopped
	}
	before := d.PID()
	d.log.Info("restarting %s so PPApp reads the format again", birdDogRunner)
	if out, err := exec.Command("systemctl", "reset-failed", birdDogRunner).CombinedOutput(); err != nil {
		d.log.Warn("reset-failed %s: %v: %s", birdDogRunner, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "try-restart", birdDogRunner).CombinedOutput(); err != nil {
		return fmt.Errorf("restart %s: %v: %s", birdDogRunner, err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if pid := d.PID(); pid != 0 && pid != before {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s was restarted but PPApp is not running after 20s — check 'journalctl -u %s'", birdDogRunner, birdDogRunner)
}

var errDecoderStopped = fmt.Errorf("the PLAY's own decoder (%s) is not running — another module has HDMI, or it was stopped. "+
	"A format takes when the decoder starts; try formats while it is running", birdDogRunner)

// noDecoder is for running off a PLAY (--decoder=none): restarts succeed at
// once, and whatever status file there is counts as the shim's.
type noDecoder struct{}

func (noDecoder) Running() bool  { return true }
func (noDecoder) Restart() error { return nil }
func (noDecoder) PID() int       { return -1 }
