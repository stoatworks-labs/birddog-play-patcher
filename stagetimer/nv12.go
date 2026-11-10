package main

// RGBA to NV12, the only format the PLAY's kmssink takes (bdomt's finding,
// omt/AGENTS.md rule 5).
//
// BT.709, limited range: the matrix HD video uses and the one the VOP's
// YUV-to-RGB conversion is likeliest to assume. Black is Y=16 and white Y=235.
// Chroma is the mean of each 2×2 block. Integer coefficients ×256.

import "image"

// NV12Layout is the layout rawvideoparse assumes when given only a width and
// height: rows padded to a multiple of four bytes, and the CbCr plane starting
// after an even number of luma rows. bdomt's, unchanged.
func NV12Layout(w, h int) (stride, uvOffset, size int) {
	stride = (w + 3) &^ 3
	rows := (h + 1) &^ 1
	return stride, stride * rows, stride * rows * 3 / 2
}

// ToNV12 converts img into dst, which must be NV12Layout's size.
func ToNV12(img *image.RGBA, dst []byte) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	stride, uvOff, size := NV12Layout(w, h)
	if len(dst) < size {
		panic("nv12: buffer too small")
	}
	pix, ps := img.Pix, img.Stride
	for y := 0; y < h; y++ {
		row := pix[y*ps : y*ps+w*4]
		out := dst[y*stride : y*stride+w]
		for x := 0; x < w; x++ {
			r, g, b := int(row[x*4]), int(row[x*4+1]), int(row[x*4+2])
			out[x] = uint8((47*r+157*g+16*b+128)>>8 + 16)
		}
	}
	if h%2 == 1 { // the padding row repeats the last
		copy(dst[h*stride:h*stride+w], dst[(h-1)*stride:(h-1)*stride+w])
	}
	for y := 0; y < h; y += 2 {
		y2 := min(y+1, h-1)
		r0 := pix[y*ps:]
		r1 := pix[y2*ps:]
		out := dst[uvOff+(y/2)*stride:]
		for x := 0; x < w; x += 2 {
			x2 := min(x+1, w-1)
			i0, i1 := x*4, x2*4
			r := int(r0[i0]) + int(r0[i1]) + int(r1[i0]) + int(r1[i1])
			g := int(r0[i0+1]) + int(r0[i1+1]) + int(r1[i0+1]) + int(r1[i1+1])
			b := int(r0[i0+2]) + int(r0[i1+2]) + int(r1[i0+2]) + int(r1[i1+2])
			// Sums of four, so the shift is 8+2.
			// -26-86+112 and 112-102-10 both sum to zero, so grey stays 128.
			cb := (-26*r - 86*g + 112*b + 512) >> 10
			cr := (112*r - 102*g - 10*b + 512) >> 10
			out[x] = uint8(clampByte(cb + 128))
			out[x+1] = uint8(clampByte(cr + 128))
		}
	}
}

func clampByte(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
