package main

// Runtime bindings to libndi. We dlopen rather than link, so the binary carries
// no NDI code and runs on whatever libndi the device already has (PLAY ships
// both libndi.so.5.5.2 and libndi.so.6.0.1).
//
// Only free-SDK entry points are used. Notably NOT used: recv_create_v4 and
// recv_set_video_allocator, which are Advanced-SDK only (see notes/03).
//
// Struct layouts are for aarch64 LP64 and were read from the SDK headers:
//   NDIlib_source_t          16 bytes
//   NDIlib_find_create_t     24 bytes
//   NDIlib_recv_create_v3_t  40 bytes
//   NDIlib_metadata_frame_t  24 bytes

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	recvBandwidthMetadataOnly = -10 // NDIlib_recv_bandwidth_metadata_only
	recvColorFormatFastest    = 100 // NDIlib_recv_color_format_fastest
)

type ndiSource struct {
	pNDIName   *byte
	pURLAddres *byte
}

type ndiFindCreate struct {
	showLocalSources bool
	_                [7]byte
	pGroups          *byte
	pExtraIPs        *byte
}

type ndiRecvCreateV3 struct {
	source           ndiSource
	colorFormat      int32
	bandwidth        int32
	allowVideoFields bool
	_                [7]byte
	pNDIRecvName     *byte
}

type ndiMetadataFrame struct {
	length   int32
	_        [4]byte
	timecode int64
	pData    *byte
}

type NDI struct {
	handle uintptr

	initialize  func() bool
	destroy     func()
	findCreate  func(*ndiFindCreate) uintptr
	findDestroy func(uintptr)
	findWait    func(uintptr, uint32) bool
	findCurrent func(uintptr, *uint32) uintptr

	recvCreate   func(*ndiRecvCreateV3) uintptr
	recvDestroy  func(uintptr)
	recvSendMeta func(uintptr, *ndiMetadataFrame) bool
	recvCapture  func(uintptr, uintptr, uintptr, uintptr, uint32) int32
	recvNumConns func(uintptr) int32
}

// candidate sonames, newest first — PLAY has both
var ndiLibs = []string{
	"libndi.so.6", "libndi.so.6.0.1",
	"libndi.so.5", "libndi.so.5.5.2",
	"libndi.so",
}

func LoadNDI() (*NDI, error) {
	var h uintptr
	var err error
	for _, name := range ndiLibs {
		h, err = purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil && h != 0 {
			logf("loaded %s", name)
			break
		}
	}
	if h == 0 {
		return nil, fmt.Errorf("could not dlopen libndi (tried %v): %v", ndiLibs, err)
	}
	n := &NDI{handle: h}
	reg := func(ptr any, sym string) error {
		s, e := purego.Dlsym(h, sym)
		if e != nil || s == 0 {
			return fmt.Errorf("missing symbol %s", sym)
		}
		purego.RegisterFunc(ptr, s)
		return nil
	}
	for _, b := range []struct {
		p   any
		sym string
	}{
		{&n.initialize, "NDIlib_initialize"},
		{&n.destroy, "NDIlib_destroy"},
		{&n.findCreate, "NDIlib_find_create_v2"},
		{&n.findDestroy, "NDIlib_find_destroy"},
		{&n.findWait, "NDIlib_find_wait_for_sources"},
		{&n.findCurrent, "NDIlib_find_get_current_sources"},
		{&n.recvCreate, "NDIlib_recv_create_v3"},
		{&n.recvDestroy, "NDIlib_recv_destroy"},
		{&n.recvSendMeta, "NDIlib_recv_send_metadata"},
		{&n.recvCapture, "NDIlib_recv_capture_v2"},
		{&n.recvNumConns, "NDIlib_recv_get_no_connections"},
	} {
		if e := reg(b.p, b.sym); e != nil {
			return nil, e
		}
	}
	if !n.initialize() {
		return nil, fmt.Errorf("NDIlib_initialize failed (unsupported CPU?)")
	}
	return n, nil
}

func (n *NDI) Close() {
	if n != nil && n.destroy != nil {
		n.destroy()
	}
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

// FindSourceURL resolves an NDI source *name* ("MACHINE (Source)") to its URL,
// so the receiver connects to the right one. Returns "" if not found in time.
func (n *NDI) FindSourceURL(name string, timeoutMs uint32) (string, bool) {
	fc := ndiFindCreate{showLocalSources: true}
	f := n.findCreate(&fc)
	if f == 0 {
		return "", false
	}
	defer n.findDestroy(f)

	n.findWait(f, timeoutMs)
	var count uint32
	p := n.findCurrent(f, &count)
	if p == 0 || count == 0 {
		return "", false
	}
	srcs := unsafe.Slice((*ndiSource)(unsafe.Pointer(p)), count)
	for i := range srcs {
		if goStr(srcs[i].pNDIName) == name {
			return goStr(srcs[i].pURLAddres), true
		}
	}
	return "", false
}

func goStr(p *byte) string {
	if p == nil {
		return ""
	}
	var out []byte
	for i := 0; ; i++ {
		c := *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(i)))
		if c == 0 {
			break
		}
		out = append(out, c)
	}
	return string(out)
}

// Receiver is a metadata-only NDI receiver. Bandwidth is
// NDIlib_recv_bandwidth_metadata_only, so this costs essentially nothing on the
// wire and can run alongside PPApp's own full-bandwidth receiver on the same
// source — which is how the KVM endpoint avoids touching PPApp at all.
type Receiver struct {
	n    *NDI
	inst uintptr
	keep []*byte // keep C strings alive for the receiver's lifetime
}

func (n *NDI) NewMetadataReceiver(sourceName, url, recvName string) (*Receiver, error) {
	var namePtr, urlPtr *byte
	keep := []*byte{}
	if sourceName != "" {
		namePtr = cstr(sourceName)
		keep = append(keep, namePtr)
	}
	if url != "" {
		urlPtr = cstr(url)
		keep = append(keep, urlPtr)
	}
	rn := cstr(recvName)
	keep = append(keep, rn)

	c := ndiRecvCreateV3{
		source:           ndiSource{pNDIName: namePtr, pURLAddres: urlPtr},
		colorFormat:      recvColorFormatFastest,
		bandwidth:        recvBandwidthMetadataOnly,
		allowVideoFields: false,
		pNDIRecvName:     rn,
	}
	inst := n.recvCreate(&c)
	if inst == 0 {
		return nil, fmt.Errorf("NDIlib_recv_create_v3 failed")
	}
	return &Receiver{n: n, inst: inst, keep: keep}, nil
}

func (r *Receiver) Close() {
	if r != nil && r.inst != 0 {
		r.n.recvDestroy(r.inst)
		r.inst = 0
	}
}

func (r *Receiver) Connected() bool { return r.n.recvNumConns(r.inst) > 0 }

// Pump drains the receiver. A receiver that is never captured from will stall,
// so this must be called periodically even though we only care about sending.
func (r *Receiver) Pump(timeoutMs uint32) { r.n.recvCapture(r.inst, 0, 0, 0, timeoutMs) }

// SendMetadata sends one metadata frame upstream to the sender.
func (r *Receiver) SendMetadata(xml string) bool {
	b := append([]byte(xml), 0)
	m := ndiMetadataFrame{length: int32(len(b)), timecode: 0, pData: &b[0]}
	return r.n.recvSendMeta(r.inst, &m)
}
