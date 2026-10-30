package main

// OMT audio to HDMI.
//
// OMT carries audio as FPA1: 32-bit float, planar, one block of
// SamplesPerChannel floats per channel. Channels that are silent for the whole
// frame are left out to save bandwidth, and ActiveChannels says which are
// present, so channel i's block is at (active channels below i) × block size.
//
// HDMI on the PLAY is card 1 (rockchiphdmi, hw:1,0), and a TV wants stereo, so
// the first two channels go out as interleaved signed 16-bit; a mono source is
// sent to both sides. Anything past channel 2 is not mixed in — a downmix would
// guess at a layout the sender never declared.

import (
	"encoding/binary"
	"fmt"
	"math"
)

const maxAudioChannels = 32

// fpa1ToS16Stereo converts one FPA1 frame, appending to dst.
func fpa1ToS16Stereo(h AudioHeader, data, dst []byte) ([]byte, error) {
	n := h.SamplesPerChannel
	if h.Channels <= 0 || h.Channels > maxAudioChannels || n <= 0 || h.SampleRate <= 0 {
		return dst, fmt.Errorf("audio: unusable header (%d channels, %d samples, %d Hz)", h.Channels, n, h.SampleRate)
	}
	block := n * 4
	offset := func(ch int) int {
		if ch >= h.Channels || h.ActiveChannels&(1<<uint(ch)) == 0 {
			return -1
		}
		k := 0
		for i := 0; i < ch; i++ {
			if h.ActiveChannels&(1<<uint(i)) != 0 {
				k++
			}
		}
		return k * block
	}
	left, right := offset(0), offset(1)
	if h.Channels == 1 {
		right = left
	}
	for _, o := range []int{left, right} {
		if o >= 0 && o+block > len(data) {
			return dst, fmt.Errorf("audio: frame is %d bytes, header needs %d", len(data), o+block)
		}
	}

	start := len(dst)
	need := n * 4
	if cap(dst)-start < need {
		grown := make([]byte, start, start+need)
		copy(grown, dst)
		dst = grown
	}
	dst = dst[:start+need]
	out := dst[start:]
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(out[i*4:], uint16(sample(data, left, i)))
		binary.LittleEndian.PutUint16(out[i*4+2:], uint16(sample(data, right, i)))
	}
	return dst, nil
}

// sample reads float sample i of the block at off (or silence for a missing
// channel) as a clamped int16.
func sample(data []byte, off, i int) int16 {
	if off < 0 {
		return 0
	}
	f := math.Float32frombits(binary.LittleEndian.Uint32(data[off+i*4:]))
	if f != f { // NaN
		return 0
	}
	v := f * 32767
	switch {
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	}
	return int16(v)
}
