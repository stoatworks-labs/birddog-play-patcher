// bdkvm — NDI KVM endpoint for the BirdDog PLAY.
//
// Reads the keyboard and mouse plugged into the PLAY's USB-A host port and
// forwards them to the NDI source the PLAY is currently displaying, as
// <ndi_kvm> metadata.
//
// It never touches PPApp. It opens its own NDI receiver at
// NDIlib_recv_bandwidth_metadata_only, which costs essentially nothing on the
// wire and coexists with PPApp's full-bandwidth receiver on the same source.
//
// Free NDI SDK only — NDIlib_recv_send_metadata is not an Advanced entry point,
// and the send path works regardless of what the sender advertises in
// <ndi_capabilities>.
//
// Installed at /userdata/birddog-kvm/, which BirdDog's own udev rule and
// BirdDogKVM.service already know how to start.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const sourceFile = "/etc/birddog-source1-name"

var (
	flagDryRun  = flag.Bool("dry-run", false, "log events instead of sending them (no NDI at all)")
	flagGrab    = flag.Bool("grab", true, "take input devices exclusively (EVIOCGRAB)")
	flagSens    = flag.Float64("sensitivity", 1.0, "mouse gain")
	flagRefW    = flag.Float64("ref-width", 1920, "virtual width the relative mouse moves across")
	flagRefH    = flag.Float64("ref-height", 1080, "virtual height")
	flagVerbose = flag.Bool("v", false, "verbose")
)

func logf(f string, a ...any) { log.Printf(f, a...) }
func vlogf(f string, a ...any) {
	if *flagVerbose {
		log.Printf(f, a...)
	}
}

// currentSource reads what the PLAY is displaying. The file holds either a bare
// NDI source name ("MACHINE (Source)"), or "SRT:name(uri)" / "None".
func currentSource() string {
	b, err := os.ReadFile(sourceFile)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if s == "" || s == "None" {
		return ""
	}
	// KVM only makes sense for NDI sources.
	for _, p := range []string{"SRT:", "CloudConnect:", "WebRTC:"} {
		if strings.HasPrefix(s, p) {
			return ""
		}
	}
	return s
}

type sender interface {
	Send(string)
}

type ndiSender struct {
	mu   sync.Mutex
	recv *Receiver
}

func (s *ndiSender) Send(xml string) {
	if xml == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recv == nil {
		return
	}
	if !s.recv.SendMetadata(xml) {
		vlogf("send failed: %s", xml)
	}
}

type dryRunSender struct{}

func (dryRunSender) Send(xml string) { fmt.Println(xml) }

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("bdkvm: ")

	devs := DiscoverDevices()
	if len(devs) == 0 {
		logf("no keyboard or mouse found under /dev/input/by-id — exiting")
		os.Exit(0)
	}
	logf("input devices: %v", devs)

	var snd sender
	var nd *NDI
	var recvHolder *ndiSender

	if *flagDryRun {
		snd = dryRunSender{}
		logf("dry-run: printing metadata instead of sending")
	} else {
		var err error
		nd, err = LoadNDI()
		if err != nil {
			logf("FATAL: %v", err)
			os.Exit(1)
		}
		defer nd.Close()
		recvHolder = &ndiSender{}
		snd = recvHolder
		go maintainReceiver(nd, recvHolder)
	}

	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; logf("shutting down"); close(stop) }()

	var wg sync.WaitGroup
	st := &pointerState{x: 0.5, y: 0.5}
	for _, d := range devs {
		dev, err := OpenDevice(d, *flagGrab)
		if err != nil {
			logf("cannot open %s: %v", d, err)
			continue
		}
		wg.Add(1)
		go func(dv *Device) {
			defer wg.Done()
			defer dv.Close()
			readLoop(dv, snd, st, stop)
		}(dev)
	}
	wg.Wait()
	logf("stopped")
}

// maintainReceiver keeps a metadata-only receiver attached to whatever source
// the PLAY is currently showing, and re-attaches when the operator changes it.
func maintainReceiver(nd *NDI, holder *ndiSender) {
	var attached string
	for {
		want := currentSource()
		if want != attached {
			holder.mu.Lock()
			if holder.recv != nil {
				holder.recv.Close()
				holder.recv = nil
			}
			holder.mu.Unlock()
			attached = want

			if want == "" {
				logf("no NDI source selected — KVM idle")
			} else {
				url, found := nd.FindSourceURL(want, 3000)
				if !found {
					vlogf("source %q not yet discovered; connecting by name", want)
				}
				r, err := nd.NewMetadataReceiver(want, url, "BirdDog PLAY KVM")
				if err != nil {
					logf("receiver for %q failed: %v", want, err)
					attached = "" // retry next tick
				} else {
					holder.mu.Lock()
					holder.recv = r
					holder.mu.Unlock()
					logf("KVM attached to %q (metadata-only)", want)
				}
			}
		}

		// A receiver that is never captured from will stall, so pump it.
		holder.mu.Lock()
		r := holder.recv
		holder.mu.Unlock()
		if r != nil {
			r.Pump(200)
		} else {
			time.Sleep(time.Second)
		}
	}
}

type pointerState struct {
	mu   sync.Mutex
	x, y float64
}

func readLoop(dev *Device, snd sender, st *pointerState, stop <-chan struct{}) {
	logf("reading %s", dev.Path)
	moved := false
	for {
		select {
		case <-stop:
			return
		default:
		}
		ev, err := dev.Read()
		if err != nil {
			logf("%s: read error (%v) — device probably unplugged", dev.Path, err)
			return
		}
		switch ev.Type {
		case evKey:
			switch ev.Code {
			case btnLeft, btnMiddle, btnRight:
				btn := map[uint16]int{btnLeft: 0, btnMiddle: 1, btnRight: 2}[ev.Code]
				if ev.Value == 2 {
					continue // no autorepeat for buttons
				}
				snd.Send(MouseButton(btn, ev.Value == 1))
			default:
				ks, ok := Keysym(ev.Code)
				if !ok {
					vlogf("unmapped keycode %d", ev.Code)
					continue
				}
				switch ev.Value {
				case 1:
					snd.Send(Key(ks, true))
				case 0:
					snd.Send(Key(ks, false))
				case 2: // autorepeat -> press again
					snd.Send(Key(ks, true))
				}
			}

		case evRel:
			switch ev.Code {
			case relX:
				st.mu.Lock()
				st.x = clampF(st.x+float64(ev.Value)**flagSens/(*flagRefW), 0, 1)
				st.mu.Unlock()
				moved = true
			case relY:
				st.mu.Lock()
				st.y = clampF(st.y+float64(ev.Value)**flagSens/(*flagRefH), 0, 1)
				st.mu.Unlock()
				moved = true
			case relWheel:
				snd.Send(Wheel(float32(ev.Value)))
			}

		case evSyn:
			// Coalesce X and Y into one position update per SYN_REPORT rather
			// than sending twice per movement.
			if moved {
				st.mu.Lock()
				x, y := st.x, st.y
				st.mu.Unlock()
				snd.Send(MousePos(float32(x), float32(y)))
				moved = false
			}
		}
	}
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
