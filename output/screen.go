package main

// What the screen on the HDMI port is, and what the PLAY is putting out.
//
// All read-only, from the kernel: the connector's status, modes and EDID in
// sysfs, and the mode in force from the Rockchip VOP's debugfs summary (which
// needs root, as bd-out runs). The modes in sysfs are the kernel's list after
// the PHY has pruned it — the screen's own list, less every mode on a clock the
// PLAY cannot make — and that is the list PPApp would choose from without the
// shim.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SysPaths struct {
	DRM     string // /sys/class/drm
	Summary string // /sys/kernel/debug/dri/0/summary
}

var defaultSys = SysPaths{DRM: "/sys/class/drm", Summary: "/sys/kernel/debug/dri/0/summary"}

type Screen struct {
	Connector string `json:"connector,omitempty"`
	Connected bool   `json:"connected"`
	Name      string `json:"name,omitempty"`
	// Native is the screen's preferred timing from its EDID, and NativeOK
	// whether the PLAY can make its clock.
	Native   *Mode `json:"native,omitempty"`
	NativeOK bool  `json:"native_ok"`
	// Modes are the sizes the kernel offers PPApp for this screen.
	Modes []string `json:"modes,omitempty"`
	// Current is the mode the VOP is putting out, as its summary says it.
	Current string `json:"current,omitempty"`
}

func ReadScreen(p SysPaths) Screen {
	var s Screen
	conns, _ := filepath.Glob(filepath.Join(p.DRM, "card*-HDMI-A-*"))
	sort.Strings(conns)
	if len(conns) > 0 {
		c := conns[0]
		s.Connector = strings.TrimPrefix(filepath.Base(c), "card0-")
		st, _ := os.ReadFile(filepath.Join(c, "status"))
		s.Connected = strings.TrimSpace(string(st)) == "connected"
		if b, err := os.ReadFile(filepath.Join(c, "modes")); err == nil {
			seen := map[string]bool{}
			for _, m := range strings.Fields(string(b)) {
				if !seen[m] {
					seen[m] = true
					s.Modes = append(s.Modes, m)
				}
			}
		}
		if b, err := os.ReadFile(filepath.Join(c, "edid")); err == nil && len(b) >= 128 {
			s.Name, s.Native = parseEDID(b)
			if s.Native != nil {
				s.NativeOK = s.Native.Check() == nil
			}
		}
	}
	if b, err := os.ReadFile(p.Summary); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(line, "Display mode:"); ok {
				s.Current = strings.TrimSpace(v)
				break
			}
		}
	}
	return s
}

// parseEDID gives the monitor name and the first detailed timing, which EDID
// 1.3 and later make the preferred one.
func parseEDID(b []byte) (string, *Mode) {
	if len(b) < 128 || string(b[:8]) != "\x00\xff\xff\xff\xff\xff\xff\x00" {
		return "", nil
	}
	var name string
	var native *Mode
	for i := 0; i < 4; i++ {
		d := b[54+18*i : 54+18*(i+1)]
		clock := int(d[0]) | int(d[1])<<8 // 10 kHz units
		if clock != 0 {
			if native == nil {
				native = dtdMode(d, clock*10)
			}
			continue
		}
		if d[3] == 0xfc { // monitor name
			n := string(d[5:18])
			if j := strings.IndexByte(n, '\n'); j >= 0 {
				n = n[:j]
			}
			name = strings.TrimSpace(n)
		}
	}
	return name, native
}

func dtdMode(d []byte, clockKHz int) *Mode {
	ha := int(d[2]) | int(d[4]>>4)<<8
	hb := int(d[3]) | int(d[4]&0xf)<<8
	va := int(d[5]) | int(d[7]>>4)<<8
	vb := int(d[6]) | int(d[7]&0xf)<<8
	hso := int(d[8]) | int(d[11]>>6)<<8
	hsw := int(d[9]) | int((d[11]>>4)&3)<<8
	vso := int(d[10]>>4) | int((d[11]>>2)&3)<<4
	vsw := int(d[10]&0xf) | int(d[11]&3)<<4
	if ha == 0 || va == 0 || d[17]&0x80 != 0 { // nothing there, or interlaced
		return nil
	}
	m := &Mode{
		Name: fmt.Sprintf("%dx%d", ha, va), ClockKHz: clockKHz,
		HDisplay: ha, HSyncStart: ha + hso, HSyncEnd: ha + hso + hsw, HTotal: ha + hb,
		VDisplay: va, VSyncStart: va + vso, VSyncEnd: va + vso + vsw, VTotal: va + vb,
	}
	if d[17]&0x18 == 0x18 { // digital separate sync: bits 2 and 1 are the polarities
		if d[17]&0x02 != 0 {
			m.Flags |= flagPHSync
		} else {
			m.Flags |= flagNHSync
		}
		if d[17]&0x04 != 0 {
			m.Flags |= flagPVSync
		} else {
			m.Flags |= flagNVSync
		}
	}
	return m
}
