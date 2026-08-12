package main

import (
	"encoding/base64"
	"regexp"
	"testing"
)

var re = regexp.MustCompile(`^<ndi_kvm u="([^"]*)"/>$`)

func payload(t *testing.T, xml string) []byte {
	t.Helper()
	m := re.FindStringSubmatch(xml)
	if m == nil {
		t.Fatalf("bad wrapper: %q", xml)
	}
	b, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func eq(t *testing.T, got, want []byte, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: length %d, want %d (% x)", what, len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: byte %d = %#x, want %#x (full: % x)", what, i, got[i], want[i], got)
		}
	}
}

func TestMousePos(t *testing.T) {
	// 0.5, 0.25 little-endian float32 = 00 00 00 3F / 00 00 80 3E, then 0x01
	eq(t, payload(t, MousePos(0.5, 0.25)),
		[]byte{0x03, 0x00, 0x00, 0x00, 0x3f, 0x00, 0x00, 0x80, 0x3e, 0x01}, "mouse pos")
}

func TestMousePosClamps(t *testing.T) {
	// 2.0 must clamp to 1.0 = 00 00 80 3F, -1 to 0.0
	eq(t, payload(t, MousePos(2.0, -1.0)),
		[]byte{0x03, 0x00, 0x00, 0x80, 0x3f, 0x00, 0x00, 0x00, 0x00, 0x01}, "clamped")
}

func TestButtons(t *testing.T) {
	for _, c := range []struct {
		btn     int
		pressed bool
		op      byte
	}{
		{0, true, 0x04}, {1, true, 0x05}, {2, true, 0x06},
		{0, false, 0x07}, {1, false, 0x08}, {2, false, 0x09},
	} {
		eq(t, payload(t, MouseButton(c.btn, c.pressed)), []byte{c.op}, "button")
	}
}

func TestWheel(t *testing.T) {
	// 1.0 = 00 00 80 3F
	eq(t, payload(t, Wheel(1.0)), []byte{0x0a, 0x00, 0x00, 0x80, 0x3f}, "wheel")
}

func TestKeyboard(t *testing.T) {
	// 'a' = 0x61 keysym, press=1
	eq(t, payload(t, Key('a', true)),
		[]byte{0x0c, 0x61, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}, "key press")
	// Return = 0xFF0D, release=0
	eq(t, payload(t, Key(0xFF0D, false)),
		[]byte{0x0c, 0x0d, 0xff, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, "key release")
}

func TestKeymapSanity(t *testing.T) {
	for code, want := range map[uint16]int32{
		30: 'a', 2: '1', 57: ' ', 28: 0xFF0D, 1: 0xFF1B,
		59: 0xFFBE, 88: 0xFFC9, // F1, F12
		42: 0xFFE1, 103: 0xFF52, // Shift_L, Up
	} {
		got, ok := Keysym(code)
		if !ok || got != want {
			t.Errorf("keycode %d -> %#x (ok=%v), want %#x", code, got, ok, want)
		}
	}
}
