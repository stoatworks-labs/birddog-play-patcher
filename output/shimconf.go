package main

// shim.conf, written for libbdout.so, and the status file it writes back.
//
// The format is the shim's (shim/core.c): a version line, at most one mode and
// one aoi line, and anything it does not expect fails the whole file into
// pass-through. Nothing here writes a mode the shim would refuse — Mode.Check
// is the shim's own check — and TestShimReadsWhatWeWrite feeds every file this
// writes to core.c, built for the host, to make sure.

import (
	"fmt"
	"os"
	"strings"
)

// RenderShimConf is shim.conf for a format (nil: follow the source) and an AOI.
func RenderShimConf(mode *Mode, aoi AOI) string {
	var b strings.Builder
	b.WriteString("# Written by bdout from the Output tab; edits here are overwritten.\n")
	b.WriteString("version 1\n")
	if mode != nil {
		fmt.Fprintf(&b, "# %s\n%s\n", mode.Modeline(), mode.ShimLine())
	}
	if aoi.Active() {
		fit := 0
		if aoi.KeepAspect {
			fit = 1
		}
		fmt.Fprintf(&b, "aoi %d %d %d %d %d %d %d %d %d\n",
			aoi.Src.X, aoi.Src.Y, aoi.Src.W, aoi.Src.H, aoi.Dst.X, aoi.Dst.Y, aoi.Dst.W, aoi.Dst.H, fit)
	}
	return b.String()
}

// ShimModeLine is the mode line in a shim.conf, or "" when it forces none. Two
// files with the same mode line need no restart of the PLAY's decoder between
// them: the AOI the shim picks up on its own.
func ShimModeLine(conf string) string {
	for _, l := range strings.Split(conf, "\n") {
		if strings.HasPrefix(l, "mode ") {
			return l
		}
	}
	return ""
}

// ShimStatus is what the shim says in /run/bdout-shim.status.
type ShimStatus struct {
	Present bool   `json:"present"`
	PID     int    `json:"pid,omitempty"`
	Conf    string `json:"conf,omitempty"`
	Mode    string `json:"mode,omitempty"`
	AOI     string `json:"aoi,omitempty"`
	AOIGeom string `json:"aoi_geometry,omitempty"`
	// Picture is "<picture> on <screen>": what PPApp showed last.
	Picture string `json:"picture,omitempty"`
	Updated int64  `json:"updated,omitempty"`
	// Loaded is true when the status came from the PPApp running now: the
	// shim is in the PLAY's decoder. Filled in by the caller.
	Loaded bool `json:"loaded"`
}

// ReadShimStatus reads the key=value file. A missing file is not an error:
// PPApp has not made a DRM call since boot, or the shim is not loaded.
func ReadShimStatus(path string) ShimStatus {
	b, err := os.ReadFile(path)
	if err != nil {
		return ShimStatus{}
	}
	st := ShimStatus{Present: true}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch k {
		case "pid":
			fmt.Sscan(v, &st.PID)
		case "conf":
			st.Conf = v
		case "mode":
			st.Mode = v
		case "aoi":
			st.AOI = v
		case "aoi_geometry":
			st.AOIGeom = v
		case "picture":
			st.Picture = v
		case "updated":
			fmt.Sscan(v, &st.Updated)
		}
	}
	return st
}

// Matches reports whether what the shim last handed PPApp is mode (nil: the
// screen's own list). A shim that has not been asked yet, or is waiting for a
// screen, will hand over whatever shim.conf says when PPApp next asks, so it
// matches anything.
func (s ShimStatus) Matches(m *Mode) bool {
	var name string
	var hz, clock int
	switch {
	case strings.HasPrefix(s.Mode, "off"):
		return m == nil
	case strings.HasPrefix(s.Mode, "forced "):
		if _, err := fmt.Sscanf(s.Mode, "forced %s at %d Hz (%d kHz)", &name, &hz, &clock); err != nil {
			return false
		}
		return m != nil && name == m.Name && clock == m.ClockKHz && hz == int(m.Refresh()+0.5)
	}
	return true
}

// PictureSizes splits the status's picture into the picture and the screen.
func (s ShimStatus) PictureSizes() (pw, ph, sw, sh int, ok bool) {
	n, _ := fmt.Sscanf(s.Picture, "%dx%d on %dx%d", &pw, &ph, &sw, &sh)
	return pw, ph, sw, sh, n == 4
}
