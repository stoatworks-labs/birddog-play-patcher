package main

// Minimal evdev reader for the keyboard/mouse on the PLAY's USB-A host port.
//
// Devices are taken from /dev/input/by-id/*-kbd and *-mouse, which is exactly
// the set BirdDog's own birddog-kvm-manager looks for before starting the KVM
// service — so we inherit their hotplug behaviour for free.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// struct input_event on 64-bit Linux: timeval{sec,usec} int64 x2, then u16,u16,s32
const inputEventSize = 24

// EVIOCGRAB = _IOW('E', 0x90, int)
const eviocgrab = 0x40044590

const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02

	relX     = 0x00
	relY     = 0x01
	relWheel = 0x08

	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112
)

type InputEvent struct {
	Type  uint16
	Code  uint16
	Value int32
}

type Device struct {
	Path string
	f    *os.File
}

// DiscoverDevices returns the keyboard and mouse device nodes, if present.
func DiscoverDevices() []string {
	var out []string
	matches, _ := filepath.Glob("/dev/input/by-id/*")
	for _, m := range matches {
		base := strings.ToLower(filepath.Base(m))
		if strings.HasSuffix(base, "-kbd") || strings.HasSuffix(base, "-mouse") ||
			strings.HasSuffix(base, "-event-kbd") || strings.HasSuffix(base, "-event-mouse") {
			// only the event* nodes carry evdev; the plain -mouse node is mousedev
			if strings.Contains(base, "event") {
				out = append(out, m)
			}
		}
	}
	return out
}

// OpenDevice opens a device and, if grab is set, takes it exclusively so
// keystrokes do not also reach the PLAY's own console.
func OpenDevice(path string, grab bool) (*Device, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if grab {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
			uintptr(eviocgrab), uintptr(unsafe.Pointer(new(int32)))); e != 0 {
			logf("warning: EVIOCGRAB failed on %s (%v) — keys will also reach the local console", path, e)
		}
	}
	return &Device{Path: path, f: f}, nil
}

func (d *Device) Close() error {
	if d.f != nil {
		return d.f.Close()
	}
	return nil
}

// Read blocks for the next event.
func (d *Device) Read() (InputEvent, error) {
	var buf [inputEventSize]byte
	if _, err := readFull(d.f, buf[:]); err != nil {
		return InputEvent{}, err
	}
	return InputEvent{
		Type:  binary.LittleEndian.Uint16(buf[16:18]),
		Code:  binary.LittleEndian.Uint16(buf[18:20]),
		Value: int32(binary.LittleEndian.Uint32(buf[20:24])),
	}, nil
}

func readFull(f *os.File, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := f.Read(b[got:])
		if err != nil {
			return got, err
		}
		got += n
	}
	return got, nil
}
