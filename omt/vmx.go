package main

// libvmx, through purego.
//
// OMT video is VMX, and libvmx is the codec's reference implementation: NEON on
// arm64, sliced across threads. It is a C++ library with a C API, so it is
// dlopened rather than linked, the way bdcam reaches libndi — the shipped
// dist/libvmx.so is built from upstream plus vmx/libvmx-decode-nv12.patch by
// vmx/build-libvmx.sh.
//
// The patch adds VMX_DecodeNV12. kmssink on the PLAY accepts NV12 and nothing
// else (bdcam measured I420 and Y42B failing to negotiate), and VMX is 4:2:2, so
// decoding straight to NV12 saves a full-frame conversion pass per frame on a
// CPU with none to spare. A stock libvmx without the patch is refused at load.
//
// Pointers passed in are Go memory, which libvmx only uses for the duration of
// a call: VMX_LoadFrom copies the compressed slices into the instance, and the
// decode writes the output and returns once every slice thread has finished.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	vmxProfileOMTSQ = 166

	vmxErrOK                 = 0
	vmxErrInvalidCodecFormat = 2
	vmxErrInvalidSliceCount  = 3
	vmxErrBufferOverflow     = 4
)

// VMX is the loaded library.
type VMX struct {
	create     func(size uint64, profile, colorSpace int32) uintptr
	destroy    func(inst uintptr)
	setThreads func(inst uintptr, n int32)
	getThreads func(inst uintptr) int32
	loadFrom   func(inst uintptr, data *byte, n int32) int32
	decodeNV12 func(inst uintptr, y *byte, strideY int32, uv *byte, strideUV int32) int32
}

var (
	vmxOnce sync.Once
	vmxLib  *VMX
	vmxErr  error
)

// LoadVMX opens libvmx once per process.
func LoadVMX(path string) (*VMX, error) {
	vmxOnce.Do(func() { vmxLib, vmxErr = openVMX(path) })
	return vmxLib, vmxErr
}

// DefaultVMXPath is libvmx beside the executable, where the installer puts it.
func DefaultVMXPath() string {
	name := "libvmx.so"
	if runtime.GOOS == "darwin" {
		name = "libvmx.dylib"
	}
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	return filepath.Join(filepath.Dir(exe), name)
}

func openVMX(path string) (*VMX, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load %s: %v", path, err)
	}
	v := &VMX{}
	for _, f := range []struct {
		ptr any
		sym string
	}{
		{&v.create, "VMX_Create"},
		{&v.destroy, "VMX_Destroy"},
		{&v.setThreads, "VMX_SetThreads"},
		{&v.getThreads, "VMX_GetThreads"},
		{&v.loadFrom, "VMX_LoadFrom"},
		{&v.decodeNV12, "VMX_DecodeNV12"},
	} {
		s, err := purego.Dlsym(h, f.sym)
		if err != nil {
			if f.sym == "VMX_DecodeNV12" {
				return nil, fmt.Errorf("%s has no VMX_DecodeNV12: it is a stock libvmx, not the patched one bdomt ships (vmx/build-libvmx.sh)", path)
			}
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		purego.RegisterFunc(f.ptr, s)
	}
	return v, nil
}

// Decoder is one libvmx instance, which is tied to a frame size.
type Decoder struct {
	v          *VMX
	inst       uintptr
	W, H       int
	ColorSpace int
}

// NewDecoder creates an instance for w×h. threads <= 0 keeps libvmx's own
// choice for the size.
func (v *VMX) NewDecoder(w, h, colorSpace, threads int) (*Decoder, error) {
	if w <= 0 || h <= 0 || w > 8192 || h > 4320 {
		return nil, fmt.Errorf("vmx: unsupported frame size %dx%d", w, h)
	}
	switch colorSpace {
	case 0, 601, 709:
	default:
		colorSpace = 0
	}
	// VMX_SIZE {int width; int height} is passed by value; an 8-byte struct of
	// two ints travels in one integer register on arm64 and x86-64, width in
	// the low half.
	size := uint64(uint32(w)) | uint64(uint32(h))<<32
	inst := v.create(size, vmxProfileOMTSQ, int32(colorSpace))
	if inst == 0 {
		return nil, fmt.Errorf("vmx: could not create a %dx%d decoder", w, h)
	}
	if threads > 0 {
		v.setThreads(inst, int32(threads))
	}
	return &Decoder{v: v, inst: inst, W: w, H: h, ColorSpace: colorSpace}, nil
}

// Threads reports how many slice threads the instance uses.
func (d *Decoder) Threads() int { return int(d.v.getThreads(d.inst)) }

// DecodeNV12 decodes one compressed frame into dst, laid out as NV12Layout
// says.
func (d *Decoder) DecodeNV12(data, dst []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("vmx: empty frame")
	}
	stride, uvOff, size := NV12Layout(d.W, d.H)
	if len(dst) < size {
		return fmt.Errorf("vmx: output buffer is %d bytes, need %d", len(dst), size)
	}
	if rc := d.v.loadFrom(d.inst, &data[0], int32(len(data))); rc != vmxErrOK {
		runtime.KeepAlive(data)
		return vmxError("load", rc)
	}
	runtime.KeepAlive(data)
	rc := d.v.decodeNV12(d.inst, &dst[0], int32(stride), (*byte)(unsafe.Pointer(&dst[uvOff])), int32(stride))
	runtime.KeepAlive(dst)
	if rc != vmxErrOK {
		return vmxError("decode", rc)
	}
	return nil
}

func (d *Decoder) Close() {
	if d != nil && d.inst != 0 {
		d.v.destroy(d.inst)
		d.inst = 0
	}
}

// NV12Layout is GStreamer's default layout for a w×h NV12 frame, which is what
// rawvideoparse assumes when given only a width and height: rows padded to a
// multiple of four bytes, and the CbCr plane starting after an even number of
// luma rows. Matching it means no stride properties on the command line.
func NV12Layout(w, h int) (stride, uvOffset, size int) {
	stride = (w + 3) &^ 3
	rows := (h + 1) &^ 1
	return stride, stride * rows, stride * rows * 3 / 2
}

func vmxError(op string, rc int32) error {
	switch rc {
	case vmxErrInvalidCodecFormat:
		if op == "decode" {
			return fmt.Errorf("vmx: interlaced frames are not supported yet")
		}
		return fmt.Errorf("vmx: not a VMX frame")
	case vmxErrInvalidSliceCount:
		return fmt.Errorf("vmx: frame does not match its declared size")
	case vmxErrBufferOverflow:
		return fmt.Errorf("vmx: frame data is truncated or corrupt")
	}
	return fmt.Errorf("vmx: %s failed (error %d)", op, rc)
}
