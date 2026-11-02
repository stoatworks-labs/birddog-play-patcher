package main

// The shim's AOI maths (bdout_apply_aoi in shim/core.c), in Go, so the tab can
// say in pixels what a setting will do before it is applied. The two must give
// the same answer; TestPreviewMatchesTheShim runs both.

import "fmt"

// Plane is where a picture goes: Src in the picture, Dst on the screen, pixels.
type Plane struct {
	SX int `json:"sx"`
	SY int `json:"sy"`
	SW int `json:"sw"`
	SH int `json:"sh"`
	DX int `json:"dx"`
	DY int `json:"dy"`
	DW int `json:"dw"`
	DH int `json:"dh"`
}

const maxScale = 8 // the VOP scales at most 8:1 either way

// part rounds to the nearest pixel, as the shim does: a crop typed in pixels
// comes back as the same pixels.
func part(n, millionths int) int { return int((int64(n)*int64(millionths) + Unit/2) / Unit) }
func even(v int) int             { return v &^ 1 }

// Preview gives the plane an AOI makes of a pw×ph picture on a sw×sh screen.
func Preview(a AOI, pw, ph, sw, sh int) (Plane, error) {
	if pw < 16 || ph < 16 || sw < 16 || sh < 16 {
		return Plane{}, fmt.Errorf("picture %dx%d or screen %dx%d too small", pw, ph, sw, sh)
	}
	p := Plane{
		SX: even(part(pw, a.Src.X)), SY: even(part(ph, a.Src.Y)),
		SW: even(part(pw, a.Src.W)), SH: even(part(ph, a.Src.H)),
		DX: even(part(sw, a.Dst.X)), DY: even(part(sh, a.Dst.Y)),
		DW: even(part(sw, a.Dst.W)), DH: even(part(sh, a.Dst.H)),
	}
	if p.SX+p.SW > pw {
		p.SW = even(pw - p.SX)
	}
	if p.SY+p.SH > ph {
		p.SH = even(ph - p.SY)
	}
	if p.DX+p.DW > sw {
		p.DW = even(sw - p.DX)
	}
	if p.DY+p.DH > sh {
		p.DH = even(sh - p.DY)
	}
	if p.SW < 16 || p.SH < 16 {
		return Plane{}, fmt.Errorf("the source area is %dx%d of a %dx%d picture, too small to show", p.SW, p.SH, pw, ph)
	}
	if p.DW < 16 || p.DH < 16 {
		return Plane{}, fmt.Errorf("the output area is %dx%d, too small to show", p.DW, p.DH)
	}
	if a.KeepAspect {
		x, y := int64(p.SW)*int64(p.DH), int64(p.DW)*int64(p.SH)
		switch {
		case x > y: // the source is wider: full width, bars above and below
			nh := even(int((int64(p.DW)*int64(p.SH) + int64(p.SW)/2) / int64(p.SW)))
			p.DY += even((p.DH - nh) / 2)
			p.DH = nh
		case x < y: // taller: full height, bars at the sides
			nw := even(int((int64(p.DH)*int64(p.SW) + int64(p.SH)/2) / int64(p.SH)))
			p.DX += even((p.DW - nw) / 2)
			p.DW = nw
		}
		if p.DW < 16 || p.DH < 16 {
			return Plane{}, fmt.Errorf("keeping the aspect ratio leaves a %dx%d picture", p.DW, p.DH)
		}
	}
	if p.SW > p.DW*maxScale || p.DW > p.SW*maxScale || p.SH > p.DH*maxScale || p.DH > p.SH*maxScale {
		return Plane{}, fmt.Errorf("%dx%d to %dx%d is beyond the scaler's 8:1", p.SW, p.SH, p.DW, p.DH)
	}
	return p, nil
}
