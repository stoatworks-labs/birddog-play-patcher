package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDecoder counts restarts and can refuse or be stopped.
type fakeDecoder struct {
	mu       sync.Mutex
	running  bool
	restarts int
	fail     error
	delay    time.Duration
	confs    []string // shim.conf's mode line at each restart: what PPApp read
	shimPath string
	pid      int
}

func (d *fakeDecoder) Running() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.running }
func (d *fakeDecoder) PID() int      { d.mu.Lock(); defer d.mu.Unlock(); return d.pid }
func (d *fakeDecoder) Restart() error {
	time.Sleep(d.delay)
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		return errDecoderStopped
	}
	d.restarts++
	b, _ := os.ReadFile(d.shimPath)
	d.confs = append(d.confs, ShimModeLine(string(b)))
	return d.fail
}
func (d *fakeDecoder) count() int { d.mu.Lock(); defer d.mu.Unlock(); return d.restarts }

func newRunner(t *testing.T, revert time.Duration) (*Runner, *fakeDecoder) {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim.conf")
	d := &fakeDecoder{running: true, shimPath: shim}
	r := &Runner{Store: &ConfigStore{Path: filepath.Join(dir, "config.json")}, ShimPath: shim,
		Decoder: d, RevertAfter: revert, Log: NewLogger()}
	r.Log.w = discard{}
	r.Boot()
	return r, d
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func shimMode(t *testing.T, r *Runner) string {
	t.Helper()
	b, err := os.ReadFile(r.ShimPath)
	if err != nil {
		t.Fatal(err)
	}
	return ShimModeLine(string(b))
}

var (
	f720  = Format{Kind: FormatStandard, Standard: "720p50"}
	f1200 = Format{Kind: FormatCustom, Width: 1920, Height: 1200, Refresh: 60}
)

func TestBootWritesFollowTheSource(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	if shimMode(t, r) != "" || d.count() != 0 {
		t.Fatal("a fresh box forces nothing and restarts nothing")
	}
}

func TestATrialNotKeptGoesBack(t *testing.T) {
	r, d := newRunner(t, 150*time.Millisecond)
	if err := r.TryFormat(f720); err != nil {
		t.Fatal(err)
	}
	m720, _ := f720.Resolve()
	if shimMode(t, r) != m720.ShimLine() || d.count() != 1 || d.confs[0] != m720.ShimLine() {
		t.Fatal("the decoder did not restart onto the format being tried")
	}
	st := r.Status()
	if st.Trial == nil || st.Trial.SecondsLeft < 0 || st.Kept.Format.Kind != FormatSource {
		t.Fatalf("%+v", st)
	}
	time.Sleep(400 * time.Millisecond)
	if shimMode(t, r) != "" || d.count() != 2 || d.confs[1] != "" {
		t.Fatalf("not reverted: %q after %d restarts", shimMode(t, r), d.count())
	}
	st = r.Status()
	if st.Trial != nil || st.Last == nil || !strings.Contains(st.Last.Text, "was not kept") {
		t.Fatalf("%+v", st)
	}
}

func TestKeepMakesItStick(t *testing.T) {
	r, d := newRunner(t, 150*time.Millisecond)
	if err := r.TryFormat(f1200); err != nil {
		t.Fatal(err)
	}
	if err := r.Keep(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	m, _ := f1200.Resolve()
	if shimMode(t, r) != m.ShimLine() || d.count() != 1 {
		t.Fatal("a kept format went back")
	}
	cfg, _ := r.Store.Load()
	if cfg.Format.Kind != FormatCustom || cfg.Format.Width != 1920 {
		t.Fatalf("not saved: %+v", cfg.Format)
	}
	if err := r.Keep(); !errors.Is(err, errNoTrial) {
		t.Fatal("kept twice")
	}
}

func TestRevertNow(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	r.TryFormat(f720)
	if err := r.Revert(); err != nil {
		t.Fatal(err)
	}
	if shimMode(t, r) != "" || d.count() != 2 || r.Status().Trial != nil {
		t.Fatal("revert did not put the kept format back")
	}
	if err := r.Revert(); !errors.Is(err, errNoTrial) {
		t.Fatal("reverted with nothing being tried")
	}
}

// Following the source is the PLAY as shipped: done at once, never tried.
func TestFollowTheSourceIsNotATrial(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	r.TryFormat(f720)
	r.Keep()
	if err := r.TryFormat(Format{Kind: FormatSource}); err != nil {
		t.Fatal(err)
	}
	st := r.Status()
	if st.Trial != nil || st.Kept.Format.Kind != FormatSource || shimMode(t, r) != "" || d.count() != 2 {
		t.Fatalf("%+v, %d restarts", st, d.count())
	}
}

// The kept timing under another name needs no trial and no restart.
func TestSameTimingIsNotATrial(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	r.TryFormat(f720)
	r.Keep()
	m, _ := f720.Resolve()
	if err := r.TryFormat(Format{Kind: FormatTiming, Timing: m}); err != nil {
		t.Fatal(err)
	}
	if r.Status().Trial != nil || d.count() != 1 {
		t.Fatal("the same timing was tried again")
	}
}

// A crash or power cut mid-trial: shim.conf holds the tried format, and the
// next start puts the kept one back.
func TestBootUndoesATrialLeftBehind(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	r.TryFormat(f720)
	r2 := &Runner{Store: r.Store, ShimPath: r.ShimPath, Decoder: d, RevertAfter: time.Minute, Log: r.Log}
	r2.Boot()
	deadline := time.Now().Add(time.Second)
	for d.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if shimMode(t, r2) != "" || d.count() != 2 {
		t.Fatalf("boot left %q after %d restarts", shimMode(t, r2), d.count())
	}
	// And --write-conf, the unit's ExecStartPre, does the same before PPApp starts.
	os.WriteFile(r.ShimPath, []byte(RenderShimConf(&Mode{Name: "x"}, AOI{})), 0o644)
	changed, err := WriteConf(r.Store, r.ShimPath)
	if err != nil || !changed || shimMode(t, r) != "" {
		t.Fatal(changed, err)
	}
}

// bd-out crashed or was restarted mid-try: ExecStartPre has already put the
// kept format back in shim.conf, so only the shim's status shows that PPApp is
// still on the one being tried.
func TestBootRestartsADecoderStillOnATrial(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	m, _ := f720.Resolve()
	status := filepath.Join(t.TempDir(), "status")
	write := func(mode string) {
		os.WriteFile(status, []byte("pid=77\nconf=ok\nmode="+mode+"\naoi=off\n"), 0o644)
	}
	boot := func() int {
		before := d.count()
		r2 := &Runner{Store: r.Store, ShimPath: r.ShimPath, ShimStatus: status, Decoder: d, RevertAfter: time.Minute, Log: r.Log}
		r2.Boot()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && d.count() == before {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		return d.count() - before
	}
	d.pid = 77
	write(fmt.Sprintf("forced %s at %d Hz (%d kHz), in place of the screen's own list", m.Name, int(m.Refresh()+0.5), m.ClockKHz))
	if n := boot(); n != 1 {
		t.Fatalf("PPApp on a format never kept: %d restarts", n)
	}
	write("off: the screen's own modes")
	if n := boot(); n != 0 {
		t.Fatalf("PPApp already following the source: %d restarts", n)
	}
	write("not asked yet")
	if n := boot(); n != 0 {
		t.Fatalf("PPApp not asked yet: %d restarts", n)
	}
	// A status from an earlier PPApp says nothing about this one.
	d.pid = 78
	write(fmt.Sprintf("forced %s at %d Hz (%d kHz)", m.Name, int(m.Refresh()+0.5), m.ClockKHz))
	if n := boot(); n != 0 {
		t.Fatalf("a stale status restarted the decoder: %d restarts", n)
	}
}

func TestShimStatusMatches(t *testing.T) {
	m, _ := f720.Resolve()
	other, _ := f1200.Resolve()
	forced := ShimStatus{Mode: fmt.Sprintf("forced %s at %d Hz (%d kHz), in place of the screen's own list", m.Name, int(m.Refresh()+0.5), m.ClockKHz)}
	if !forced.Matches(m) || forced.Matches(other) || forced.Matches(nil) {
		t.Fatal("forced")
	}
	off := ShimStatus{Mode: "off: the screen's own modes"}
	if !off.Matches(nil) || off.Matches(m) {
		t.Fatal("off")
	}
	for _, s := range []string{"not asked yet", "1920x1200 waiting: no screen connected", ""} {
		if !(ShimStatus{Mode: s}).Matches(m) || !(ShimStatus{Mode: s}).Matches(nil) {
			t.Fatalf("%q", s)
		}
	}
}

func TestDecoderStoppedRefusesATrial(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	d.running = false
	if err := r.TryFormat(f720); !errors.Is(err, errDecoderStopped) {
		t.Fatalf("got %v", err)
	}
	if shimMode(t, r) != "" {
		t.Fatal("shim.conf changed for a trial that never started")
	}
	// Following the source still saves: it takes when the decoder starts.
	if err := r.TryFormat(Format{Kind: FormatSource}); err != nil {
		t.Fatal(err)
	}
}

func TestAFailedRestartPutsTheKeptFormatBack(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	d.fail = errors.New("PPApp is not running after 20s")
	if err := r.TryFormat(f720); err == nil {
		t.Fatal("no error")
	}
	if shimMode(t, r) != "" || r.Status().Trial != nil {
		t.Fatal("the tried format stayed in shim.conf")
	}
}

func TestBusyRefusesASecondChange(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	d.delay = 200 * time.Millisecond
	done := make(chan error)
	go func() { done <- r.TryFormat(f720) }()
	time.Sleep(50 * time.Millisecond)
	if err := r.TryFormat(f1200); !errors.Is(err, errBusy) {
		t.Fatalf("got %v", err)
	}
	if st := r.Status(); !st.Busy || st.Trial == nil {
		t.Fatalf("%+v", st)
	}
	// An AOI goes in alongside the format being tried, restart or not.
	if err := r.SetAOI(AOI{On: true, Src: Rect{0, 0, 500000, Unit}, Dst: wholeRect}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(r.ShimPath)
	m, _ := f720.Resolve()
	if !strings.Contains(string(b), m.ShimLine()) || !strings.Contains(string(b), "aoi 0 0 500000") {
		t.Fatalf("shim.conf:\n%s", b)
	}
}

func TestAOINeedsNoRestart(t *testing.T) {
	r, d := newRunner(t, time.Minute)
	a := AOI{On: true, Src: Rect{500000, 0, 500000, Unit}, Dst: wholeRect, CanvasW: 3840, CanvasH: 1080}
	if err := r.SetAOI(a); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(r.ShimPath)
	if !strings.Contains(string(b), "aoi 500000 0 500000 1000000") || d.count() != 0 {
		t.Fatalf("%d restarts, shim.conf:\n%s", d.count(), b)
	}
	cfg, _ := r.Store.Load()
	if cfg.AOI != a {
		t.Fatal("AOI not saved")
	}
	if err := r.SetAOI(AOI{On: true, Src: Rect{0, 0, 0, 0}, Dst: wholeRect}); err == nil {
		t.Fatal("an empty area was accepted")
	}
}
