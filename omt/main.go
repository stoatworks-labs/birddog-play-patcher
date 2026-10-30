package main

// bdomt — an OMT receiver for the BirdDog PLAY.
//
// Puts an Open Media Transport source on the PLAY's HDMI output, with its
// sound, the way the PLAY's own decoder shows NDI. OMT is received natively
// (protocol.go, receiver.go), its VMX video is decoded by a patched libvmx
// straight to NV12 (vmx.go), and the frames reach the panel through kmssink
// (sink.go) after the PLAY's decoder has handed the display over (display.go).
//
//	--serve :8094     the daemon: the OMT tab's API, and the session it drives
//	--patch-ui        add the OMT tab to birdUI's AV Setup page (and remove it)
//	--discover        list the OMT sources this box can see, then exit
//	--receive ADDR    receive and decode ADDR into files, then exit — the bench
//	                  harness for checking decode off a PLAY

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

// version is set by build.sh.
var version = "dev"

const (
	defaultDir     = "/userdata/bd-omt"
	defaultUIDir   = "/srv/birddog-web-ui"
	defaultUIBase  = "http://127.0.0.1"
	defaultAPIPort = 8094
	resumeGuard    = "/run/bdomt-resume"
)

func main() {
	var (
		serveAddr   = flag.String("serve", "", "serve the OMT tab's API on this address (e.g. :8094) and run the receiver")
		dir         = flag.String("dir", defaultDir, "where config.json lives")
		vmxPath     = flag.String("vmx", "", "patched libvmx to load (default: beside this binary)")
		patchUI     = flag.Bool("patch-ui", false, "add the OMT tab to birdUI's AV Setup page")
		unpatchUI   = flag.Bool("unpatch-ui", false, "remove the tab, restoring the page")
		restoreUI   = flag.Bool("restore-ui", false, "restore the page from the backup taken before patching")
		uiDir       = flag.String("ui-dir", defaultUIDir, "the stock web UI's directory")
		uiBase      = flag.String("ui-base", defaultUIBase, "where the stock web UI answers, for session checks")
		apiPort     = flag.Int("api-port", defaultAPIPort, "port the tab calls; baked into the script at patch time")
		allowUnprot = flag.Bool("allow-unprotected", false, "allow control even when birdUI has no password set (see README)")
		displayMode = flag.String("display", "ppapp", "how HDMI is taken: ppapp (stop the PLAY's decoder) or none (off a PLAY)")
		videoSink   = flag.String("gst-video-sink", "", "replace the kmssink element (testing off a PLAY, e.g. fakesink)")
		audioSink   = flag.String("gst-audio-sink", "", "replace the alsasink element (testing off a PLAY, e.g. fakesink)")
		discover    = flag.Bool("discover", false, "list OMT sources on the network and exit")
		receive     = flag.String("receive", "", "receive this source (omt://host:port, host:port or a discovered name), decode it and exit")
		seconds     = flag.Float64("seconds", 10, "--receive: how long to receive")
		videoOut    = flag.String("video-out", "", "--receive: write decoded NV12 frames here")
		frames      = flag.Int("frames", 0, "--receive: write at most this many frames to --video-out (0 = all)")
		audioOut    = flag.String("audio-out", "", "--receive: write the converted S16LE stereo here")
		threads     = flag.Int("threads", 4, "--receive: libvmx decode threads")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	log := NewLogger()
	if *vmxPath == "" {
		*vmxPath = DefaultVMXPath()
	}
	u := UIPaths{Dir: *uiDir}

	switch {
	case *patchUI:
		err := ApplyPatch(u, *apiPort)
		if errors.Is(err, errAlreadyPatched) {
			fmt.Println("AV Setup page already has the OMT tab")
			return
		}
		exitOn(err)
		fmt.Println("added the OMT tab to", u.Template())
		return
	case *unpatchUI:
		exitOn(RemovePatch(u))
		fmt.Println("removed the OMT tab from", u.Template())
		return
	case *restoreUI:
		exitOn(RestoreBackup(u))
		fmt.Println("restored", u.Template(), "from", u.Backup())
		return
	case *discover:
		runDiscover(log)
		return
	case *receive != "":
		os.Exit(runReceive(log, *receive, *vmxPath, *seconds, *videoOut, *frames, *audioOut, *threads))
	case *serveAddr != "":
		os.Exit(runServe(log, serveOpts{
			addr: *serveAddr, dir: *dir, vmxPath: *vmxPath, uiBase: *uiBase,
			allowUnprotected: *allowUnprot, display: *displayMode,
			videoSink: *videoSink, audioSink: *audioSink,
		}))
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bdomt:", err)
		os.Exit(1)
	}
}

type serveOpts struct {
	addr, dir, vmxPath, uiBase string
	allowUnprotected           bool
	display                    string
	videoSink, audioSink       string
}

func runServe(log *Logger, o serveOpts) int {
	log.Info("bdomt %s starting (api %s)", version, o.addr)
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		log.Error("%s: %v", o.dir, err)
		return 1
	}
	store := &ConfigStore{Path: filepath.Join(o.dir, "config.json")}
	cfg, err := store.Load()
	if err != nil {
		log.Warn("config: %v — using defaults", err)
	}

	caps := Caps{RawVideo: gstHasElement("rawvideoparse"), Audio: gstHasElement("rawaudioparse")}
	if o.videoSink != "" {
		caps.RawVideo = true
	}
	if _, err := LoadVMX(o.vmxPath); err != nil {
		caps.VMXError = err.Error()
		log.Error("vmx: %v", err)
	} else {
		caps.VMX = true
	}
	log.Info("capabilities: rawvideoparse=%v rawaudioparse=%v vmx=%v", caps.RawVideo, caps.Audio, caps.VMX)
	if !caps.Audio {
		log.Warn("gst: rawaudioparse not available; OMT will be shown without sound")
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
	disc := NewDiscovery(log)
	gst := func(st Settings) GstOptions {
		return GstOptions{VideoSink: o.videoSink, AudioSink: o.audioSink, MatchOutput: st.MatchOutput, Log: log}
	}
	sess := NewSession(SessionDeps{
		Display:  display,
		NewVideo: func(st Settings) VideoSink { return NewGstVideo(gst(st)) },
		NewAudio: func(st Settings) AudioSink { return NewGstAudio(gst(st)) },
		VMX:      func() (*VMX, error) { return LoadVMX(o.vmxPath) },
		Resolve:  disc.Resolve,
		Mode:     DetectMode,
		AudioOK:  caps.Audio,
		Log:      log,
	})
	gate := NewGate(o.uiBase)
	gate.AllowUnprotected = o.allowUnprotected
	api := &APIServer{
		Store: store, Session: sess, Discovery: disc, Gate: gate,
		Sys: &sysSampler{}, Caps: caps, Version: version, Log: log,
	}

	srv := &http.Server{Addr: o.addr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http: %v", err)
			os.Exit(1)
		}
	}()

	if cfg.OnAir && cfg.Resume && !cfg.Source.IsZero() && caps.VMX && caps.RawVideo {
		go resume(log, sess, cfg, o.display == "ppapp")
	}

	// Shutting down must give HDMI back: a restart of bd-omt that skipped this
	// would leave the unit dark. OnAir stays as it is, so a reboot or an
	// upgrade comes back to the same source.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	log.Info("signal %v: shutting down", s)
	sess.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Info("stopped")
	return 0
}

// resume puts the saved source back after a restart. It waits for the PLAY's
// decoder to be up first — stopping PPApp halfway through its own start is
// asking for the abort the handover tries hardest to avoid — and it refuses to
// run more than three times in ten minutes, so a crash loop cannot cycle PPApp
// every few seconds forever.
func resume(log *Logger, sess *Session, cfg Config, waitForPPApp bool) {
	if !resumeAllowed(resumeGuard, time.Now()) {
		log.Error("resume: tried three times in ten minutes; leaving HDMI to the PLAY's decoder. Press Show on the OMT tab to try again")
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
			log.Warn("resume: the PLAY's own decoder did not start within a minute; not taking the display. Press Show on the OMT tab when ready")
			return
		}
		time.Sleep(3 * time.Second)
	}
	log.Info("resume: %s was on air before the restart", cfg.Source.Label())
	if err := sess.Start(cfg.Source, cfg.Settings()); err != nil {
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

func runDiscover(log *Logger) {
	d := NewDiscovery(log)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	found, err := d.Sources(ctx, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bdomt:", err)
	}
	if len(found) == 0 {
		fmt.Println("no OMT sources found")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tADDRESS")
	for _, f := range found {
		fmt.Fprintf(tw, "%s\t%s\n", f.Name, f.Address)
	}
	tw.Flush()
}

// runReceive is the harness: the daemon's receive and decode path, with files
// where the display and the speakers would be.
func runReceive(log *Logger, addr, vmxPath string, seconds float64, videoOut string, frames int, audioOut string, threads int) int {
	if _, err := LoadVMX(vmxPath); err != nil {
		fmt.Fprintln(os.Stderr, "bdomt:", err)
		return 1
	}
	src := Source{Address: addr}
	if _, _, err := parseAddress(addr); err != nil || strings.Contains(addr, "(") {
		src = Source{Name: addr}
	}
	fv := &fileVideo{limit: frames}
	fa := &fileAudio{}
	for _, f := range []struct {
		path string
		set  func(*os.File)
	}{
		{videoOut, func(w *os.File) { fv.out = w }},
		{audioOut, func(w *os.File) { fa.out = w }},
	} {
		if f.path == "" {
			continue
		}
		w, err := os.Create(f.path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bdomt:", err)
			return 1
		}
		defer w.Close()
		f.set(w)
	}
	disc := NewDiscovery(log)
	sess := NewSession(SessionDeps{
		Display:  &noDisplay{},
		NewVideo: func(Settings) VideoSink { return fv },
		NewAudio: func(Settings) AudioSink { return fa },
		VMX:      func() (*VMX, error) { return LoadVMX(vmxPath) },
		Resolve:  disc.Resolve,
		Mode:     func() OutputMode { return OutputMode{0, 0} },
		AudioOK:  true,
		Log:      log,
	})
	sys := &sysSampler{}
	sys.Read()
	if err := sess.Start(src, Settings{Quality: QualityDefault, Audio: true, Threads: threads, HoldOnLoss: true}); err != nil {
		fmt.Fprintln(os.Stderr, "bdomt:", err)
		return 1
	}
	time.Sleep(time.Duration(seconds * float64(time.Second)))
	st := sess.Status()
	sy := sys.Read()
	sess.Stop()
	out := map[string]any{
		"session":      st,
		"sys":          sy,
		"frames":       fv.frames,
		"audio_bytes":  fa.bytes,
		"frame_hashes": fv.sums,
	}
	if len(fv.sums) > 8 {
		out["frame_hashes"] = fv.sums[:8]
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	if fv.frames == 0 {
		return 1
	}
	return 0
}
