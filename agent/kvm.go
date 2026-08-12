package main

// The <ndi_kvm> wire format.
//
// Receiver -> sender metadata carrying keyboard/mouse events. The opcodes below
// were reverse-engineered and byte-verified against a real NDI sender during the
// glkvm-cloud NDI KVM work; see the reference note of the same name.
//
//	<ndi_kvm u="BASE64"/>
//
// Payload, little-endian:
//	03  mouse position   float32 x, float32 y (normalised 0..1), then 0x01
//	04/05/06  left/middle/right button press
//	07/08/09  left/middle/right button release
//	0A  vertical wheel   float32
//	0C  keyboard         int32 X11 keysym, int32 (1 = press, 0 = release)
//
// Senders advertise support via <ndi_capabilities>, but recv_send_metadata
// emits regardless of whether is_supported is set — that flag only reflects the
// advertisement, it does not gate sending.

import (
	"encoding/base64"
	"encoding/binary"
	"math"
)

const (
	opMousePos      = 0x03
	opLeftPress     = 0x04
	opMiddlePress   = 0x05
	opRightPress    = 0x06
	opLeftRelease   = 0x07
	opMiddleRelease = 0x08
	opRightRelease  = 0x09
	opWheel         = 0x0A
	opKeyboard      = 0x0C
)

func wrap(payload []byte) string {
	return `<ndi_kvm u="` + base64.StdEncoding.EncodeToString(payload) + `"/>`
}

func f32(b []byte, v float32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], math.Float32bits(v))
	return append(b, tmp[:]...)
}

func i32(b []byte, v int32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(v))
	return append(b, tmp[:]...)
}

// MousePos — x and y are normalised to 0.0..1.0 across the remote screen.
func MousePos(x, y float32) string {
	b := []byte{opMousePos}
	b = f32(b, clamp01(x))
	b = f32(b, clamp01(y))
	b = append(b, 0x01)
	return wrap(b)
}

func MouseButton(button int, pressed bool) string {
	var op byte
	switch button {
	case 0:
		op = opLeftPress
	case 1:
		op = opMiddlePress
	case 2:
		op = opRightPress
	default:
		return ""
	}
	if !pressed {
		op += 3 // press opcodes 04/05/06 -> release 07/08/09
	}
	return wrap([]byte{op})
}

// Wheel — positive is usually "away from the user"/scroll up.
func Wheel(delta float32) string {
	return wrap(f32([]byte{opWheel}, delta))
}

// Key — keysym is an X11 keysym, not a Linux keycode.
func Key(keysym int32, pressed bool) string {
	b := []byte{opKeyboard}
	b = i32(b, keysym)
	if pressed {
		b = i32(b, 1)
	} else {
		b = i32(b, 0)
	}
	return wrap(b)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
