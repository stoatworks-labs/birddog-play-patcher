package main

// Putting a format and an AOI into effect, and taking a format back when
// nobody says it worked.
//
// A format the screen cannot show leaves it dark, and the person looking at a
// dark screen may have no other way to see the PLAY. So a new format is tried,
// not set: shim.conf gets it and the decoder restarts, and unless Keep comes
// within RevertAfter the last kept format goes back the same way — a monitor's
// "Keep these display settings?". Only Keep writes a format to config.json, so
// a power cut or a crash mid-try comes back to the kept one too: Boot (and
// --write-conf, the unit's ExecStartPre) writes shim.conf from config.json.
//
// Going back to following the source is never tried, only done: it is the PLAY
// as BirdDog ships it. An AOI is never tried either: it cannot leave the screen
// dark, and the shim takes it without a restart.

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

type Runner struct {
	Store       *ConfigStore
	ShimPath    string
	ShimStatus  string // the shim's status file: what PPApp was last handed
	Decoder     Decoder
	RevertAfter time.Duration
	Log         *Logger

	mu    sync.Mutex
	trial *trial
	gen   int  // counts trials, so a stale revert timer does nothing
	busy  bool // a decoder restart is under way
	last  *Event
}

type trial struct {
	format   Format
	label    string
	deadline time.Time
	timer    *time.Timer
}

// Event is the last thing the runner did that the tab should mention.
type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // ok, warn, err
	Text string    `json:"text"`
}

type TrialStatus struct {
	Format      Format `json:"format"`
	Label       string `json:"label"`
	SecondsLeft int    `json:"seconds_left"`
}

type RunnerStatus struct {
	Kept      Config       `json:"kept"`
	KeptLabel string       `json:"kept_label"`
	KeptMode  *Mode        `json:"kept_mode,omitempty"`
	Trial     *TrialStatus `json:"trial,omitempty"`
	Busy      bool         `json:"busy"`
	Last      *Event       `json:"last,omitempty"`
	ConfError string       `json:"conf_error,omitempty"`
}

var (
	errBusy    = errors.New("the PLAY's decoder is restarting for a format change; wait a few seconds")
	errNoTrial = errors.New("no format is being tried")
)

func (r *Runner) note(kind, format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	r.last = &Event{Time: time.Now(), Kind: kind, Text: text}
	switch kind {
	case "err":
		r.Log.Error("%s", text)
	case "warn":
		r.Log.Warn("%s", text)
	default:
		r.Log.Info("%s", text)
	}
}

// WriteConf writes shim.conf from config.json and reports whether the forced
// format changed, i.e. whether the decoder needs a restart for it to take.
func WriteConf(store *ConfigStore, shimPath string) (bool, error) {
	cfg, err := store.Load()
	changed, werr := writeShim(shimPath, cfg.Format, cfg.AOI)
	if werr != nil {
		return changed, werr
	}
	return changed, err
}

func writeShim(path string, f Format, a AOI) (modeChanged bool, err error) {
	mode, err := f.Resolve()
	if err != nil {
		return false, err
	}
	conf := RenderShimConf(mode, a)
	old, rerr := os.ReadFile(path)
	if rerr == nil && string(old) == conf {
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(conf), 0o644); err != nil {
		return false, err
	}
	return ShimModeLine(string(old)) != ShimModeLine(conf), nil
}

// Boot puts the kept settings in force. After a power cut mid-try the unit's
// ExecStartPre has already done it, before the decoder started. After bd-out
// itself crashed or was restarted mid-try, ExecStartPre has put the kept
// format back in shim.conf but PPApp is still running the one being tried —
// which only the shim's status can tell, so that is what decides the restart.
func (r *Runner) Boot() {
	changed, err := WriteConf(r.Store, r.ShimPath)
	if err != nil {
		r.Log.Warn("config: %v — following the source until it is fixed", err)
	}
	if !changed && r.ShimStatus != "" {
		cfg, _ := r.Store.Load()
		kept, _ := cfg.Format.Resolve()
		st := ReadShimStatus(r.ShimStatus)
		if pid := r.Decoder.PID(); st.Present && pid > 0 && st.PID == pid && !st.Matches(kept) {
			r.Log.Info("the PLAY's decoder is on a format that was never kept (%s)", st.Mode)
			changed = true
		}
	}
	if !changed {
		return
	}
	r.Log.Info("putting the kept format back: %s", func() string { c, _ := r.Store.Load(); return c.Format.Label() }())
	if !r.Decoder.Running() {
		return
	}
	r.mu.Lock()
	r.busy = true
	r.mu.Unlock()
	go func() {
		err := r.Decoder.Restart()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.busy = false
		if err != nil {
			r.note("err", "%v", err)
		}
	}()
}

// active is the format in shim.conf: the one being tried, or the kept one.
func (r *Runner) activeLocked(cfg Config) Format {
	if r.trial != nil {
		return r.trial.format
	}
	return cfg.Format
}

// TryFormat puts a format on HDMI. Anything that forces a timing the kept
// format does not is a trial, reverted unless kept in time; the source's own
// format, or the kept timing under another name, is just kept.
func (r *Runner) TryFormat(f Format) error {
	mode, err := f.Resolve()
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return errBusy
	}
	cfg, err := r.Store.Load()
	if err != nil {
		r.Log.Warn("config: %v", err)
	}
	kept, _ := cfg.Format.Resolve()

	if mode == nil || (kept != nil && mode.ShimLine() == kept.ShimLine()) {
		r.stopTrialLocked()
		cfg.Format = f
		if err := r.Store.Save(cfg); err != nil {
			r.mu.Unlock()
			return err
		}
		changed, err := writeShim(r.ShimPath, f, cfg.AOI)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		r.note("ok", "format: %s", f.Label())
		if !changed || !r.Decoder.Running() {
			r.mu.Unlock()
			return nil
		}
		return r.restartUnlocking(func(err error) {
			r.note("err", "%v", err)
		})
	}

	if !r.Decoder.Running() {
		r.mu.Unlock()
		return errDecoderStopped
	}
	r.stopTrialLocked()
	label := f.Label()
	r.gen++
	r.trial = &trial{format: f, label: label}
	if _, err := writeShim(r.ShimPath, f, cfg.AOI); err != nil {
		r.trial = nil
		r.mu.Unlock()
		return err
	}
	r.note("ok", "trying %s; going back to %s in %s unless kept", label, cfg.Format.Label(), r.RevertAfter)
	gen := r.gen
	var restartErr error
	_ = r.restartUnlocking(func(err error) { restartErr = err })

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.trial == nil || r.gen != gen {
		return nil // reverted or replaced while the decoder restarted
	}
	if restartErr != nil {
		r.trial = nil
		if _, err := WriteConf(r.Store, r.ShimPath); err != nil {
			r.Log.Error("putting the kept format back: %v", err)
		}
		r.note("err", "%v; the kept format goes back the next time the decoder starts", restartErr)
		return restartErr
	}
	// The countdown starts once PPApp is back, so the time is all for looking.
	r.trial.deadline = time.Now().Add(r.RevertAfter)
	r.trial.timer = time.AfterFunc(r.RevertAfter, func() {
		r.revert(gen, fmt.Sprintf("%s was not kept within %s", label, r.RevertAfter))
	})
	return nil
}

// restartUnlocking restarts the decoder with mu released, which it must be
// held for on entry; it returns with mu released. busy keeps other format
// changes out meanwhile.
func (r *Runner) restartUnlocking(onErr func(error)) error {
	r.busy = true
	r.mu.Unlock()
	err := r.Decoder.Restart()
	r.mu.Lock()
	r.busy = false
	if err != nil {
		onErr(err)
	}
	r.mu.Unlock()
	return err
}

func (r *Runner) stopTrialLocked() {
	if r.trial != nil && r.trial.timer != nil {
		r.trial.timer.Stop()
	}
	r.trial = nil
}

// Keep makes the format being tried the kept one.
func (r *Runner) Keep() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.trial == nil {
		return errNoTrial
	}
	cfg, _ := r.Store.Load()
	cfg.Format = r.trial.format
	if err := r.Store.Save(cfg); err != nil {
		return err
	}
	r.note("ok", "kept %s", r.trial.label)
	r.stopTrialLocked()
	return nil
}

// Revert ends a trial now.
func (r *Runner) Revert() error {
	r.mu.Lock()
	if r.trial == nil {
		r.mu.Unlock()
		return errNoTrial
	}
	gen := r.gen
	r.mu.Unlock()
	r.revert(gen, "went back at your request")
	return nil
}

func (r *Runner) revert(gen int, why string) {
	r.mu.Lock()
	if r.trial == nil || r.gen != gen {
		r.mu.Unlock()
		return
	}
	for r.busy { // a restart for this very trial is still finishing
		r.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		r.mu.Lock()
		if r.trial == nil || r.gen != gen {
			r.mu.Unlock()
			return
		}
	}
	r.stopTrialLocked()
	cfg, _ := r.Store.Load()
	changed, err := WriteConf(r.Store, r.ShimPath)
	if err != nil {
		r.Log.Error("putting the kept format back: %v", err)
	}
	r.note("warn", "%s: back to %s", why, cfg.Format.Label())
	if !changed || !r.Decoder.Running() {
		r.mu.Unlock()
		return
	}
	_ = r.restartUnlocking(func(err error) { r.note("err", "%v", err) })
}

// SetAOI saves and applies an AOI. No restart: the shim reads it within half
// a second. With a format being tried, it goes in alongside that.
func (r *Runner) SetAOI(a AOI) error {
	if err := a.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, _ := r.Store.Load()
	cfg.AOI = a
	if err := r.Store.Save(cfg); err != nil {
		return err
	}
	if _, err := writeShim(r.ShimPath, r.activeLocked(cfg), a); err != nil {
		return err
	}
	if a.Active() {
		r.note("ok", "area of interest on")
	} else {
		r.note("ok", "area of interest off: the whole picture on the whole screen")
	}
	return nil
}

// RestartDecoder restarts the decoder by hand: after an install, before which
// PPApp was started without the shim.
func (r *Runner) RestartDecoder() error {
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return errBusy
	}
	if !r.Decoder.Running() {
		r.mu.Unlock()
		return errDecoderStopped
	}
	r.note("ok", "restarting the PLAY's decoder")
	return r.restartUnlocking(func(err error) { r.note("err", "%v", err) })
}

func (r *Runner) Status() RunnerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, err := r.Store.Load()
	st := RunnerStatus{Kept: cfg, KeptLabel: cfg.Format.Label(), Busy: r.busy, Last: r.last}
	st.KeptMode, _ = cfg.Format.Resolve()
	if err != nil {
		st.ConfError = err.Error()
	}
	if t := r.trial; t != nil {
		ts := &TrialStatus{Format: t.format, Label: t.label}
		if !t.deadline.IsZero() {
			ts.SecondsLeft = int(time.Until(t.deadline).Round(time.Second) / time.Second)
			if ts.SecondsLeft < 0 {
				ts.SecondsLeft = 0
			}
		} else {
			ts.SecondsLeft = int(r.RevertAfter / time.Second) // still restarting
		}
		st.Trial = ts
	}
	return st
}
