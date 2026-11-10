package main

// bdstage — Stagetimer on a BirdDog PLAY's HDMI output.
//
// Reads a Stagetimer room over its API (stagetimer.go, socketio.go, feed.go),
// draws the countdown, the rundown or the clock itself (model.go, render.go),
// and puts the picture on HDMI through kmssink (sink.go) once the PLAY's own
// decoder has handed the display over (display.go).
//
//	--serve :8096     the daemon: the Stagetimer tab's API and the screen it drives
//	--patch-ui        add the Stagetimer tab to birdUI's AV Setup page (and remove it)
//	--probe           connect to a room and print what arrives, then exit
//	--demo DIR        draw every sample screen as a PNG, then exit

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // room time zones on a box whose zoneinfo may be thin

	_ "golang.org/x/crypto/x509roots/fallback" // TLS roots if the PLAY has none
)

// version is set by build.sh.
var version = "dev"

const (
	defaultDir     = "/userdata/bd-stagetimer"
	defaultUIDir   = "/srv/birddog-web-ui"
	defaultUIBase  = "http://127.0.0.1"
	defaultAPIPort = 8096
	resumeGuard    = "/run/bdstage-resume"
)

func main() {
	var (
		serveAddr   = flag.String("serve", "", "serve the Stagetimer tab's API on this address (e.g. :8096) and run the screen")
		dir         = flag.String("dir", defaultDir, "where config.json lives")
		patchUI     = flag.Bool("patch-ui", false, "add the Stagetimer tab to birdUI's AV Setup page")
		unpatchUI   = flag.Bool("unpatch-ui", false, "remove the tab, restoring the page")
		restoreUI   = flag.Bool("restore-ui", false, "restore the page from the backup taken before patching")
		uiDir       = flag.String("ui-dir", defaultUIDir, "the stock web UI's directory")
		uiBase      = flag.String("ui-base", defaultUIBase, "where the stock web UI answers, for session checks")
		apiPort     = flag.Int("api-port", defaultAPIPort, "port the tab calls; baked into the script at patch time")
		allowUnprot = flag.Bool("allow-unprotected", false, "allow control even when birdUI has no password set (see README)")
		displayMode = flag.String("display", "ppapp", "how HDMI is taken: ppapp (stop the PLAY's decoder) or none (off a PLAY)")
		sinkSpec    = flag.String("sink", "kms", "where frames go: kms (gst-launch to kmssink) or png:DIR (off a PLAY)")
		videoSink   = flag.String("gst-video-sink", "", "replace the kmssink element (testing off a PLAY, e.g. fakesink)")
		probe       = flag.Bool("probe", false, "connect to --room (key in $STAGETIMER_API_KEY) and print what arrives")
		room        = flag.String("room", "", "--probe: the room ID")
		apiBase     = flag.String("api-base", "", "--probe: the API address (default https://api.stagetimer.io)")
		seconds     = flag.Float64("seconds", 15, "--probe: how long to listen")
		demo        = flag.String("demo", "", "draw every sample screen as a PNG into this directory, then exit")
		size        = flag.String("size", "1920x1080", "--demo: frame size")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	log := NewLogger()
	u := UIPaths{Dir: *uiDir}

	switch {
	case *patchUI:
		err := ApplyPatch(u, *apiPort)
		if errors.Is(err, errAlreadyPatched) {
			fmt.Println("AV Setup page already has the Stagetimer tab")
			return
		}
		exitOn(err)
		fmt.Println("added the Stagetimer tab to", u.Template())
	case *unpatchUI:
		exitOn(RemovePatch(u))
		fmt.Println("removed the Stagetimer tab from", u.Template())
	case *restoreUI:
		exitOn(RestoreBackup(u))
		fmt.Println("restored", u.Template(), "from", u.Backup())
	case *demo != "":
		var w, h int
		if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w < 320 || h < 180 {
			exitOn(fmt.Errorf("--size wants WxH, at least 320x180"))
		}
		exitOn(runDemo(*demo, w, h))
	case *probe:
		os.Exit(runProbe(log, Credentials{RoomID: NormalizeRoomID(*room), APIKey: os.Getenv("STAGETIMER_API_KEY"), Base: *apiBase}, *seconds))
	case *serveAddr != "":
		os.Exit(runServe(log, serveOpts{
			addr: *serveAddr, dir: *dir, uiBase: *uiBase, allowUnprotected: *allowUnprot,
			display: *displayMode, sink: *sinkSpec, videoSink: *videoSink,
		}))
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bdstage:", err)
		os.Exit(1)
	}
}

// newHTTPClient is shared by the API calls and the WebSocket. No overall
// Timeout: the WebSocket library refuses a client with one, and every call
// carries a context deadline instead.
func newHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 20 * time.Second
	return &http.Client{Transport: tr}
}

type serveOpts struct {
	addr, dir, uiBase        string
	allowUnprotected         bool
	display, sink, videoSink string
}

func runServe(log *Logger, o serveOpts) int {
	log.Info("bdstage %s starting (api %s)", version, o.addr)
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		log.Error("%s: %v", o.dir, err)
		return 1
	}
	store := &ConfigStore{Path: filepath.Join(o.dir, "config.json")}
	cfg, err := store.Load()
	if err != nil {
		log.Warn("config: %v — using defaults", err)
	}

	var display DisplayOwner
	switch o.display {
	case "ppapp":
		display = NewDisplay(log)
	case "none":
		display = &noDisplay{}
	default:
		log.Error("--display must be ppapp or none")
		return 2
	}

	caps := Caps{}
	var newSink func() FrameSink
	mode := DetectMode
	switch {
	case o.sink == "kms":
		caps.RawVideo = gstHasElement("rawvideoparse") || o.videoSink != ""
		var elem []string
		if o.videoSink != "" {
			elem = strings.Fields(o.videoSink)
		}
		newSink = func() FrameSink { return &GstSink{SinkElem: elem, Log: log} }
	case strings.HasPrefix(o.sink, "png:"):
		caps.RawVideo = true
		dir := strings.TrimPrefix(o.sink, "png:")
		newSink = func() FrameSink { return &PNGSink{Dir: dir, Keep: 600} } // ten minutes of a running countdown
		mode = func() OutputMode { return OutputMode{1920, 1080} }
	default:
		log.Error("--sink must be kms or png:DIR")
		return 2
	}
	log.Info("capabilities: rawvideoparse=%v", caps.RawVideo)

	hc := newHTTPClient()
	screen := NewScreen(ScreenDeps{
		Display: display, NewSink: newSink, Mode: mode, HTTP: hc, Log: log, Hint: settingsHint,
	})
	screen.SetOptions(cfg.Display)
	gate := NewGate(o.uiBase)
	gate.AllowUnprotected = o.allowUnprotected
	api := &APIServer{Store: store, Screen: screen, Gate: gate, Caps: caps, Version: version, Log: log, HTTP: hc}

	srv := &http.Server{Addr: o.addr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http: %v", err)
			os.Exit(1)
		}
	}()

	if cfg.Showing && cfg.Resume && cfg.Configured() && caps.RawVideo {
		go resume(log, screen, cfg, o.display == "ppapp")
	}

	// Shutting down must give HDMI back: a restart of bd-stagetimer that
	// skipped this would leave the unit dark. Showing stays as it is, so a
	// reboot or an upgrade comes back to the timer.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	log.Info("signal %v: shutting down", s)
	screen.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Info("stopped")
	return 0
}

// resume puts the timer back after a restart. It waits for the PLAY's decoder
// to be up first — stopping PPApp halfway through its own start is asking for
// the abort the handover tries hardest to avoid — and it refuses to run more
// than three times in ten minutes, so a crash loop cannot cycle PPApp every
// few seconds forever. bdomt's, unchanged.
func resume(log *Logger, screen *Screen, cfg Config, waitForPPApp bool) {
	if !resumeAllowed(resumeGuard, time.Now()) {
		log.Error("resume: tried three times in ten minutes; leaving HDMI to the PLAY's decoder. Press Show on the Stagetimer tab to try again")
		return
	}
	if waitForPPApp {
		// An update stops BirdDogRunner for its whole run and starts it again
		// at the end; taking the display in that window would have the
		// installer start PPApp on top of us. No PPApp in a minute means
		// someone else has the output, or meant it to be off: leave it.
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) && !processRunning("PPApp") {
			time.Sleep(time.Second)
		}
		if !processRunning("PPApp") {
			log.Warn("resume: the PLAY's own decoder did not start within a minute; not taking the display. Press Show on the Stagetimer tab when ready")
			return
		}
		time.Sleep(3 * time.Second)
	}
	log.Info("resume: the timer was on HDMI before the restart")
	if err := screen.Start(cfg.Credentials(), cfg.Display); err != nil {
		log.Error("resume: %v", err)
	}
}

// resumeAllowed counts resumes in a tmpfs file: "<count> <first unix time>".
func resumeAllowed(path string, now time.Time) bool {
	count, first := 0, now
	if b, err := os.ReadFile(path); err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 {
			c, _ := strconv.Atoi(f[0])
			t, _ := strconv.ParseInt(f[1], 10, 64)
			if now.Sub(time.Unix(t, 0)) < 10*time.Minute {
				count, first = c, time.Unix(t, 0)
			}
		}
	}
	if count >= 3 {
		return false
	}
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", count+1, first.Unix())), 0o644)
	return true
}

// runProbe connects to a room and prints what the feed sees: the check that
// a room ID and key work, from a laptop or over SSH on the PLAY.
func runProbe(log *Logger, c Credentials, seconds float64) int {
	if c.RoomID == "" || c.APIKey == "" {
		fmt.Fprintln(os.Stderr, "bdstage: --probe needs --room and the key in $STAGETIMER_API_KEY")
		return 2
	}
	hc := newHTTPClient()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds*float64(time.Second)))
	defer cancel()
	cl := NewClient(c, hc)
	// A refused key is reported and the socket tried anyway: the two halves
	// refuse in different words, and both are worth seeing.
	authErr := cl.TestAuth(ctx)
	if authErr != nil {
		fmt.Fprintln(os.Stderr, "bdstage: test_auth:", authErr)
	} else {
		fmt.Println("test_auth: ok")
	}
	f := NewFeed(c, hc, log)
	go f.Run(ctx)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s := f.Snapshot()
			loc := RoomLocation(s)
			tv := TimerAt(s, time.Now(), loc, true)
			out := map[string]any{
				"feed": s, "room": s.Room.Name, "timezone": s.Room.Timezone, "blackout": s.Room.Blackout,
				"on_air": s.Room.OnAir, "timer": tv.Timer.Name, "appearance": tv.Appearance,
				"running": tv.Running, "display": tv.Main, "phase": tv.Phase.String(), "timers": len(s.Timers),
			}
			if s.Message != nil {
				out["message"] = map[string]any{"showing": s.Message.Showing, "text": s.Message.Text, "focus": s.Message.Focus}
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			if authErr != nil || !s.HaveStatus {
				return 1
			}
			return 0
		case <-tick.C:
			s := f.Snapshot()
			tv := TimerAt(s, time.Now(), RoomLocation(s), true)
			fmt.Printf("%s  %-8s %-10s %s\n", time.Now().Format("15:04:05"), s.State, tv.Main, tv.Timer.Name)
		}
	}
}
