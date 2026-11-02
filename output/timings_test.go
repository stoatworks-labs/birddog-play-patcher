package main

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The shim checks every mode against its own copy of the clock list, so the
// two must be the same list.
func TestClockListsAgree(t *testing.T) {
	b, err := os.ReadFile("shim/bdout.h")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)#define BDOUT_CLOCKS_KHZ\s*\\?(.*?)\n\n`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no BDOUT_CLOCKS_KHZ in shim/bdout.h")
	}
	var c []int
	for _, f := range regexp.MustCompile(`\d+`).FindAllString(string(m[1]), -1) {
		v, _ := strconv.Atoi(f)
		c = append(c, v)
	}
	if len(c) != len(ClocksKHz) {
		t.Fatalf("bdout.h has %d clocks, timings.go %d", len(c), len(ClocksKHz))
	}
	for i := range c {
		if c[i] != ClocksKHz[i] {
			t.Fatalf("clock %d: bdout.h %d, timings.go %d", i, c[i], ClocksKHz[i])
		}
	}
}

func TestStandardsAreValidAndRightlyNamed(t *testing.T) {
	ids := map[string]bool{}
	for _, s := range Standards {
		if ids[s.ID] {
			t.Errorf("%s: duplicate ID", s.ID)
		}
		ids[s.ID] = true
		if err := s.Mode.Check(); err != nil {
			t.Errorf("%s: %v", s.ID, err)
		}
		// The rate in the name is the rate the timing gives.
		want := 60.0
		if i := strings.IndexAny(s.ID, "p@"); i >= 0 {
			rest := s.ID[i+1:]
			if j := strings.IndexByte(rest, 'H'); j >= 0 {
				rest = rest[:j]
			}
			if v, err := strconv.ParseFloat(rest, 64); err == nil {
				want = v
			}
		}
		tol := 0.01
		if s.Group == "Computer (VESA)" || s.Fitted {
			tol = 0.006 // DMT's own rates are a little off theirs: 800x600 is 60.317
		}
		if got := s.Mode.Refresh(); math.Abs(got-want)/want > tol {
			t.Errorf("%s: refresh %.3f, want about %g", s.ID, got, want)
		}
	}
}

func TestFitGivesTimingsTheShimTakes(t *testing.T) {
	sizes := [][2]int{{640, 480}, {1024, 600}, {1280, 720}, {1366, 768}, {1920, 1080}, {1920, 1200},
		{2048, 1080}, {2560, 1080}, {2560, 1440}, {3840, 1080}, {1080, 1920}, {3840, 2160}, {4096, 2160}, {800, 1280}}
	rates := []float64{23.976, 24, 25, 29.97, 30, 50, 59.94, 60}
	fitted := 0
	for _, s := range sizes {
		for _, hz := range rates {
			m, err := Fit(s[0], s[1], hz)
			if err != nil {
				// Only allowed when even the biggest clock is too slow.
				if float64((s[0]+80)*(s[1]+15))*hz < 594e6 {
					t.Errorf("%dx%d@%g: %v", s[0], s[1], hz, err)
				}
				continue
			}
			fitted++
			if err := m.Check(); err != nil {
				t.Errorf("%dx%d@%g: %v", s[0], s[1], hz, err)
			}
			if got := m.Refresh(); math.Abs(got-hz)/hz > fitTol {
				t.Errorf("%dx%d@%g: refresh %.4f", s[0], s[1], hz, got)
			}
			if m.HTotal-m.HDisplay < minHBlank {
				t.Errorf("%dx%d@%g: horizontal blanking %d", s[0], s[1], hz, m.HTotal-m.HDisplay)
			}
			if float64(m.VTotal-m.VDisplay)/(hz*float64(m.VTotal)) < minVBlankS*0.999 {
				t.Errorf("%dx%d@%g: vertical blanking %d lines is under 460 µs", s[0], s[1], hz, m.VTotal-m.VDisplay)
			}
		}
	}
	if fitted < 90 {
		t.Fatalf("only %d of %d fitted", fitted, len(sizes)*len(rates))
	}
}

func TestFitRefuses(t *testing.T) {
	for _, c := range []struct {
		w, h int
		hz   float64
	}{{1921, 1080, 60}, {5000, 1080, 60}, {100, 100, 60}, {1920, 1080, 10}, {4096, 4096, 60}} {
		if _, err := Fit(c.w, c.h, c.hz); err == nil {
			t.Errorf("%dx%d@%g fitted", c.w, c.h, c.hz)
		}
	}
}

func TestModeCheckRefuses(t *testing.T) {
	ok, _ := StandardByID("1080p60")
	cases := map[string]func(*Mode){
		"clock":     func(m *Mode) { m.ClockKHz = 154000 },
		"order":     func(m *Mode) { m.HSyncStart = m.HDisplay },
		"odd":       func(m *Mode) { m.HDisplay = 1919 },
		"interlace": func(m *Mode) { m.Flags |= 1 << 4 },
		"space":     func(m *Mode) { m.Name = "1920 1080" },
		"comment":   func(m *Mode) { m.Name = "a#b" },
		"wide":      func(m *Mode) { m.HDisplay, m.HSyncStart, m.HSyncEnd, m.HTotal = 4100, 4110, 4120, 4130 },
		"slow":      func(m *Mode) { m.ClockKHz = 27000 },
	}
	for name, f := range cases {
		m := ok.Mode
		f(&m)
		if m.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ok.Mode.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestParseSize(t *testing.T) {
	w, h, hz, err := ParseSize("1920x1200@59.94", 60)
	if err != nil || w != 1920 || h != 1200 || hz != 59.94 {
		t.Fatal(w, h, hz, err)
	}
	if _, _, hz, _ := ParseSize("3840x1080", 50); hz != 50 {
		t.Fatal("default rate not used")
	}
	for _, bad := range []string{"1920", "x1080", "1920x1080@", "1920x1080@fast"} {
		if _, _, _, err := ParseSize(bad, 60); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestHz(t *testing.T) {
	for v, want := range map[float64]string{60: "60", 59.94005994: "59.94", 23.976: "23.976", 60.0129: "60.013"} {
		if got := hz(v); got != want {
			t.Errorf("hz(%v) = %s, want %s", v, got, want)
		}
	}
}
