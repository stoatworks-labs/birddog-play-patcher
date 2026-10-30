package main

// The OMT wire format, receive side.
//
// OMT 1.0 is a TCP stream of frames, little-endian throughout (libomtnet's
// PROTOCOL.md; the code here was checked against libomtnet 7d29031, which is the
// reference implementation every sender uses):
//
//	header    16 bytes  version(1)=1, type(1), timestamp(8, 100 ns units),
//	                    metadata length(2), data length(4)
//	extended  32 bytes for video, 24 for audio, none for metadata
//	data      the payload, then `metadata length` bytes of per-frame XML
//
// The data length counts the extended header, the payload and the trailing
// metadata, but not the 16-byte header. Control messages are metadata frames
// whose data is a fixed XML string; both ends match them as strings, not as XML,
// so they must be sent byte for byte as libomtnet spells them — including the
// `Program==` typo the protocol keeps for compatibility, and with no NUL on the
// end (libomtnet sends none, and compares the whole buffer).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	frameMetadata = 1
	frameVideo    = 2
	frameAudio    = 4

	headerLen      = 16
	videoHeaderLen = 32
	audioHeaderLen = 24

	// maxVideoData is libomtnet's VIDEO_MAX_SIZE: a sender drops anything bigger,
	// so a larger length on the wire is a desynchronised stream, not a frame.
	maxVideoData = 10 << 20
	maxAudioData = 1 << 20
	maxMetaData  = 64 << 10
)

// FourCCs as they appear on the wire (little-endian ASCII).
var (
	codecVMX1 = fourCC("VMX1")
	codecFPA1 = fourCC("FPA1")
)

func fourCC(s string) uint32 { return binary.LittleEndian.Uint32([]byte(s)) }

func fourCCString(v uint32) string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, string(b))
}

// Video flags.
const (
	videoInterlaced   = 1
	videoAlpha        = 2
	videoPremultiplie = 4
	videoPreview      = 8
	videoHighBitDepth = 16
)

// Control messages, exactly as libomtnet's OMTMetadataConstants spells them.
const (
	msgSubscribeVideo    = `<OMTSubscribe Video="true" />`
	msgSubscribeAudio    = `<OMTSubscribe Audio="true" />`
	msgSubscribeMetadata = `<OMTSubscribe Metadata="true" />`
	msgTallyNone         = `<OMTTally Preview="false" Program=="false" />`
	msgTallyProgram      = `<OMTTally Preview="false" Program=="true" />`
	msgQualityPrefix     = `<OMTSettings Quality=`
	msgSenderInfoPrefix  = `<OMTInfo`
	msgRedirectPrefix    = `<OMTRedirect`
)

// Quality is the receiver's suggestion to the sender. The sender encodes at the
// highest quality any of its receivers asks for, so a PLAY asking for Low only
// helps when nobody else is watching.
type Quality string

const (
	QualityDefault Quality = "Default"
	QualityLow     Quality = "Low"
	QualityMedium  Quality = "Medium"
	QualityHigh    Quality = "High"
)

func (q Quality) Valid() bool {
	switch q {
	case QualityDefault, QualityLow, QualityMedium, QualityHigh:
		return true
	}
	return false
}

func qualityMessage(q Quality) string {
	if !q.Valid() {
		q = QualityDefault
	}
	return msgQualityPrefix + `"` + string(q) + `" />`
}

// VideoHeader is the 32-byte extended header of a video frame.
type VideoHeader struct {
	Codec      uint32
	Width      int
	Height     int
	FrameRateN int
	FrameRateD int
	Aspect     float32
	Flags      int
	ColorSpace int
}

// AudioHeader is the 24-byte extended header of an audio frame.
type AudioHeader struct {
	Codec             uint32
	SampleRate        int
	SamplesPerChannel int
	Channels          int
	ActiveChannels    uint32
}

// Frame is one frame off the wire. Data is the payload without the extended
// header or the trailing per-frame metadata; it aliases the reader's buffer and
// is only valid until the next read on that buffer.
type Frame struct {
	Type      int
	Timestamp int64
	Video     VideoHeader
	Audio     AudioHeader
	Data      []byte
	Meta      []byte
}

var errBadVersion = errors.New("omt: not an OMT 1.0 frame (the stream is out of step or this is not an OMT sender)")

// readFrame reads one frame into buf, growing it as needed, and returns the
// frame and the (possibly reallocated) buffer.
func readFrame(r io.Reader, buf []byte) (Frame, []byte, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, buf, err
	}
	if hdr[0] != 1 {
		return Frame{}, buf, errBadVersion
	}
	f := Frame{
		Type:      int(hdr[1]),
		Timestamp: int64(binary.LittleEndian.Uint64(hdr[2:10])),
	}
	metaLen := int(binary.LittleEndian.Uint16(hdr[10:12]))
	dataLen := int(int32(binary.LittleEndian.Uint32(hdr[12:16])))

	ext, limit := 0, maxMetaData
	switch f.Type {
	case frameVideo:
		ext, limit = videoHeaderLen, maxVideoData+videoHeaderLen+maxMetaData
	case frameAudio:
		ext, limit = audioHeaderLen, maxAudioData+audioHeaderLen+maxMetaData
	case frameMetadata:
	default:
		// An unknown type with a sane length can be skipped; anything else
		// means we have lost the framing.
	}
	if dataLen < ext+metaLen || dataLen > limit {
		return Frame{}, buf, fmt.Errorf("omt: frame type %d claims %d bytes (extended %d, metadata %d) — stream out of step", f.Type, dataLen, ext, metaLen)
	}
	if cap(buf) < dataLen {
		buf = make([]byte, dataLen, dataLen+dataLen/4)
	}
	buf = buf[:dataLen]
	if _, err := io.ReadFull(r, buf); err != nil {
		return Frame{}, buf, err
	}

	switch f.Type {
	case frameVideo:
		f.Video = VideoHeader{
			Codec:      binary.LittleEndian.Uint32(buf[0:4]),
			Width:      int(int32(binary.LittleEndian.Uint32(buf[4:8]))),
			Height:     int(int32(binary.LittleEndian.Uint32(buf[8:12]))),
			FrameRateN: int(int32(binary.LittleEndian.Uint32(buf[12:16]))),
			FrameRateD: int(int32(binary.LittleEndian.Uint32(buf[16:20]))),
			Aspect:     math.Float32frombits(binary.LittleEndian.Uint32(buf[20:24])),
			Flags:      int(int32(binary.LittleEndian.Uint32(buf[24:28]))),
			ColorSpace: int(int32(binary.LittleEndian.Uint32(buf[28:32]))),
		}
	case frameAudio:
		f.Audio = AudioHeader{
			Codec:             binary.LittleEndian.Uint32(buf[0:4]),
			SampleRate:        int(int32(binary.LittleEndian.Uint32(buf[4:8]))),
			SamplesPerChannel: int(int32(binary.LittleEndian.Uint32(buf[8:12]))),
			Channels:          int(int32(binary.LittleEndian.Uint32(buf[12:16]))),
			ActiveChannels:    binary.LittleEndian.Uint32(buf[16:20]),
		}
	}
	f.Data = buf[ext : dataLen-metaLen]
	f.Meta = buf[dataLen-metaLen:]
	return f, buf, nil
}

// encodeMetadata builds a metadata frame carrying xml.
func encodeMetadata(xml string) []byte {
	b := make([]byte, headerLen+len(xml))
	b[0] = 1
	b[1] = frameMetadata
	// Timestamp 0 and metadata length 0, as libomtnet's receiver sends them.
	binary.LittleEndian.PutUint32(b[12:16], uint32(len(xml)))
	copy(b[headerLen:], xml)
	return b
}

// metadataText returns a metadata payload as a string, without the NUL some
// senders put on the end.
func metadataText(b []byte) string {
	return strings.TrimRight(string(b), "\x00")
}

// xmlAttr pulls attr="value" out of a one-element control message. Control
// messages are flat, single elements, so this is enough, and it avoids
// pretending to parse arbitrary XML from the network.
func xmlAttr(s, attr string) string {
	key := attr + `="`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return xmlUnescape(rest[:j])
}

func xmlUnescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return r.Replace(s)
}

// SenderInfo is what a sender says about itself in <OMTInfo ... />.
type SenderInfo struct {
	Product      string `json:"product,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Version      string `json:"version,omitempty"`
}

func parseSenderInfo(s string) SenderInfo {
	return SenderInfo{
		Product:      xmlAttr(s, "ProductName"),
		Manufacturer: xmlAttr(s, "Manufacturer"),
		Version:      xmlAttr(s, "Version"),
	}
}
