package main

// bdout — custom HDMI formats and an area of interest for the BirdDog PLAY.
//
// The PLAY's own decoder (PPApp) picks its HDMI format from the screen's list
// to match the stream, and always shows the whole picture on the whole screen.
// libbdout.so, preloaded into PPApp by a drop-in on BirdDogRunner.service,
// changes both: it hands PPApp a one-mode list when a format is forced, and it
// rewrites PPApp's video plane to show part of the picture, placed anywhere on
// the screen (shim/). This program is the other half: the Output tab's API,
// which writes the shim's settings and restarts the decoder when a format
// changes — trying a new format first, and taking it back unless kept.
//
//	--serve :8095     the daemon: the Output tab's API
//	--write-conf      write shim.conf from config.json, then exit (the unit's
//	                  ExecStartPre, so the decoder never starts on a format
//	                  that was being tried when the power went)
//	--reset           back to the PLAY as shipped: follow the source, no AOI
//	--patch-ui        add the Output tab to birdUI's AV Setup page (and remove it)
//	--fit WxH[@HZ]    print the timing a size gets on the PLAY's clocks, then exit
//	--formats         list the standard formats, then exit

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"
)

// version is set by build.sh.
var version = "dev"

const (
	defaultDir        = "/userdata/bd-out"
	defaultShimStatus = "/run/bdout-shim.status"
	defaultUIDir      = "/srv/birddog-web-ui"
	defaultUIBase     = "http://127.0.0.1"
	defaultAPIPort    = 8095
)

func main() {
	var (
		serveAddr   = flag.String("serve", "", "serve the Output tab's API on this address (e.g. :8095)")
		dir         = flag.String("dir", defaultDir, "where config.json and shim.conf live")
		shimStatus  = flag.String("shim-status", defaultShimStatus, "the status file the shim writes")
		writeConf   = flag.Bool("write-conf", false, "write shim.conf from config.json and exit")
		reset       = flag.Bool("reset", false, "forget the format and the AOI (follow the source, whole picture) and exit")
		patchUI     = flag.Bool("patch-ui", false, "add the Output tab to birdUI's AV Setup page")
		unpatchUI   = flag.Bool("unpatch-ui", false, "remove the tab, restoring the page")
		restoreUI   = flag.Bool("restore-ui", false, "restore the page from the backup taken before patching")
		uiDir       = flag.String("ui-dir", defaultUIDir, "the stock web UI's directory")
		uiBase      = flag.String("ui-base", defaultUIBase, "where the stock web UI answers, for session checks")
		apiPort     = flag.Int("api-port", defaultAPIPort, "port the tab calls; baked into the script at patch time")
		allowUnprot = flag.Bool("allow-unprotected", false, "allow control even when birdUI has no password set (see README)")
		decoderMode = flag.String("decoder", "systemd", "how the PLAY's decoder is restarted: systemd, or none (off a PLAY)")
		sysRoot     = flag.String("sys", "", "read the screen from this directory's class/drm and kernel/debug/dri/0/summary instead of /sys (testing)")
		revertAfter = flag.Duration("revert-after", 30*time.Second, "how long a tried format waits to be kept")
		fit         = flag.String("fit", "", "print the timing WIDTHxHEIGHT[@HZ] gets on the PLAY's clocks and exit")
		formats     = flag.Bool("formats", false, "list the standard formats and exit")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	u := UIPaths{Dir: *uiDir}
	store := &ConfigStore{Path: filepath.Join(*dir, "config.json")}
	shimPath := filepath.Join(*dir, "shim.conf")

	switch {
	case *writeConf:
		if _, err := WriteConf(store, shimPath); err != nil {
			// The file is written either way — following the source when the
			// config is unreadable — so this is a warning, not a failed start.
			fmt.Fprintln(os.Stderr, "bdout:", err)
		}
		return
	case *reset:
		exitOn(store.Save(DefaultConfig()))
		changed, err := WriteConf(store, shimPath)
		exitOn(err)
		fmt.Println("following the source, whole picture on the whole screen")
		if changed {
			fmt.Println("the decoder takes the format when it restarts: systemctl restart BirdDogRunner")
		}
		return
	case *patchUI:
		err := ApplyPatch(u, *apiPort)
		if errors.Is(err, errAlreadyPatched) {
			fmt.Println("AV Setup page already has the Output tab")
			return
		}
		exitOn(err)
		fmt.Println("added the Output tab to", u.Template())
		return
	case *unpatchUI:
		exitOn(RemovePatch(u))
		fmt.Println("removed the Output tab from", u.Template())
		return
	case *restoreUI:
		exitOn(RestoreBackup(u))
		fmt.Println("restored", u.Template(), "from", u.Backup())
		return
	case *fit != "":
		w, h, hz, err := ParseSize(*fit, 60)
		exitOn(err)
		m, err := Fit(w, h, hz)
		exitOn(err)
		fmt.Printf("%s\n%s\nrefresh %.4f Hz\n", m.Modeline(), m.ShimLine(), m.Refresh())
		for _, n := range m.Notes() {
			fmt.Println("note:", n)
		}
		return
	case *formats:
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tGROUP\tREFRESH\tMODELINE")
		for _, s := range Standards {
			fmt.Fprintf(tw, "%s\t%s\t%.3f\t%s\n", s.ID, s.Group, s.Mode.Refresh(), s.Mode.Modeline())
		}
		tw.Flush()
		return
	case *serveAddr != "":
		sys := defaultSys
		if *sysRoot != "" {
			sys = SysPaths{DRM: filepath.Join(*sysRoot, "class", "drm"), Summary: filepath.Join(*sysRoot, "kernel", "debug", "dri", "0", "summary")}
		}
		os.Exit(runServe(serveOpts{
			addr: *serveAddr, store: store, shimPath: shimPath, shimStatus: *shimStatus,
			uiBase: *uiBase, allowUnprotected: *allowUnprot, decoder: *decoderMode,
			sys: sys, revertAfter: *revertAfter,
		}))
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bdout:", err)
		os.Exit(1)
	}
}

type serveOpts struct {
	addr                         string
	store                        *ConfigStore
	shimPath, shimStatus, uiBase string
	allowUnprotected             bool
	decoder                      string
	sys                          SysPaths
	revertAfter                  time.Duration
}

func runServe(o serveOpts) int {
	log := NewLogger()
	log.Info("bdout %s starting (api %s)", version, o.addr)
	var dec Decoder
	switch o.decoder {
	case "systemd":
		dec = systemdDecoder{log: log}
	case "none":
		dec = noDecoder{}
	default:
		log.Error("--decoder must be systemd or none")
		return 2
	}
	if o.revertAfter < 5*time.Second {
		log.Error("--revert-after under 5s leaves no time to look at the screen")
		return 2
	}
	runner := &Runner{Store: o.store, ShimPath: o.shimPath, ShimStatus: o.shimStatus, Decoder: dec, RevertAfter: o.revertAfter, Log: log}
	runner.Boot()
	cfg, _ := o.store.Load()
	log.Info("format: %s; area of interest %s", cfg.Format.Label(), map[bool]string{true: "on", false: "off"}[cfg.AOI.Active()])

	gate := NewGate(o.uiBase)
	gate.AllowUnprotected = o.allowUnprotected
	api := &APIServer{
		Store: o.store, Runner: runner, Decoder: dec, Gate: gate, Sys: o.sys,
		ShimStatus: o.shimStatus, Version: version, Log: log,
	}
	srv := &http.Server{Addr: o.addr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http: %v", err)
			os.Exit(1)
		}
	}()

	// A format being tried is not kept by stopping bd-out: the next start's
	// ExecStartPre and Boot put the kept one back.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	log.Info("signal %v: shutting down", s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Info("stopped")
	return 0
}
