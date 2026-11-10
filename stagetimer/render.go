package main

// Drawing a Model into a frame.
//
// Everything is laid out on a 1080-line design grid and scaled to the output,
// so 720p and 1080p are the same picture. Black background, white type and
// Stagetimer's green / yellow / red for wrap-up, because a confidence monitor
// at the foot of a stage has to be read at a glance from ten metres, in a dark
// room, by someone who is mid-sentence.

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
	"strings"
)

var (
	colBG        = color.RGBA{0, 0, 0, 255}
	colFG        = color.RGBA{255, 255, 255, 255}
	colSecondary = color.RGBA{161, 161, 170, 255}
	colDim       = color.RGBA{96, 96, 106, 255}
	colPanel     = color.RGBA{26, 26, 31, 255}
	colTrack     = color.RGBA{44, 44, 50, 255}
	colGreen     = color.RGBA{34, 197, 94, 255}
	colYellow    = color.RGBA{250, 204, 21, 255}
	colRed       = color.RGBA{239, 68, 68, 255}
	colOnAir     = color.RGBA{220, 38, 38, 255}
	colAmber     = color.RGBA{245, 158, 11, 255}
	colMsgGreen  = color.RGBA{74, 222, 128, 255}
	colMsgRed    = color.RGBA{248, 113, 113, 255}
)

func phaseColor(p Phase) color.RGBA {
	switch p {
	case PhaseYellow:
		return colYellow
	case PhaseRed, PhaseOver:
		return colRed
	}
	return colGreen
}

// digitColor is the countdown's colour: white while there is plenty of time.
func digitColor(p Phase) color.RGBA {
	switch p {
	case PhaseYellow:
		return colYellow
	case PhaseRed, PhaseOver:
		return colRed
	}
	return colFG
}

func messageColor(c string) color.RGBA {
	switch c {
	case "green":
		return colMsgGreen
	case "red":
		return colMsgRed
	}
	return colFG
}

func dim(c color.RGBA, f float64) color.RGBA {
	return color.RGBA{uint8(float64(c.R) * f), uint8(float64(c.G) * f), uint8(float64(c.B) * f), 255}
}

// Renderer owns one frame buffer.
type Renderer struct {
	W, H int
	img  *image.RGBA
	reg  *Typeface
	bold *Typeface
	s    float64
}

func NewRenderer(w, h int) *Renderer {
	reg, bold := fonts()
	s := math.Min(float64(w)/1920, float64(h)/1080)
	return &Renderer{W: w, H: h, img: image.NewRGBA(image.Rect(0, 0, w, h)), reg: reg, bold: bold, s: s}
}

// px scales a design size.
func (r *Renderer) px(v float64) int { return int(math.Round(v * r.s)) }

// Draw renders m and returns the frame (owned by the renderer; valid until the
// next Draw).
func (r *Renderer) Draw(m Model) *image.RGBA {
	fill(r.img, r.img.Bounds(), colBG)
	full := r.img.Bounds()
	switch {
	case m.Blank:
		return r.img
	case m.Notice != "":
		r.drawNotice(m, full)
	default:
		switch m.View {
		case ViewAgenda:
			r.drawAgendaScreen(m, full)
		case ViewSplit:
			left := full
			left.Max.X = full.Min.X + full.Dx()*60/100
			right := full
			right.Min.X = left.Max.X
			r.drawTimer(m, left, false)
			fill(r.img, image.Rect(right.Min.X, full.Min.Y+r.px(60), right.Min.X+max(1, r.px(2)), full.Max.Y-r.px(60)), colTrack)
			r.drawAgenda(m, right, true)
		case ViewClock:
			r.drawClock(m, full)
		default:
			r.drawTimer(m, full, true)
		}
		if m.Conn != "" {
			r.drawConn(m.Conn)
		}
	}
	if m.Flash {
		invert(r.img)
	}
	if m.Mirror != "" {
		mirror(r.img, strings.Contains(m.Mirror, "h"), strings.Contains(m.Mirror, "v"))
	}
	return r.img
}

// ------------------------------------------------------------------ screens

func (r *Renderer) drawNotice(m Model, R image.Rectangle) {
	in := R.Inset(r.px(120))
	cx := (in.Min.X + in.Max.X) / 2
	tag := "STAGETIMER · BIRDDOG PLAY"
	tpx := r.px(30)
	r.center(r.bold, tpx, cx, in.Min.Y+r.px(40), tag, colDim)

	px := r.px(68)
	lines := r.bold.Wrap(px, m.Notice, in.Dx())
	dpx := r.px(38)
	var dlines []string
	if m.NoticeDetail != "" {
		dlines = r.reg.Wrap(dpx, m.NoticeDetail, in.Dx())
		if len(dlines) > 4 {
			dlines = append(dlines[:3], r.reg.Ellipsize(dpx, dlines[3], in.Dx()))
		}
	}
	h := len(lines)*px*6/5 + r.px(30) + len(dlines)*dpx*7/5
	y := (in.Min.Y+in.Max.Y)/2 - h/2
	for _, l := range lines {
		y += px * 6 / 5
		r.center(r.bold, px, cx, y, l, colFG)
	}
	y += r.px(30)
	for _, l := range dlines {
		y += dpx * 7 / 5
		r.center(r.reg, dpx, cx, y, l, colSecondary)
	}
	if m.Hint != "" {
		hpx := r.px(32)
		hpx = min(hpx, r.reg.FitLine(m.Hint, in.Dx(), hpx))
		r.center(r.reg, hpx, cx, in.Max.Y, m.Hint, colDim)
	}
}

// drawTimer is the speaker's view: title, countdown, speaker, message,
// progress. withClock puts the time of day in the top corner.
func (r *Renderer) drawTimer(m Model, R image.Rectangle, withClock bool) {
	in := R.Inset(r.px(56))
	cx := (in.Min.X + in.Max.X) / 2
	w := in.Dx()

	// Header: ON AIR on the left, the clock on the right.
	hb := r.px(80)
	r.drawHeader(m, image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+hb), withClock, false)
	top := in.Min.Y + hb + r.px(16)
	bottom := in.Max.Y

	if m.Msg.Show && m.Msg.Focus {
		r.drawMessage(m.Msg, image.Rect(in.Min.X, top, in.Max.X, bottom), r.px(240))
		return
	}
	if !m.HasTimer {
		name := m.RoomName
		if name == "" {
			name = "Stagetimer"
		}
		px := r.bold.FitLine(name, w, r.px(96))
		r.center(r.bold, px, cx, (top+bottom)/2, name, colSecondary)
		r.center(r.reg, r.px(40), cx, (top+bottom)/2+r.px(80), "No timer selected", colDim)
		if m.Msg.Show {
			mh := (bottom - top) * 30 / 100
			r.drawMessage(m.Msg, image.Rect(in.Min.X, bottom-mh, in.Max.X, bottom), r.px(110))
		}
		return
	}

	if m.ShowProgress {
		ph := r.px(26)
		r.drawProgress(m, image.Rect(in.Min.X, bottom-ph, in.Max.X, bottom))
		bottom -= ph + r.px(34)
	}
	if m.Next != "" {
		npx := r.px(40)
		npx = min(npx, r.reg.FitLine(m.Next, w, npx))
		_, d := r.reg.Metrics(npx)
		r.center(r.reg, npx, cx, bottom-d, r.reg.Ellipsize(npx, m.Next, w), colSecondary)
		bottom -= npx*6/5 + r.px(20)
	}
	if m.Msg.Show {
		mh := (bottom - top) * 34 / 100
		r.drawMessage(m.Msg, image.Rect(in.Min.X, bottom-mh, in.Max.X, bottom), r.px(120))
		bottom -= mh + r.px(24)
	}
	if m.Title != "" {
		tpx := r.bold.FitLine(m.Title, w, r.px(76))
		a, d := r.bold.Metrics(tpx)
		r.center(r.bold, tpx, cx, top+a, r.bold.Ellipsize(tpx, m.Title, w), colFG)
		top += a + d + r.px(8)
	}
	spH := 0
	if m.Speaker != "" {
		spH = r.px(84)
	}
	subH := 0
	if m.Sub != "" || m.State != "" {
		subH = r.px(76)
	}
	boxTop, boxBot := top, bottom-spH-subH
	if m.Main != "" && boxBot > boxTop {
		col := digitColor(m.Phase)
		if m.Dim {
			col = dim(col, 0.62)
		}
		// Size from the string's shape with every digit an 8, so the size does
		// not breathe as the numbers change.
		shape := shapeOf(m.Main)
		dpx := r.bold.FitTabular(shape, w*94/100, (boxBot-boxTop)*86/100)
		ch := r.bold.CapHeight(dpx)
		tw := r.bold.TabularWidth(dpx, m.Main)
		base := boxTop + (boxBot-boxTop+ch)/2
		r.bold.DrawTabular(r.img, r.img.Bounds(), dpx, cx-tw/2, base, m.Main, col)
	}
	y := boxBot
	if subH > 0 {
		label := m.Sub
		col := colSecondary
		if m.State != "" {
			label = m.State
			if m.Sub != "" {
				label = m.State + "   " + m.Sub
			}
		}
		spx := r.px(52)
		a, _ := r.bold.Metrics(spx)
		r.center(r.bold, spx, cx, y+a, label, col)
		y += subH
	}
	if m.Speaker != "" {
		spx := r.reg.FitLine(m.Speaker, w, r.px(58))
		a, _ := r.reg.Metrics(spx)
		r.center(r.reg, spx, cx, y+a, r.reg.Ellipsize(spx, m.Speaker, w), colSecondary)
	}
}

// drawHeader draws ON AIR at the left and, if asked, the clock at the right.
func (r *Renderer) drawHeader(m Model, R image.Rectangle, withClock, withName bool) {
	x := R.Min.X
	if m.OnAir {
		px := r.px(38)
		label := "ON AIR"
		tw := r.bold.Width(px, label)
		pad := r.px(22)
		h := R.Dy() * 3 / 4
		pill := image.Rect(x, R.Min.Y, x+tw+2*pad, R.Min.Y+h)
		roundRect(r.img, pill, h/2, colOnAir)
		ch := r.bold.CapHeight(px)
		r.bold.Draw(r.img, r.img.Bounds(), px, x+pad, pill.Min.Y+(h+ch)/2, label, colFG)
		x = pill.Max.X + r.px(28)
	}
	cw := 0
	if withClock && m.Clock != "" {
		px := r.px(52)
		cw = r.bold.TabularWidth(px, m.Clock)
		ch := r.bold.CapHeight(px)
		h := R.Dy() * 3 / 4
		r.bold.DrawTabular(r.img, r.img.Bounds(), px, R.Max.X-cw, R.Min.Y+(h+ch)/2, m.Clock, colSecondary)
	}
	if withName && m.RoomName != "" {
		px := r.px(52)
		room := r.bold.Ellipsize(px, m.RoomName, R.Max.X-cw-r.px(40)-x)
		ch := r.bold.CapHeight(px)
		h := R.Dy() * 3 / 4
		r.bold.Draw(r.img, r.img.Bounds(), px, x, R.Min.Y+(h+ch)/2, room, colFG)
	}
}

func (r *Renderer) drawProgress(m Model, R image.Rectangle) {
	w := R.Dx()
	at := func(t int) int { return R.Min.X + w*t/1000 }
	capsuleSpan(r.img, R, R.Min.X, R.Max.X, colTrack)
	// The wrap-up zones, faint, so the bar says where yellow and red begin.
	if m.YellowAt < m.RedAt {
		capsuleSpan(r.img, R, at(m.YellowAt), at(m.RedAt), dim(colYellow, 0.28))
	}
	if m.RedAt < 1000 {
		capsuleSpan(r.img, R, at(m.RedAt), R.Max.X, dim(colRed, 0.28))
	}
	if m.Progress > 0 {
		col := phaseColor(m.Phase)
		if m.Dim {
			col = dim(col, 0.62)
		}
		roundRect(r.img, image.Rect(R.Min.X, R.Min.Y, max(R.Min.X+R.Dy(), at(m.Progress)), R.Max.Y), R.Dy()/2, col)
	}
}

// capsuleSpan fills the columns x0..x1 of the capsule (a rectangle with fully
// rounded ends) that is bar.
func capsuleSpan(dst *image.RGBA, bar image.Rectangle, x0, x1 int, c color.RGBA) {
	rad := bar.Dy() / 2
	if rad <= 0 || x1 <= x0 {
		return
	}
	inner0, inner1 := bar.Min.X+rad, bar.Max.X-rad
	if a, b := max(x0, inner0), min(x1, inner1); b > a {
		fill(dst, image.Rect(a, bar.Min.Y, b, bar.Max.Y).Intersect(dst.Bounds()), c)
	}
	fr := float64(rad)
	cy := float64(bar.Min.Y) + fr
	for x := max(x0, bar.Min.X); x < min(x1, bar.Max.X); x++ {
		var cx float64
		switch {
		case x < inner0:
			cx = float64(inner0)
		case x >= inner1:
			cx = float64(inner1)
		default:
			continue
		}
		for y := bar.Min.Y; y < bar.Max.Y; y++ {
			if !(image.Point{x, y}).In(dst.Bounds()) {
				continue
			}
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			a := fr - math.Sqrt(dx*dx+dy*dy) + 0.5
			if a > 0 {
				blend(dst, x, y, c, math.Min(a, 1))
			}
		}
	}
}

func (r *Renderer) drawMessage(msg MsgModel, R image.Rectangle, maxPx int) {
	face := r.reg
	if msg.Bold {
		face = r.bold
	}
	px, lines, lh := face.FitParagraph(msg.Text, R.Dx(), R.Dy(), maxPx)
	a, _ := face.Metrics(px)
	cx := (R.Min.X + R.Max.X) / 2
	y := R.Min.Y + (R.Dy()-len(lines)*lh)/2 + a
	for _, l := range lines {
		r.center(face, px, cx, y, l, messageColor(msg.Color))
		y += lh
	}
}

func (r *Renderer) drawAgendaScreen(m Model, R image.Rectangle) {
	in := R.Inset(r.px(56))
	if m.Msg.Show && m.Msg.Focus {
		hb := r.px(80)
		r.drawHeader(m, image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+hb), true, true)
		r.drawMessage(m.Msg, image.Rect(in.Min.X, in.Min.Y+hb+r.px(16), in.Max.X, in.Max.Y), r.px(240))
		return
	}
	r.drawAgenda(m, R, false)
}

// drawAgenda is the rundown. compact is the narrower column of the split view.
func (r *Renderer) drawAgenda(m Model, R image.Rectangle, compact bool) {
	in := R.Inset(r.px(56))
	hb := r.px(80)
	hm := m
	if compact {
		hm.OnAir = false // the countdown beside it already says so
	}
	r.drawHeader(hm, image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+hb), true, true)
	top := in.Min.Y + hb + r.px(20)
	bottom := in.Max.Y
	if !compact && m.Msg.Show && !m.Msg.Focus {
		mh := (bottom - top) * 22 / 100
		band := image.Rect(in.Min.X, bottom-mh, in.Max.X, bottom)
		roundRect(r.img, band, r.px(18), colPanel)
		r.drawMessage(m.Msg, band.Inset(r.px(20)), r.px(96))
		bottom -= mh + r.px(20)
	}
	if len(m.Rows) == 0 {
		cx := (in.Min.X + in.Max.X) / 2
		r.center(r.reg, r.px(40), cx, (top+bottom)/2, "No timers in this room", colDim)
		return
	}
	rowH := r.px(118)
	gap := r.px(10)
	if compact {
		rowH = r.px(100)
	}
	n := max(1, (bottom-top+gap)/(rowH+gap))
	cur := -1
	for i, row := range m.Rows {
		if row.Current {
			cur = i
		}
	}
	first := 0
	if cur > 0 {
		first = cur - 1
	}
	if first+n > len(m.Rows) {
		first = max(0, len(m.Rows)-n)
	}
	y := top
	for i := first; i < len(m.Rows) && i < first+n; i++ {
		r.drawRow(m.Rows[i], image.Rect(in.Min.X, y, in.Max.X, y+rowH), compact)
		y += rowH + gap
	}
}

func (r *Renderer) drawRow(row RowModel, R image.Rectangle, compact bool) {
	pad := r.px(24)
	if row.Current {
		roundRect(r.img, R, r.px(16), colPanel)
		bar := image.Rect(R.Min.X, R.Min.Y+r.px(14), R.Min.X+r.px(8), R.Max.Y-r.px(14))
		roundRect(r.img, bar, r.px(4), phaseColor(row.Phase))
	}
	titleCol, subCol, timeCol := colFG, colSecondary, colSecondary
	if row.Past {
		titleCol, subCol, timeCol = colDim, colDim, colDim
	}
	if row.Estimate && !row.Current {
		timeCol = dim(timeCol, 0.7)
	}
	x := R.Min.X + pad + r.px(8)
	timeW := r.px(190)
	tpx, ttl, spx := r.px(40), r.px(46), r.px(30)
	if compact {
		timeW, tpx, ttl, spx = r.px(150), r.px(32), r.px(38), r.px(27)
	}
	mid := R.Min.Y + R.Dy()/2
	if row.Start != "" {
		r.reg.DrawTabular(r.img, R, tpx, x, mid+r.reg.CapHeight(tpx)/2-r.px(10), row.Start, timeCol)
	}
	if row.Next {
		npx := r.px(22)
		label := "NEXT"
		lw := r.bold.Width(npx, label)
		ph := r.px(32)
		py := mid + r.px(14)
		roundRect(r.img, image.Rect(x, py, x+lw+r.px(20), py+ph), ph/2, colTrack)
		r.bold.Draw(r.img, R, npx, x+r.px(10), py+(ph+r.bold.CapHeight(npx))/2, label, colFG)
	}
	// Right column: the length, or the live countdown on the current row.
	right := R.Max.X - pad
	rw := 0
	if row.Current && row.Countdown != "" {
		cpx := r.px(54)
		if compact {
			cpx = r.px(46)
		}
		rw = r.bold.TabularWidth(cpx, row.Countdown)
		r.bold.DrawTabular(r.img, R, cpx, right-rw, mid+r.bold.CapHeight(cpx)/2, row.Countdown, digitColor(row.Phase))
	} else if row.Length != "" {
		rw = r.reg.TabularWidth(tpx, row.Length)
		r.reg.DrawTabular(r.img, R, tpx, right-rw, mid+r.reg.CapHeight(tpx)/2, row.Length, timeCol)
	}
	tx := x + timeW
	tw := right - rw - r.px(30) - tx
	if tw <= 0 {
		return
	}
	hasSub := row.Speaker != "" || len(row.Labels) > 0
	if hasSub {
		r.bold.Draw(r.img, R, ttl, tx, mid-r.px(6), r.bold.Ellipsize(ttl, row.Title, tw), titleCol)
		sy := mid + r.px(6) + r.reg.CapHeight(spx) + r.px(8)
		sx := tx
		if row.Speaker != "" {
			s := r.reg.Ellipsize(spx, row.Speaker, tw)
			r.reg.Draw(r.img, R, spx, sx, sy, s, subCol)
			sx += r.reg.Width(spx, s) + r.px(18)
		}
		for _, lb := range row.Labels {
			if lb.Name == "" {
				continue
			}
			lpx := r.px(22)
			lw := r.bold.Width(lpx, lb.Name) + r.px(20)
			if sx+lw > tx+tw {
				break
			}
			ph := r.px(32)
			py := sy - r.reg.CapHeight(spx)/2 - ph/2
			lc := parseHex(lb.Color, colTrack)
			if row.Past {
				lc = dim(lc, 0.5)
			}
			roundRect(r.img, image.Rect(sx, py, sx+lw, py+ph), ph/2, lc)
			r.bold.Draw(r.img, R, lpx, sx+r.px(10), py+(ph+r.bold.CapHeight(lpx))/2, lb.Name, readableOn(lc))
			sx += lw + r.px(10)
		}
	} else {
		r.bold.Draw(r.img, R, ttl, tx, mid+r.bold.CapHeight(ttl)/2, r.bold.Ellipsize(ttl, row.Title, tw), titleCol)
	}
}

func (r *Renderer) drawClock(m Model, R image.Rectangle) {
	in := R.Inset(r.px(56))
	cx := (in.Min.X + in.Max.X) / 2
	hb := r.px(80)
	r.drawHeader(m, image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+hb), false, false)
	top := in.Min.Y + hb
	bottom := in.Max.Y
	if m.Msg.Show && m.Msg.Focus {
		r.drawMessage(m.Msg, image.Rect(in.Min.X, top+r.px(16), in.Max.X, bottom), r.px(240))
		return
	}
	if m.Msg.Show {
		mh := (bottom - top) * 30 / 100
		r.drawMessage(m.Msg, image.Rect(in.Min.X, bottom-mh, in.Max.X, bottom), r.px(110))
		bottom -= mh + r.px(20)
	}
	if m.RoomName != "" {
		px := r.bold.FitLine(m.RoomName, in.Dx(), r.px(56))
		a, _ := r.bold.Metrics(px)
		r.center(r.bold, px, cx, top+a, r.bold.Ellipsize(px, m.RoomName, in.Dx()), colSecondary)
		top += a + r.px(30)
	}
	dateH := r.px(90)
	dpx := r.bold.FitTabular(shapeOf(m.BigClock), in.Dx()*92/100, (bottom-top-dateH)*80/100)
	ch := r.bold.CapHeight(dpx)
	tw := r.bold.TabularWidth(dpx, m.BigClock)
	base := top + (bottom-top-dateH+ch)/2
	r.bold.DrawTabular(r.img, r.img.Bounds(), dpx, cx-tw/2, base, m.BigClock, colFG)
	r.center(r.reg, r.px(52), cx, base+r.px(110), m.DateLine, colSecondary)
}

func (r *Renderer) drawConn(text string) {
	px := r.px(28)
	tw := r.reg.Width(px, text)
	dot := r.px(16)
	x := r.W - r.px(40) - tw
	y := r.H - r.px(28)
	ch := r.reg.CapHeight(px)
	roundRect(r.img, image.Rect(x-dot-r.px(14), y-ch/2-dot/2, x-r.px(14), y-ch/2+dot/2), dot/2, colAmber)
	r.reg.Draw(r.img, r.img.Bounds(), px, x, y, text, colAmber)
}

// center draws s centred on cx with its baseline at y.
func (r *Renderer) center(t *Typeface, px, cx, y int, s string, c color.RGBA) {
	w := t.Width(px, s)
	t.Draw(r.img, r.img.Bounds(), px, cx-w/2, y, s, c)
}

// shapeOf replaces every digit with 8, the widest-looking, for sizing.
func shapeOf(s string) string {
	b := []rune(s)
	for i, c := range b {
		if c >= '0' && c <= '9' {
			b[i] = '8'
		}
	}
	return string(b)
}

// ------------------------------------------------------------------ pixels

func fill(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// roundRect fills r with corners of radius rad, anti-aliased.
func roundRect(dst *image.RGBA, r image.Rectangle, rad int, c color.RGBA) {
	r = r.Canon()
	rad = min(rad, r.Dx()/2, r.Dy()/2)
	if rad <= 0 {
		fill(dst, r.Intersect(dst.Bounds()), c)
		return
	}
	fill(dst, image.Rect(r.Min.X+rad, r.Min.Y, r.Max.X-rad, r.Max.Y).Intersect(dst.Bounds()), c)
	fill(dst, image.Rect(r.Min.X, r.Min.Y+rad, r.Min.X+rad, r.Max.Y-rad).Intersect(dst.Bounds()), c)
	fill(dst, image.Rect(r.Max.X-rad, r.Min.Y+rad, r.Max.X, r.Max.Y-rad).Intersect(dst.Bounds()), c)
	fr := float64(rad)
	corners := []struct{ x0, y0, cx, cy int }{
		{r.Min.X, r.Min.Y, r.Min.X + rad, r.Min.Y + rad},
		{r.Max.X - rad, r.Min.Y, r.Max.X - rad, r.Min.Y + rad},
		{r.Min.X, r.Max.Y - rad, r.Min.X + rad, r.Max.Y - rad},
		{r.Max.X - rad, r.Max.Y - rad, r.Max.X - rad, r.Max.Y - rad},
	}
	b := dst.Bounds()
	for _, k := range corners {
		for y := k.y0; y < k.y0+rad; y++ {
			for x := k.x0; x < k.x0+rad; x++ {
				if !(image.Point{x, y}).In(b) {
					continue
				}
				dx := float64(x) + 0.5 - float64(k.cx)
				dy := float64(y) + 0.5 - float64(k.cy)
				a := fr - math.Sqrt(dx*dx+dy*dy) + 0.5
				if a <= 0 {
					continue
				}
				blend(dst, x, y, c, math.Min(a, 1))
			}
		}
	}
}

func blend(dst *image.RGBA, x, y int, c color.RGBA, a float64) {
	i := dst.PixOffset(x, y)
	p := dst.Pix[i : i+4 : i+4]
	p[0] = uint8(float64(p[0])*(1-a) + float64(c.R)*a)
	p[1] = uint8(float64(p[1])*(1-a) + float64(c.G)*a)
	p[2] = uint8(float64(p[2])*(1-a) + float64(c.B)*a)
	p[3] = 255
}

func invert(img *image.RGBA) {
	p := img.Pix
	for i := 0; i+3 < len(p); i += 4 {
		p[i], p[i+1], p[i+2] = 255-p[i], 255-p[i+1], 255-p[i+2]
	}
}

func mirror(img *image.RGBA, h, v bool) {
	w, ht := img.Rect.Dx(), img.Rect.Dy()
	if h {
		for y := 0; y < ht; y++ {
			row := img.Pix[y*img.Stride : y*img.Stride+w*4]
			for i, j := 0, w-1; i < j; i, j = i+1, j-1 {
				a, b := row[i*4:i*4+4], row[j*4:j*4+4]
				for k := 0; k < 4; k++ {
					a[k], b[k] = b[k], a[k]
				}
			}
		}
	}
	if v {
		tmp := make([]byte, w*4)
		for i, j := 0, ht-1; i < j; i, j = i+1, j-1 {
			a := img.Pix[i*img.Stride : i*img.Stride+w*4]
			b := img.Pix[j*img.Stride : j*img.Stride+w*4]
			copy(tmp, a)
			copy(a, b)
			copy(b, tmp)
		}
	}
}

// parseHex reads "#rrggbb" (or "rrggbb"), else returns def.
func parseHex(s string, def color.RGBA) color.RGBA {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return def
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return def
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

// readableOn picks black or white text for a background.
func readableOn(c color.RGBA) color.RGBA {
	l := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	if l > 150 {
		return color.RGBA{0, 0, 0, 255}
	}
	return colFG
}
