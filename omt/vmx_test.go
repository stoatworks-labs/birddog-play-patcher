package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ebitengine/purego"
)

// The tests that decode need this machine's build of the patched libvmx:
// vmx/build-libvmx.sh host, or BDOMT_TEST_LIBVMX pointing at one. Without it
// they skip, saying so.
func testVMXPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("BDOMT_TEST_LIBVMX"); p != "" {
		return p
	}
	name := "libvmx.so"
	if runtime.GOOS == "darwin" {
		name = "libvmx.dylib"
	}
	p, _ := filepath.Abs(filepath.Join("vmx", ".build", name))
	if _, err := os.Stat(p); err != nil {
		t.Skip("no host libvmx: run vmx/build-libvmx.sh host (or set BDOMT_TEST_LIBVMX)")
	}
	return p
}

// encodeFlat makes a VMX frame of one flat colour with libvmx's own encoder,
// so the tests need no recorded video.
func encodeFlat(t *testing.T, path string, w, h int, y, cb, cr byte) []byte {
	t.Helper()
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatal(err)
	}
	var (
		create  func(size uint64, profile, colorSpace int32) uintptr
		encode  func(inst uintptr, src *byte, stride, interlaced int32) int32
		save    func(inst uintptr, dst *byte, maxLen int32) int32
		destroy func(inst uintptr)
	)
	purego.RegisterLibFunc(&create, lib, "VMX_Create")
	purego.RegisterLibFunc(&encode, lib, "VMX_EncodeUYVY")
	purego.RegisterLibFunc(&save, lib, "VMX_SaveTo")
	purego.RegisterLibFunc(&destroy, lib, "VMX_Destroy")

	inst := create(uint64(uint32(w))|uint64(uint32(h))<<32, vmxProfileOMTSQ, 709)
	if inst == 0 {
		t.Fatal("VMX_Create failed")
	}
	defer destroy(inst)
	uyvy := make([]byte, w*h*2)
	for i := 0; i < len(uyvy); i += 4 {
		uyvy[i], uyvy[i+1], uyvy[i+2], uyvy[i+3] = cb, y, cr, y
	}
	if rc := encode(inst, &uyvy[0], int32(w*2), 0); rc != 0 {
		t.Fatalf("VMX_EncodeUYVY: %d", rc)
	}
	out := make([]byte, w*h*4)
	n := save(inst, &out[0], int32(len(out)))
	if n <= 0 {
		t.Fatalf("VMX_SaveTo: %d", n)
	}
	return out[:n]
}

func near(a, b byte) bool { return a-b < 4 || b-a < 4 }

// checkFlat reports whether an NV12 frame is (close to) one flat colour.
func checkFlat(nv12 []byte, w, h int, y, cb, cr byte) bool {
	stride, uvOff, _ := NV12Layout(w, h)
	for row := 0; row < h; row++ {
		for x := 0; x < w; x++ {
			if !near(nv12[row*stride+x], y) {
				return false
			}
		}
	}
	for row := 0; row < h/2; row++ {
		for x := 0; x < w; x += 2 {
			if !near(nv12[uvOff+row*stride+x], cb) || !near(nv12[uvOff+row*stride+x+1], cr) {
				return false
			}
		}
	}
	return true
}

func TestDecodeNV12(t *testing.T) {
	path := testVMXPath(t)
	v, err := LoadVMX(path)
	if err != nil {
		t.Fatal(err)
	}
	const w, h = 128, 64
	frame := encodeFlat(t, path, w, h, 180, 90, 200)
	d, err := v.NewDecoder(w, h, 709, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, _, size := NV12Layout(w, h)
	out := make([]byte, size)
	if err := d.DecodeNV12(frame, out); err != nil {
		t.Fatal(err)
	}
	if !checkFlat(out, w, h, 180, 90, 200) {
		t.Fatalf("decoded frame is not the colour encoded: Y %d CbCr %d,%d", out[0], out[size*2/3], out[size*2/3+1])
	}
	if err := d.DecodeNV12(frame[:len(frame)/2], out); err == nil {
		t.Error("a truncated frame decoded without error")
	}
	if err := d.DecodeNV12(frame, out[:size-1]); err == nil {
		t.Error("a short output buffer was accepted")
	}
}

func TestNV12Layout(t *testing.T) {
	for _, c := range []struct{ w, h, stride, uv, size int }{
		{1920, 1080, 1920, 1920 * 1080, 1920 * 1080 * 3 / 2},
		{1282, 721, 1284, 1284 * 722, 1284 * 722 * 3 / 2},
	} {
		s, uv, size := NV12Layout(c.w, c.h)
		if s != c.stride || uv != c.uv || size != c.size {
			t.Errorf("%dx%d: got %d %d %d", c.w, c.h, s, uv, size)
		}
	}
}
