package main

// HDMI timings the PLAY can actually put out.
//
// The RK3328's HDMI PHY makes 20 pixel clocks and refuses every other one
// (its pre-PLL table in the PLAY's 4.4.194 kernel; Rockchip's driver has no
// fallback that calculates a PLL setting). A mode on another clock is pruned
// when it comes from an EDID, and handed over directly it leaves the VOP with
// a pixel clock of 0 — a dark screen, not an error. So every format here sits
// on one of those clocks, and a custom size is fitted onto one by padding the
// blanking until the refresh comes out right.
//
// shim/bdout.h carries the same list for the shim, which checks it again;
// TestClockListsAgree holds the two together.

import (
	"fmt"
	"math"
	"strings"
)

// ClocksKHz are the pixel clocks the PHY makes, ascending.
var ClocksKHz = []int{
	27000, 40000, 59341, 59400, 65000, 71000, 74176, 74250, 83500, 85750,
	88750, 108000, 119000, 148352, 148500, 162000, 296703, 297000, 593407, 594000,
}

const (
	maxWidth   = 4096 // the VOP's widest output
	maxHeight  = 4096
	hdmi14KHz  = 340000
	fitTol     = 0.0005  // a fitted refresh lands within 0.05% of the one asked for
	fitTight   = 0.00005 // and within 0.005% where the clock allows, so 59.94 reads as 59.94
	hGranule   = 8       // CVT's horizontal granularity
	minHBlank  = 80      // CVT reduced blanking v2
	minVBlankS = 460e-6  // CVT: at least 460 µs of vertical blanking
)

// Sync polarity flags, as DRM numbers them.
const (
	flagPHSync = 1 << 0
	flagNHSync = 1 << 1
	flagPVSync = 1 << 2
	flagNVSync = 1 << 3
)

// Mode is one HDMI timing, in DRM's terms.
type Mode struct {
	Name       string `json:"name"`
	ClockKHz   int    `json:"clock_khz"`
	HDisplay   int    `json:"hdisplay"`
	HSyncStart int    `json:"hsync_start"`
	HSyncEnd   int    `json:"hsync_end"`
	HTotal     int    `json:"htotal"`
	VDisplay   int    `json:"vdisplay"`
	VSyncStart int    `json:"vsync_start"`
	VSyncEnd   int    `json:"vsync_end"`
	VTotal     int    `json:"vtotal"`
	Flags      int    `json:"flags"`
}

// Refresh is the exact frame rate the timing gives.
func (m Mode) Refresh() float64 {
	return float64(m.ClockKHz) * 1000 / float64(m.HTotal*m.VTotal)
}

// ShimLine is the mode as shim.conf has it.
func (m Mode) ShimLine() string {
	return fmt.Sprintf("mode %s %d %d %d %d %d %d %d %d %d %d", m.Name, m.ClockKHz,
		m.HDisplay, m.HSyncStart, m.HSyncEnd, m.HTotal,
		m.VDisplay, m.VSyncStart, m.VSyncEnd, m.VTotal, m.Flags)
}

// Modeline is the X11 spelling, for anyone checking a timing elsewhere.
func (m Mode) Modeline() string {
	pol := func(p, n int, axis string) string {
		switch {
		case m.Flags&p != 0:
			return "+" + axis + "sync"
		case m.Flags&n != 0:
			return "-" + axis + "sync"
		}
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf(`Modeline "%s" %.3f %d %d %d %d %d %d %d %d %s %s`, m.Name,
		float64(m.ClockKHz)/1000, m.HDisplay, m.HSyncStart, m.HSyncEnd, m.HTotal,
		m.VDisplay, m.VSyncStart, m.VSyncEnd, m.VTotal,
		pol(flagPHSync, flagNHSync, "h"), pol(flagPVSync, flagNVSync, "v")))
}

func clockSupported(khz int) bool {
	for _, c := range ClocksKHz {
		if c == khz {
			return true
		}
	}
	return false
}

// Check is the shim's bdout_mode_check, so nothing reaches shim.conf that the
// shim would refuse.
func (m Mode) Check() error {
	switch {
	case !clockSupported(m.ClockKHz):
		return fmt.Errorf("pixel clock %d kHz is not one the PLAY's HDMI PHY can make", m.ClockKHz)
	case !(m.HDisplay > 0 && m.HDisplay < m.HSyncStart && m.HSyncStart < m.HSyncEnd && m.HSyncEnd <= m.HTotal):
		return fmt.Errorf("horizontal timings out of order")
	case !(m.VDisplay > 0 && m.VDisplay < m.VSyncStart && m.VSyncStart < m.VSyncEnd && m.VSyncEnd <= m.VTotal):
		return fmt.Errorf("vertical timings out of order")
	case m.HTotal > 0xffff || m.VTotal > 0xffff:
		return fmt.Errorf("totals over 65535")
	case m.HDisplay > maxWidth || m.VDisplay > maxHeight:
		return fmt.Errorf("%dx%d is bigger than the PLAY can drive", m.HDisplay, m.VDisplay)
	case m.HDisplay%2 != 0 || m.VDisplay%2 != 0:
		return fmt.Errorf("width and height must be even")
	case m.Flags&^(flagPHSync|flagNHSync|flagPVSync|flagNVSync) != 0:
		return fmt.Errorf("only sync polarity flags are allowed (no interlace)")
	case len(m.Name) == 0 || len(m.Name) > 31 || strings.ContainsAny(m.Name, " \t\n#"):
		return fmt.Errorf("mode name %q must be 1-31 characters with no spaces", m.Name)
	}
	if hz := math.Round(m.Refresh()); hz < 20 || hz > 120 {
		return fmt.Errorf("refresh %.0f Hz is outside 20-120", hz)
	}
	return nil
}

// Notes are the things worth saying about a timing before someone uses it.
func (m Mode) Notes() []string {
	var n []string
	if m.ClockKHz > hdmi14KHz {
		n = append(n, "over 340 MHz: needs a screen with HDMI 2.0")
	}
	return n
}

// Standard is a format with a name everyone knows.
type Standard struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group"`
	Mode  Mode   `json:"mode"`
	// Fitted formats have no standard timing on a clock the PHY makes, so
	// they come from Fit, as a custom size would.
	Fitted bool `json:"fitted,omitempty"`
}

func cea(id string, clock, h, hss, hse, ht, v, vss, vse, vt int) Standard {
	return ceaSync(id, clock, h, hss, hse, ht, v, vss, vse, vt, flagPHSync|flagPVSync)
}

func ceaSync(id string, clock, h, hss, hse, ht, v, vss, vse, vt, flags int) Standard {
	return Standard{ID: id, Label: id, Group: "TV and video (CEA-861)", Mode: Mode{
		Name: fmt.Sprintf("%dx%d", h, v), ClockKHz: clock,
		HDisplay: h, HSyncStart: hss, HSyncEnd: hse, HTotal: ht,
		VDisplay: v, VSyncStart: vss, VSyncEnd: vse, VTotal: vt, Flags: flags,
	}}
}

func dmt(label string, clock, h, hss, hse, ht, v, vss, vse, vt, flags int) Standard {
	return Standard{ID: strings.ReplaceAll(label, " ", "-"), Label: label, Group: "Computer (VESA)", Mode: Mode{
		Name: fmt.Sprintf("%dx%d", h, v), ClockKHz: clock,
		HDisplay: h, HSyncStart: hss, HSyncEnd: hse, HTotal: ht,
		VDisplay: v, VSyncStart: vss, VSyncEnd: vse, VTotal: vt, Flags: flags,
	}}
}

func fitted(w, h int, hz float64) Standard {
	m, err := Fit(w, h, hz)
	if err != nil {
		panic(fmt.Sprintf("fitted %dx%d@%g: %v", w, h, hz, err))
	}
	id := fmt.Sprintf("%dx%d@%g", w, h, hz)
	return Standard{ID: "fit-" + id, Label: id, Group: "Fitted to the PLAY's clocks", Mode: m, Fitted: true}
}

// Standards are the formats the tab offers by name. The CEA timings are the
// ones in CEA-861 and the VESA ones DMT's; only those whose clock the PHY makes
// are here (640x480's 25.175 MHz, for one, is not).
var Standards = buildStandards()

func buildStandards() []Standard {
	s := []Standard{
		ceaSync("480p59.94", 27000, 720, 736, 798, 858, 480, 489, 495, 525, flagNHSync|flagNVSync),
		ceaSync("576p50", 27000, 720, 732, 796, 864, 576, 581, 586, 625, flagNHSync|flagNVSync),
		cea("720p23.98", 59341, 1280, 3040, 3080, 3300, 720, 725, 730, 750),
		cea("720p24", 59400, 1280, 3040, 3080, 3300, 720, 725, 730, 750),
		cea("720p25", 74250, 1280, 3700, 3740, 3960, 720, 725, 730, 750),
		cea("720p29.97", 74176, 1280, 3040, 3080, 3300, 720, 725, 730, 750),
		cea("720p30", 74250, 1280, 3040, 3080, 3300, 720, 725, 730, 750),
		cea("720p50", 74250, 1280, 1720, 1760, 1980, 720, 725, 730, 750),
		cea("720p59.94", 74176, 1280, 1390, 1430, 1650, 720, 725, 730, 750),
		cea("720p60", 74250, 1280, 1390, 1430, 1650, 720, 725, 730, 750),
		cea("1080p23.98", 74176, 1920, 2558, 2602, 2750, 1080, 1084, 1089, 1125),
		cea("1080p24", 74250, 1920, 2558, 2602, 2750, 1080, 1084, 1089, 1125),
		cea("1080p25", 74250, 1920, 2448, 2492, 2640, 1080, 1084, 1089, 1125),
		cea("1080p29.97", 74176, 1920, 2008, 2052, 2200, 1080, 1084, 1089, 1125),
		cea("1080p30", 74250, 1920, 2008, 2052, 2200, 1080, 1084, 1089, 1125),
		cea("1080p50", 148500, 1920, 2448, 2492, 2640, 1080, 1084, 1089, 1125),
		cea("1080p59.94", 148352, 1920, 2008, 2052, 2200, 1080, 1084, 1089, 1125),
		cea("1080p60", 148500, 1920, 2008, 2052, 2200, 1080, 1084, 1089, 1125),
		cea("2160p23.98", 296703, 3840, 5116, 5204, 5500, 2160, 2168, 2178, 2250),
		cea("2160p24", 297000, 3840, 5116, 5204, 5500, 2160, 2168, 2178, 2250),
		cea("2160p25", 297000, 3840, 4896, 4984, 5280, 2160, 2168, 2178, 2250),
		cea("2160p29.97", 296703, 3840, 4016, 4104, 4400, 2160, 2168, 2178, 2250),
		cea("2160p30", 297000, 3840, 4016, 4104, 4400, 2160, 2168, 2178, 2250),
		cea("2160p50", 594000, 3840, 4896, 4984, 5280, 2160, 2168, 2178, 2250),
		cea("2160p59.94", 593407, 3840, 4016, 4104, 4400, 2160, 2168, 2178, 2250),
		cea("2160p60", 594000, 3840, 4016, 4104, 4400, 2160, 2168, 2178, 2250),
		cea("4096x2160p24", 297000, 4096, 5116, 5204, 5500, 2160, 2168, 2178, 2250),
		cea("4096x2160p25", 297000, 4096, 5064, 5152, 5280, 2160, 2168, 2178, 2250),
		cea("4096x2160p30", 297000, 4096, 4184, 4272, 4400, 2160, 2168, 2178, 2250),
		cea("4096x2160p50", 594000, 4096, 5064, 5152, 5280, 2160, 2168, 2178, 2250),
		cea("4096x2160p60", 594000, 4096, 4184, 4272, 4400, 2160, 2168, 2178, 2250),

		dmt("800x600 60Hz", 40000, 800, 840, 968, 1056, 600, 601, 605, 628, flagPHSync|flagPVSync),
		dmt("1024x768 60Hz", 65000, 1024, 1048, 1184, 1344, 768, 771, 777, 806, flagNHSync|flagNVSync),
		dmt("1280x800 60Hz", 83500, 1280, 1352, 1480, 1680, 800, 803, 809, 831, flagNHSync|flagPVSync),
		dmt("1280x800 60Hz RB", 71000, 1280, 1328, 1360, 1440, 800, 803, 809, 823, flagPHSync|flagNVSync),
		dmt("1280x960 60Hz", 108000, 1280, 1376, 1488, 1800, 960, 961, 964, 1000, flagPHSync|flagPVSync),
		dmt("1280x1024 60Hz", 108000, 1280, 1328, 1440, 1688, 1024, 1025, 1028, 1066, flagPHSync|flagPVSync),
		dmt("1440x900 60Hz RB", 88750, 1440, 1488, 1520, 1600, 900, 903, 909, 926, flagPHSync|flagNVSync),
		dmt("1600x1200 60Hz", 162000, 1600, 1664, 1856, 2160, 1200, 1201, 1204, 1250, flagPHSync|flagPVSync),
		dmt("1680x1050 60Hz RB", 119000, 1680, 1728, 1760, 1840, 1050, 1053, 1059, 1080, flagPHSync|flagNVSync),

		fitted(1366, 768, 60),
		fitted(1920, 1200, 60),
		fitted(1920, 1200, 50),
		fitted(2560, 1080, 60),
		fitted(3840, 1080, 60),
		fitted(3840, 1080, 50),
		fitted(1080, 1920, 60),
	}
	return s
}

// StandardByID looks a format up by its ID.
func StandardByID(id string) (Standard, bool) {
	for _, s := range Standards {
		if s.ID == id {
			return s, true
		}
	}
	return Standard{}, false
}

// Fit works out a timing for w×h at hz on one of the PHY's clocks.
//
// It starts from CVT reduced blanking (the least blanking a screen expects:
// 80 pixels a line, 460 µs a frame), takes the smallest clock that covers it,
// and spreads the extra blanking that clock forces over both axes in
// proportion, so neither ends up absurd. Line lengths stay on CVT's 8-pixel
// granule. The refresh lands within 0.05% of the one asked for, and within
// 0.005% wherever that clock has totals that get there.
func Fit(w, h int, hz float64) (Mode, error) {
	switch {
	case w < 320 || h < 200:
		return Mode{}, fmt.Errorf("%dx%d is smaller than 320x200", w, h)
	case w > maxWidth || h > maxHeight:
		return Mode{}, fmt.Errorf("%dx%d: the PLAY drives at most %d pixels either way", w, h, maxWidth)
	case w%2 != 0 || h%2 != 0:
		return Mode{}, fmt.Errorf("%dx%d: width and height must be even", w, h)
	case hz < 23 || hz > 120 || math.IsNaN(hz):
		return Mode{}, fmt.Errorf("%g Hz: choose 23 to 120", hz)
	}

	vMin := h + 15
	for vMin < 0xffff && float64(vMin-h) < minVBlankS*hz*float64(vMin) {
		vMin++
	}
	hMin := roundUp(w+minHBlank, hGranule)

	for _, clock := range ClocksKHz {
		area := float64(clock) * 1000 / hz // pixels per frame
		r := area / float64(hMin*vMin)
		if r < 1 {
			continue
		}
		vTarget := float64(vMin) * math.Sqrt(r)
		var best *cand
		for vt := vMin; vt <= 0xffff && float64(vt) <= vTarget*1.5+16; vt++ {
			ideal := area / float64(vt)
			for _, ht := range []int{roundDown(int(ideal), hGranule), roundUp(int(math.Ceil(ideal)), hGranule)} {
				if ht < hMin || ht > 0xffff {
					continue
				}
				got := float64(clock) * 1000 / float64(ht*vt)
				e := math.Abs(got-hz) / hz
				if e > fitTol {
					continue
				}
				c := cand{ht, vt, e, math.Abs(float64(vt) - vTarget)}
				if best == nil || c.better(*best) {
					best = &c
				}
			}
		}
		if best == nil {
			continue
		}
		hfp := evenClamp((best.ht-w-32)/3, 8, 48)
		m := Mode{
			Name: fmt.Sprintf("%dx%d", w, h), ClockKHz: clock,
			HDisplay: w, HSyncStart: w + hfp, HSyncEnd: w + hfp + 32, HTotal: best.ht,
			VDisplay: h, VSyncStart: h + 3, VSyncEnd: h + 3 + 6, VTotal: best.vt,
			Flags: flagPHSync | flagNVSync, // CVT reduced blanking's polarity
		}
		if err := m.Check(); err != nil {
			return Mode{}, err
		}
		return m, nil
	}
	return Mode{}, fmt.Errorf("no clock the PLAY makes gives %dx%d at %g Hz to within 0.05%%", w, h, hz)
}

// cand is a pair of totals Fit could use.
type cand struct {
	ht, vt int
	err    float64 // how far the refresh is from the one asked for, as a fraction
	dist   float64 // how far the frame is from blanking spread evenly
}

// better prefers a refresh within fitTight, then blanking spread evenly, then
// the nearer refresh.
func (c cand) better(o cand) bool {
	if ct, ot := c.err <= fitTight, o.err <= fitTight; ct != ot {
		return ct
	}
	if c.dist != o.dist {
		return c.dist < o.dist
	}
	return c.err < o.err
}

func roundUp(v, g int) int   { return (v + g - 1) / g * g }
func roundDown(v, g int) int { return v / g * g }

func evenClamp(v, lo, hi int) int {
	v &^= 1
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ParseSize reads "1920x1200@60" (the @ part optional, defaulting to def).
func ParseSize(s string, def float64) (w, h int, hz float64, err error) {
	hz = def
	size, rate, hasRate := strings.Cut(strings.TrimSpace(s), "@")
	if _, err = fmt.Sscanf(size, "%dx%d", &w, &h); err != nil {
		return 0, 0, 0, fmt.Errorf("%q: expected WIDTHxHEIGHT[@HZ]", s)
	}
	if hasRate {
		if _, err = fmt.Sscanf(rate, "%g", &hz); err != nil {
			return 0, 0, 0, fmt.Errorf("%q: the refresh after @ is not a number", s)
		}
	}
	return w, h, hz, nil
}
