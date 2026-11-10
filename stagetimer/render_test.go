package main

import (
	"image"
	"image/color"
	"testing"
	"time"
)

func TestNV12Layout(t *testing.T) {
	for _, c := range []struct{ w, h, stride, uv, size int }{
		{1920, 1080, 1920, 1920 * 1080, 1920 * 1080 * 3 / 2},
		{1280, 720, 1280, 1280 * 720, 1280 * 720 * 3 / 2},
		{1366, 768, 1368, 1368 * 768, 1368 * 768 * 3 / 2},
		{5, 3, 8, 8 * 4, 8 * 4 * 3 / 2},
	} {
		s, uv, n := NV12Layout(c.w, c.h)
		if s != c.stride || uv != c.uv || n != c.size {
			t.Errorf("NV12Layout(%d,%d) = %d %d %d, want %d %d %d", c.w, c.h, s, uv, n, c.stride, c.uv, c.size)
		}
	}
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	return img
}

// BT.709 limited range: the values any reference converter gives, ±1.
func TestToNV12Colours(t *testing.T) {
	cases := []struct {
		name      string
		c         color.RGBA
		y, cb, cr int
	}{
		{"black", color.RGBA{0, 0, 0, 255}, 16, 128, 128},
		{"white", color.RGBA{255, 255, 255, 255}, 235, 128, 128},
		{"red", color.RGBA{255, 0, 0, 255}, 63, 102, 240},
		{"green", color.RGBA{0, 255, 0, 255}, 173, 42, 26},
		{"blue", color.RGBA{0, 0, 255, 255}, 32, 240, 118},
	}
	near := func(a, b int) bool { return a-b <= 1 && b-a <= 1 }
	for _, c := range cases {
		w, h := 6, 3 // odd height and a padded stride
		_, uvOff, size := NV12Layout(w, h)
		buf := make([]byte, size)
		ToNV12(solid(w, h, c.c), buf)
		if y := int(buf[0]); !near(y, c.y) {
			t.Errorf("%s: Y %d, want %d", c.name, y, c.y)
		}
		if y := int(buf[2*8+5]); !near(y, c.y) { // last real row, last pixel
			t.Errorf("%s: Y at the corner %d, want %d", c.name, y, c.y)
		}
		if cb, cr := int(buf[uvOff]), int(buf[uvOff+1]); !near(cb, c.cb) || !near(cr, c.cr) {
			t.Errorf("%s: CbCr %d %d, want %d %d", c.name, cb, cr, c.cb, c.cr)
		}
		// The odd last row's chroma (row pair 1) and the padding luma row.
		if cb := int(buf[uvOff+8]); !near(cb, c.cb) {
			t.Errorf("%s: Cb of the last row pair %d", c.name, cb)
		}
		if y := int(buf[3*8]); !near(y, c.y) {
			t.Errorf("%s: padding row Y %d", c.name, y)
		}
	}
}

func TestRenderSize(t *testing.T) {
	for _, c := range []struct {
		in   OutputMode
		w, h int
	}{
		{OutputMode{1920, 1080}, 1920, 1080},
		{OutputMode{1280, 720}, 1280, 720},
		{OutputMode{3840, 2160}, 1920, 1080},
		{OutputMode{4096, 2160}, 1920, 1012},
		{OutputMode{0, 0}, 1920, 1080},
		{OutputMode{1365, 767}, 1364, 766},
	} {
		if w, h := renderSize(c.in); w != c.w || h != c.h {
			t.Errorf("renderSize(%v) = %dx%d, want %dx%d", c.in, w, h, c.w, c.h)
		}
	}
}

// Every demo scene draws at both common rasters, and draws something.
func TestEveryScreenDraws(t *testing.T) {
	now := time.Now()
	for _, size := range [][2]int{{1920, 1080}, {1280, 720}, {720, 576}} {
		for _, sc := range demoScenes() {
			snap := demoSnapshot(now)
			if sc.edit != nil {
				sc.edit(&snap, now)
			}
			m := BuildModel(snap, now, sc.opts, "Settings: http://192.168.1.50/ › AV Setup › Stagetimer")
			img := NewRenderer(size[0], size[1]).Draw(m)
			if img.Rect.Dx() != size[0] || img.Rect.Dy() != size[1] {
				t.Fatalf("%s at %v: drew %v", sc.name, size, img.Rect)
			}
			if distinct(img) < 2 {
				t.Errorf("%s at %v: the frame is a single colour", sc.name, size)
			}
		}
	}
}

func distinct(img *image.RGBA) int {
	seen := map[[3]uint8]bool{}
	for i := 0; i < len(img.Pix) && len(seen) < 8; i += 4 * 97 {
		seen[[3]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}] = true
	}
	return len(seen)
}

func TestBlackoutIsBlack(t *testing.T) {
	now := time.Now()
	s := demoSnapshot(now)
	s.Room.Blackout = true
	s.Message = &Message{Showing: true, Text: "should not show"}
	m := BuildModel(s, now, DefaultViewOptions(), "")
	if !m.Blank {
		t.Fatal("blackout not blank")
	}
	img := NewRenderer(640, 360).Draw(m)
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 || img.Pix[i+1] != 0 || img.Pix[i+2] != 0 {
			t.Fatalf("pixel %d is %v, want black", i/4, img.Pix[i:i+3])
		}
	}
}

func TestFlashInverts(t *testing.T) {
	now := time.Now()
	s := demoSnapshot(now)
	s.FlashAt, s.FlashCount = now.Add(-200*time.Millisecond), 3
	on := BuildModel(s, now, DefaultViewOptions(), "")
	off := BuildModel(s, now.Add(400*time.Millisecond), DefaultViewOptions(), "")
	done := BuildModel(s, now.Add(3*time.Second), DefaultViewOptions(), "")
	if !on.Flash || off.Flash || done.Flash {
		t.Fatalf("flash phases: %v %v %v", on.Flash, off.Flash, done.Flash)
	}
	a := NewRenderer(320, 180).Draw(on)
	off.Main = on.Main // the same picture, half a second apart
	b := NewRenderer(320, 180).Draw(off)
	if a.Pix[0] != 255-b.Pix[0] || a.Pix[1] != 255-b.Pix[1] || a.Pix[2] != 255-b.Pix[2] {
		t.Errorf("flash corner %v vs %v: not inverted", a.Pix[0:3], b.Pix[0:3])
	}
}

func TestMirror(t *testing.T) {
	now := time.Now()
	s := demoSnapshot(now)
	o := DefaultViewOptions()
	plain := NewRenderer(320, 180).Draw(BuildModel(s, now, o, ""))
	o.Mirror = "h"
	flipped := NewRenderer(320, 180).Draw(BuildModel(s, now, o, ""))
	w := 320
	for _, y := range []int{10, 90, 170} {
		for x := 0; x < w; x += 7 {
			p := plain.RGBAAt(x, y)
			q := flipped.RGBAAt(w-1-x, y)
			if p != q {
				t.Fatalf("(%d,%d) %v vs mirrored %v", x, y, p, q)
			}
		}
	}
}

func TestModelKeyChangesOncePerSecond(t *testing.T) {
	// Short timers are the hard case: the bar has the most to move per second.
	for _, length := range []time.Duration{time.Minute, 5 * time.Minute, 25 * time.Minute} {
		start := time.Now().Truncate(time.Second)
		s := demoSnapshot(start)
		s.Status.Start = start.UnixMilli()
		s.Status.Finish = start.Add(length).UnixMilli()
		for _, o := range []ViewOptions{DefaultViewOptions(), {View: ViewSplit, ShowProgress: true}} {
			keys := map[string]bool{}
			for i := 0; i < 50; i++ { // five seconds in 100 ms steps, from 0.05 s in
				keys[BuildModel(s, start.Add(50*time.Millisecond+time.Duration(i)*100*time.Millisecond), o, "").Key()] = true
			}
			if len(keys) > 6 {
				t.Errorf("%v timer, %s view: %d distinct frames in five seconds, want at most 6", length, o.View, len(keys))
			}
		}
	}
}

func TestModelHonoursOptions(t *testing.T) {
	now := time.Now()
	s := demoSnapshot(now)
	s.Message = &Message{Showing: true, Text: "wrap up", Uppercase: true, Color: "Red"}
	o := DefaultViewOptions()
	m := BuildModel(s, now, o, "")
	if m.Title == "" || m.Speaker == "" || m.Clock == "" || !m.ShowProgress || m.Next != "" || m.Msg.Text != "WRAP UP" || m.Msg.Color != "red" {
		t.Errorf("defaults: %+v", m)
	}
	o = ViewOptions{View: ViewTimer, ShowNext: true}
	m = BuildModel(s, now, o, "")
	if m.Title != "" || m.Speaker != "" || m.Clock != "" || m.ShowProgress || m.Msg.Show || m.Next == "" {
		t.Errorf("all off: %+v", m)
	}
	o.View = "nonsense"
	if BuildModel(s, now, o, "").View != ViewTimer {
		t.Error("unknown view not mapped to timer")
	}
	o.View = ViewClock
	if m := BuildModel(s, now, o, ""); m.BigClock == "" || m.DateLine == "" {
		t.Error("clock view without a clock")
	}
	o.View = ViewAgenda
	if m := BuildModel(s, now, o, ""); len(m.Rows) != len(s.Timers) {
		t.Errorf("agenda rows %d, want %d", len(m.Rows), len(s.Timers))
	}
}
