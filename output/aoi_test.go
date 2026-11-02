package main

import (
	"fmt"
	"testing"
)

// The tab's preview is the shim's answer: the same AOIs through Preview and
// through core.c give the same pixels, or both refuse.
func TestPreviewMatchesTheShim(t *testing.T) {
	areas := []AOI{
		{On: true, Src: Rect{500000, 0, 500000, Unit}, Dst: wholeRect},
		{On: true, Src: Rect{0, 0, 333333, Unit}, Dst: wholeRect},
		{On: true, Src: Rect{333333, 0, 333333, Unit}, Dst: wholeRect, KeepAspect: true},
		{On: true, Src: Rect{250000, 250000, 500000, 500000}, Dst: Rect{100000, 100000, 400000, 400000}},
		{On: true, Src: wholeRect, Dst: wholeRect, KeepAspect: true},
		{On: true, Src: Rect{0, 0, 10000, 10000}, Dst: wholeRect},
		{On: true, Src: wholeRect, Dst: Rect{999990, 0, 10, Unit}},
		{On: true, Src: Rect{123457, 654321, 333333, 222222}, Dst: Rect{0, 500000, Unit, 500000}, KeepAspect: true},
	}
	sizes := [][4]int{{1920, 1080, 1920, 1080}, {3840, 1080, 1920, 1200}, {3840, 2160, 1920, 1080}, {1280, 720, 3840, 2160}, {1080, 1920, 1920, 1080}}
	for i, a := range areas {
		for _, s := range sizes {
			out, ok := runConfcheck(t, RenderShimConf(nil, a), fmt.Sprintf("%dx%d", s[0], s[1]), fmt.Sprintf("%dx%d", s[2], s[3]))
			p, err := Preview(a, s[0], s[1], s[2], s[3])
			if (err == nil) != ok {
				t.Errorf("area %d on %v: Go %v, shim %q", i, s, err, out)
				continue
			}
			if !ok {
				continue
			}
			want := fmt.Sprintf("plane %d %d %d %d %d %d %d %d", p.SX, p.SY, p.SW, p.SH, p.DX, p.DY, p.DW, p.DH)
			if got := out[len(out)-len(want):]; got != want {
				t.Errorf("area %d on %v: shim %q, Go %q", i, s, out, want)
			}
		}
	}
}

// What the tab sends for a crop typed in pixels (toRect in output.js: each
// value round(px * 1e6 / canvas), the size then held to what is left of the
// picture) comes back as those pixels, for every even position and size.
func TestPixelsSurviveTheRoundTrip(t *testing.T) {
	m := func(px, of int) int { return int((int64(px)*Unit + int64(of)/2) / int64(of)) }
	for _, canvas := range []int{1280, 1920, 2560, 3840, 4096, 5760, 7680} {
		for x := 0; x < canvas; x += 2 * 7 {
			for _, w := range []int{canvas - x, (canvas - x) / 2 &^ 1, 16} {
				if w < 16 || x+w > canvas {
					continue
				}
				mx := m(x, canvas)
				mw := min(m(w, canvas), Unit-mx)
				if part(canvas, mx) != x || part(canvas, mw) != w {
					t.Fatalf("canvas %d: %d+%d came back %d+%d", canvas, x, w, part(canvas, mx), part(canvas, mw))
				}
			}
		}
	}
}

func TestPreviewCases(t *testing.T) {
	half := AOI{On: true, Src: Rect{500000, 0, 500000, Unit}, Dst: wholeRect}
	p, err := Preview(half, 3840, 1080, 1920, 1080)
	if err != nil || p != (Plane{1920, 0, 1920, 1080, 0, 0, 1920, 1080}) {
		t.Fatal(p, err)
	}
	// 16:9 on 16:10, aspect kept: bars of 60 above and below.
	p, _ = Preview(AOI{On: true, Src: wholeRect, Dst: wholeRect, KeepAspect: true}, 1920, 1080, 1920, 1200)
	if p != (Plane{0, 0, 1920, 1080, 0, 60, 1920, 1080}) {
		t.Fatal(p)
	}
	if _, err := Preview(AOI{On: true, Src: Rect{0, 0, 10000, 10000}, Dst: wholeRect}, 3840, 2160, 3840, 2160); err == nil {
		t.Fatal("past 8:1 previewed")
	}
}
