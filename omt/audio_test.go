package main

import (
	"encoding/binary"
	"math"
	"testing"
)

// fpa1 builds an FPA1 payload from per-channel samples, leaving out the
// channels whose bit is clear in active, as a sender does.
func fpa1(active uint32, chans ...[]float32) []byte {
	var out []byte
	for ch, s := range chans {
		if active&(1<<uint(ch)) == 0 {
			continue
		}
		for _, v := range s {
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v))
		}
	}
	return out
}

func stereoAt(pcm []byte, i int) (int16, int16) {
	return int16(binary.LittleEndian.Uint16(pcm[i*4:])), int16(binary.LittleEndian.Uint16(pcm[i*4+2:]))
}

func TestFPA1Stereo(t *testing.T) {
	h := AudioHeader{Codec: codecFPA1, SampleRate: 48000, SamplesPerChannel: 3, Channels: 2, ActiveChannels: 3}
	pcm, err := fpa1ToS16Stereo(h, fpa1(3, []float32{0, 0.5, -1}, []float32{1, -0.5, 2}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 12 {
		t.Fatalf("got %d bytes, want 12", len(pcm))
	}
	want := [][2]int16{{0, 32767}, {16383, -16383}, {-32767, 32767}}
	for i, w := range want {
		if l, r := stereoAt(pcm, i); l != w[0] || r != w[1] {
			t.Errorf("sample %d: got %d,%d want %d,%d", i, l, r, w[0], w[1])
		}
	}
}

func TestFPA1MonoGoesToBothSides(t *testing.T) {
	h := AudioHeader{Codec: codecFPA1, SampleRate: 44100, SamplesPerChannel: 2, Channels: 1, ActiveChannels: 1}
	pcm, err := fpa1ToS16Stereo(h, fpa1(1, []float32{0.25, -0.25}), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if l, r := stereoAt(pcm, i); l != r || l == 0 {
			t.Errorf("sample %d: %d,%d", i, l, r)
		}
	}
}

// Silent channels are left out of the payload, so channel 1 of a frame whose
// channel 0 is silent is the first block, and a silent left is silence.
func TestFPA1LeftOutChannels(t *testing.T) {
	h := AudioHeader{Codec: codecFPA1, SampleRate: 48000, SamplesPerChannel: 2, Channels: 4, ActiveChannels: 0b1010}
	data := fpa1(0b1010, nil, []float32{0.5, 0.5}, nil, []float32{-1, -1})
	pcm, err := fpa1ToS16Stereo(h, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l, r := stereoAt(pcm, 0); l != 0 || r != 16383 {
		t.Fatalf("got %d,%d want 0,16383 (channel 3 must not leak into the right)", l, r)
	}
}

func TestFPA1NaNAndClamp(t *testing.T) {
	h := AudioHeader{Codec: codecFPA1, SampleRate: 48000, SamplesPerChannel: 2, Channels: 2, ActiveChannels: 3}
	nan := float32(math.NaN())
	pcm, err := fpa1ToS16Stereo(h, fpa1(3, []float32{nan, float32(math.Inf(-1))}, []float32{40, -40}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if l, r := stereoAt(pcm, 0); l != 0 || r != 32767 {
		t.Errorf("NaN/+clip: got %d,%d", l, r)
	}
	if l, r := stereoAt(pcm, 1); l != -32768 || r != -32768 {
		t.Errorf("-inf/-clip: got %d,%d", l, r)
	}
}

func TestFPA1RefusesShortOrNonsense(t *testing.T) {
	h := AudioHeader{Codec: codecFPA1, SampleRate: 48000, SamplesPerChannel: 100, Channels: 2, ActiveChannels: 3}
	if _, err := fpa1ToS16Stereo(h, make([]byte, 100*4), nil); err == nil {
		t.Error("a frame one channel short was accepted")
	}
	for _, bad := range []AudioHeader{
		{SampleRate: 48000, SamplesPerChannel: 1, Channels: 0},
		{SampleRate: 48000, SamplesPerChannel: 1, Channels: 64},
		{SampleRate: 0, SamplesPerChannel: 1, Channels: 2},
		{SampleRate: 48000, SamplesPerChannel: 0, Channels: 2},
	} {
		if _, err := fpa1ToS16Stereo(bad, make([]byte, 1024), nil); err == nil {
			t.Errorf("header %+v accepted", bad)
		}
	}
}
