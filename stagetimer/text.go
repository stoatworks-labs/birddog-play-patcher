package main

// Text on the frame: Noto Sans (SIL OFL 1.1, fonts/OFL.txt), rasterised by
// x/image and cached per glyph and size, because a countdown redraws the same
// half-dozen glyphs at 400 px every second and the PLAY's A53s should not be
// re-rasterising them each time.

import (
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed fonts/NotoSans-Regular.ttf
var fontRegularTTF []byte

//go:embed fonts/NotoSans-Bold.ttf
var fontBoldTTF []byte

// Typeface is one font at any pixel size.
type Typeface struct {
	f *opentype.Font

	mu     sync.Mutex
	faces  map[int]font.Face
	glyphs map[glyphKey]*glyph
}

type glyphKey struct {
	px int
	r  rune
}

type glyph struct {
	mask *image.Alpha // nil for blank glyphs
	off  image.Point  // top-left of mask relative to the pen on the baseline
	adv  fixed.Int26_6
}

func mustTypeface(ttf []byte) *Typeface {
	f, err := opentype.Parse(ttf)
	if err != nil {
		panic(err)
	}
	return &Typeface{f: f, faces: map[int]font.Face{}, glyphs: map[glyphKey]*glyph{}}
}

var (
	fontsOnce   sync.Once
	faceRegular *Typeface
	faceBold    *Typeface
)

func fonts() (*Typeface, *Typeface) {
	fontsOnce.Do(func() {
		faceRegular = mustTypeface(fontRegularTTF)
		faceBold = mustTypeface(fontBoldTTF)
	})
	return faceRegular, faceBold
}

func (t *Typeface) faceLocked(px int) font.Face {
	if px < 4 {
		px = 4
	}
	if f, ok := t.faces[px]; ok {
		return f
	}
	if len(t.faces) > 64 {
		t.faces = map[int]font.Face{}
		t.glyphs = map[glyphKey]*glyph{}
	}
	f, err := opentype.NewFace(t.f, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	t.faces[px] = f
	return f
}

func (t *Typeface) glyphLocked(px int, r rune) *glyph {
	k := glyphKey{px, r}
	if g, ok := t.glyphs[k]; ok {
		return g
	}
	if len(t.glyphs) > 4000 {
		t.glyphs = map[glyphKey]*glyph{}
	}
	face := t.faceLocked(px)
	g := &glyph{}
	dr, mask, mp, adv, ok := face.Glyph(fixed.Point26_6{}, r)
	if !ok {
		dr, mask, mp, adv, ok = face.Glyph(fixed.Point26_6{}, '?')
	}
	g.adv = adv
	if ok && !dr.Empty() && mask != nil {
		// The face reuses its buffer, so copy the mask out.
		a := image.NewAlpha(image.Rect(0, 0, dr.Dx(), dr.Dy()))
		draw.Draw(a, a.Bounds(), mask, mp, draw.Src)
		g.mask, g.off = a, dr.Min
	}
	t.glyphs[k] = g
	return g
}

// Metrics are the face's ascent and descent in pixels.
func (t *Typeface) Metrics(px int) (ascent, descent int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.faceLocked(px).Metrics()
	return m.Ascent.Ceil(), m.Descent.Ceil()
}

// CapHeight is the height of a capital or digit at px, for centring numbers.
func (t *Typeface) CapHeight(px int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.glyphLocked(px, '0')
	if g.mask == nil {
		return px * 7 / 10
	}
	return g.mask.Bounds().Dy()
}

// Width is the advance of s at px, kerning included.
func (t *Typeface) Width(px int, s string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	face := t.faceLocked(px)
	var w fixed.Int26_6
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			w += face.Kern(prev, r)
		}
		w += t.glyphLocked(px, r).adv
		prev = r
	}
	return w.Ceil()
}

// Draw puts s on dst with its baseline at y, starting at x, clipped to clip.
func (t *Typeface) Draw(dst *image.RGBA, clip image.Rectangle, px, x, y int, s string, c color.RGBA) {
	t.mu.Lock()
	defer t.mu.Unlock()
	face := t.faceLocked(px)
	src := image.NewUniform(c)
	pen := fixed.I(x)
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			pen += face.Kern(prev, r)
		}
		g := t.glyphLocked(px, r)
		t.blit(dst, clip, g, pen.Round(), y, src)
		pen += g.adv
		prev = r
	}
}

func (t *Typeface) blit(dst *image.RGBA, clip image.Rectangle, g *glyph, x, y int, src image.Image) {
	if g.mask == nil {
		return
	}
	r := g.mask.Bounds().Add(image.Pt(x, y).Add(g.off))
	rc := r.Intersect(clip)
	if rc.Empty() {
		return
	}
	draw.DrawMask(dst, rc, src, image.Point{}, g.mask, rc.Min.Sub(r.Min), draw.Over)
}

// DigitCell is the widest digit's advance: the cell every digit of a running
// clock is centred in, so the numbers do not shuffle sideways as they change.
func (t *Typeface) DigitCell(px int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	var w fixed.Int26_6
	for r := '0'; r <= '9'; r++ {
		if a := t.glyphLocked(px, r).adv; a > w {
			w = a
		}
	}
	return w.Ceil()
}

// TabularWidth is the width of s laid out with fixed digit cells.
func (t *Typeface) TabularWidth(px int, s string) int {
	cell := t.DigitCell(px)
	t.mu.Lock()
	defer t.mu.Unlock()
	w := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			w += cell
		} else {
			w += t.glyphLocked(px, r).adv.Ceil()
		}
	}
	return w
}

// DrawTabular draws s with fixed digit cells.
func (t *Typeface) DrawTabular(dst *image.RGBA, clip image.Rectangle, px, x, y int, s string, c color.RGBA) {
	cell := t.DigitCell(px)
	t.mu.Lock()
	defer t.mu.Unlock()
	src := image.NewUniform(c)
	for _, r := range s {
		g := t.glyphLocked(px, r)
		if r >= '0' && r <= '9' {
			gx := x + (cell-g.adv.Ceil())/2
			t.blit(dst, clip, g, gx, y, src)
			x += cell
		} else {
			t.blit(dst, clip, g, x, y, src)
			x += g.adv.Ceil()
		}
	}
}

// FitTabular is the largest px at which s (tabular) fits w × capHeight h.
func (t *Typeface) FitTabular(s string, w, h int) int {
	const probe = 200
	tw := t.TabularWidth(probe, s)
	ch := t.CapHeight(probe)
	px := probe
	if tw > 0 {
		px = probe * w / tw
	}
	if ch > 0 {
		if byH := probe * h / ch; byH < px {
			px = byH
		}
	}
	return max(px, 8)
}

// FitLine is the largest px, at most maxPx, at which s fits in w.
func (t *Typeface) FitLine(s string, w, maxPx int) int {
	const probe = 100
	tw := t.Width(probe, s)
	if tw <= 0 {
		return maxPx
	}
	return max(min(maxPx, probe*w/tw), 8)
}

// Ellipsize shortens s with "…" until it fits w at px.
func (t *Typeface) Ellipsize(px int, s string, w int) string {
	if t.Width(px, s) <= w {
		return s
	}
	rs := []rune(s)
	for len(rs) > 0 {
		rs = rs[:len(rs)-1]
		c := strings.TrimRightFunc(string(rs), unicode.IsSpace) + "…"
		if t.Width(px, c) <= w {
			return c
		}
	}
	return ""
}

// Wrap breaks s into lines no wider than w at px. A word too long for a line
// is broken where it must be.
func (t *Typeface) Wrap(px int, s string, w int) []string {
	var lines []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range words {
			try := word
			if line != "" {
				try = line + " " + word
			}
			if t.Width(px, try) <= w {
				line = try
				continue
			}
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			for t.Width(px, word) > w {
				rs := []rune(word)
				n := len(rs)
				for n > 1 && t.Width(px, string(rs[:n])) > w {
					n--
				}
				lines = append(lines, string(rs[:n]))
				word = string(rs[n:])
			}
			line = word
		}
		lines = append(lines, line)
	}
	return lines
}

// FitParagraph finds the largest px (at most maxPx) at which s wraps into a
// w × h box, and returns the lines and the line height.
func (t *Typeface) FitParagraph(s string, w, h, maxPx int) (int, []string, int) {
	for px := maxPx; px >= 12; px = px * 9 / 10 {
		lines := t.Wrap(px, s, w)
		lh := px * 6 / 5
		if len(lines)*lh <= h {
			return px, lines, lh
		}
	}
	px := 12
	lh := px * 6 / 5
	lines := t.Wrap(px, s, w)
	if n := h / lh; n > 0 && len(lines) > n {
		lines = lines[:n]
	}
	return px, lines, lh
}
