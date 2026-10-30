package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"strings"
	"testing"
)

// The frames a sender puts on the wire, built the way libomtnet's OMTFrame
// writes them, for the tests and the fake sender.

func wireFrame(typ byte, ts int64, ext, payload, meta []byte) []byte {
	b := make([]byte, headerLen, headerLen+len(ext)+len(payload)+len(meta))
	b[0] = 1
	b[1] = typ
	binary.LittleEndian.PutUint64(b[2:10], uint64(ts))
	binary.LittleEndian.PutUint16(b[10:12], uint16(len(meta)))
	binary.LittleEndian.PutUint32(b[12:16], uint32(len(ext)+len(payload)+len(meta)))
	b = append(b, ext...)
	b = append(b, payload...)
	return append(b, meta...)
}

func wireVideo(ts int64, h VideoHeader, payload, meta []byte) []byte {
	ext := make([]byte, videoHeaderLen)
	le := binary.LittleEndian
	le.PutUint32(ext[0:], h.Codec)
	le.PutUint32(ext[4:], uint32(h.Width))
	le.PutUint32(ext[8:], uint32(h.Height))
	le.PutUint32(ext[12:], uint32(h.FrameRateN))
	le.PutUint32(ext[16:], uint32(h.FrameRateD))
	le.PutUint32(ext[20:], math.Float32bits(h.Aspect))
	le.PutUint32(ext[24:], uint32(h.Flags))
	le.PutUint32(ext[28:], uint32(h.ColorSpace))
	return wireFrame(frameVideo, ts, ext, payload, meta)
}

func wireAudio(ts int64, h AudioHeader, payload []byte) []byte {
	ext := make([]byte, audioHeaderLen)
	le := binary.LittleEndian
	le.PutUint32(ext[0:], h.Codec)
	le.PutUint32(ext[4:], uint32(h.SampleRate))
	le.PutUint32(ext[8:], uint32(h.SamplesPerChannel))
	le.PutUint32(ext[12:], uint32(h.Channels))
	le.PutUint32(ext[16:], h.ActiveChannels)
	return wireFrame(frameAudio, ts, ext, payload, nil)
}

func TestReadFrameRoundTrip(t *testing.T) {
	vh := VideoHeader{Codec: codecVMX1, Width: 1920, Height: 1080, FrameRateN: 60000, FrameRateD: 1001,
		Aspect: 16.0 / 9, Flags: videoAlpha, ColorSpace: 709}
	ah := AudioHeader{Codec: codecFPA1, SampleRate: 48000, SamplesPerChannel: 4, Channels: 2, ActiveChannels: 3}
	var stream bytes.Buffer
	stream.Write(wireVideo(1234567, vh, []byte("compressed"), []byte(`<Caption/>`)))
	stream.Write(wireAudio(42, ah, make([]byte, 32)))
	stream.Write(encodeMetadata(msgSubscribeVideo))
	stream.Write(wireFrame(9, 0, nil, []byte("unknown type, skipped"), nil))

	var buf []byte
	f, buf, err := readFrame(&stream, buf)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != frameVideo || f.Timestamp != 1234567 || f.Video != vh {
		t.Fatalf("video frame: got %+v", f)
	}
	if string(f.Data) != "compressed" || string(f.Meta) != `<Caption/>` {
		t.Fatalf("video payload %q, metadata %q", f.Data, f.Meta)
	}
	f, buf, err = readFrame(&stream, buf)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != frameAudio || f.Audio != ah || len(f.Data) != 32 || len(f.Meta) != 0 {
		t.Fatalf("audio frame: got %+v", f)
	}
	f, buf, err = readFrame(&stream, buf)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != frameMetadata || metadataText(f.Data) != msgSubscribeVideo {
		t.Fatalf("metadata frame: got type %d %q", f.Type, f.Data)
	}
	f, buf, err = readFrame(&stream, buf)
	if err != nil || f.Type != 9 {
		t.Fatalf("unknown frame type: %v %+v", err, f)
	}
	if _, _, err = readFrame(&stream, buf); err != io.EOF {
		t.Fatalf("end of stream: got %v, want EOF", err)
	}
}

func TestReadFrameRefusesALostStream(t *testing.T) {
	bad := wireVideo(0, VideoHeader{Codec: codecVMX1}, nil, nil)
	bad[0] = 2
	if _, _, err := readFrame(bytes.NewReader(bad), nil); err != errBadVersion {
		t.Fatalf("version 2: got %v", err)
	}
	// A length shorter than the extended header it must contain.
	short := wireFrame(frameVideo, 0, make([]byte, 8), nil, nil)
	if _, _, err := readFrame(bytes.NewReader(short), nil); err == nil {
		t.Fatal("a video frame without room for its header was accepted")
	}
	// A length past libomtnet's own maximum.
	huge := wireFrame(frameVideo, 0, make([]byte, videoHeaderLen), nil, nil)
	binary.LittleEndian.PutUint32(huge[12:16], maxVideoData*2)
	if _, _, err := readFrame(bytes.NewReader(huge), nil); err == nil || !strings.Contains(err.Error(), "out of step") {
		t.Fatalf("oversized frame: got %v", err)
	}
}

// The control messages are compared as strings at the sender, so they must be
// libomtnet's bytes exactly — typo included.
func TestControlMessagesAreLibomtnetsBytes(t *testing.T) {
	for got, want := range map[string]string{
		msgSubscribeVideo:           `<OMTSubscribe Video="true" />`,
		msgSubscribeAudio:           `<OMTSubscribe Audio="true" />`,
		msgSubscribeMetadata:        `<OMTSubscribe Metadata="true" />`,
		msgTallyProgram:             `<OMTTally Preview="false" Program=="true" />`,
		msgTallyNone:                `<OMTTally Preview="false" Program=="false" />`,
		qualityMessage(QualityLow):  `<OMTSettings Quality="Low" />`,
		qualityMessage(QualityHigh): `<OMTSettings Quality="High" />`,
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	m := encodeMetadata(msgSubscribeVideo)
	if len(m) != headerLen+len(msgSubscribeVideo) || m[len(m)-1] == 0 {
		t.Fatal("a control message must carry no NUL terminator")
	}
	if binary.LittleEndian.Uint32(m[12:16]) != uint32(len(msgSubscribeVideo)) || m[10] != 0 || m[11] != 0 {
		t.Fatal("control message length fields are wrong")
	}
}

func TestSenderInfo(t *testing.T) {
	got := parseSenderInfo(`<OMTInfo ProductName="vMix &amp; friends" Manufacturer="StudioCoast" Version="29.0" />` + "\x00")
	want := SenderInfo{Product: "vMix & friends", Manufacturer: "StudioCoast", Version: "29.0"}
	if got != want {
		t.Fatalf("got %+v", got)
	}
	if metadataText([]byte("abc\x00\x00")) != "abc" {
		t.Fatal("trailing NULs not trimmed")
	}
	if xmlAttr(`<OMTRedirect NewAddress="" />`, "NewAddress") != "" || xmlAttr(`<x/>`, "NewAddress") != "" {
		t.Fatal("missing or empty attribute")
	}
}
