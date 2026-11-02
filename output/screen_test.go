package main

import (
	"os"
	"path/filepath"
	"testing"
)

// edid is a minimal EDID 1.3 base block: the header, a 1080p60 detailed
// timing first, and a monitor name. Checksums are not read, so not set.
func edid(clock10k int, name string) []byte {
	b := make([]byte, 128)
	copy(b, "\x00\xff\xff\xff\xff\xff\xff\x00")
	d := b[54:72]
	d[0], d[1] = byte(clock10k), byte(clock10k>>8)
	ha, hb, va, vb := 1920, 280, 1080, 45
	d[2], d[3], d[4] = byte(ha), byte(hb), byte(ha>>8<<4|hb>>8)
	d[5], d[6], d[7] = byte(va), byte(vb), byte(va>>8<<4|vb>>8)
	d[8], d[9], d[10], d[11] = 88, 44, 4<<4|5, 0
	d[17] = 0x18 | 0x04 | 0x02 // digital separate, +v +h
	n := b[72:90]
	n[3] = 0xfc
	copy(n[5:], name+"\n")
	return b
}

func fakeSys(t *testing.T, status string, e []byte) SysPaths {
	t.Helper()
	root := t.TempDir()
	c := filepath.Join(root, "class", "drm", "card0-HDMI-A-1")
	os.MkdirAll(c, 0o755)
	os.WriteFile(filepath.Join(c, "status"), []byte(status+"\n"), 0o644)
	os.WriteFile(filepath.Join(c, "modes"), []byte("1920x1080\n1920x1080\n1280x720\n720x576\n"), 0o644)
	os.WriteFile(filepath.Join(c, "edid"), e, 0o644)
	sum := filepath.Join(root, "summary")
	os.WriteFile(sum, []byte("VOP [ff370000.vop]: ACTIVE\n    Connector: HDMI-A\n\tbus_format[100a]: UYVY8_1X16\n\toverlay_mode[0] output_mode[f] color_space[0]\n    Display mode: 1920x1080p60\n"), 0o644)
	return SysPaths{DRM: filepath.Join(root, "class", "drm"), Summary: sum}
}

func TestReadScreen(t *testing.T) {
	s := ReadScreen(fakeSys(t, "connected", edid(14850, "PROJECTOR 1")))
	if !s.Connected || s.Connector != "HDMI-A-1" || s.Name != "PROJECTOR 1" || s.Current != "1920x1080p60" {
		t.Fatalf("%+v", s)
	}
	if len(s.Modes) != 3 || s.Modes[0] != "1920x1080" {
		t.Fatalf("modes %v", s.Modes)
	}
	want, _ := StandardByID("1080p60")
	n := s.Native
	if n == nil || n.ClockKHz != 148500 || n.HSyncStart != want.Mode.HSyncStart || n.HSyncEnd != want.Mode.HSyncEnd ||
		n.HTotal != want.Mode.HTotal || n.VSyncStart != want.Mode.VSyncStart || n.VSyncEnd != want.Mode.VSyncEnd ||
		n.VTotal != want.Mode.VTotal || n.Flags != want.Mode.Flags || !s.NativeOK {
		t.Fatalf("native %+v (ok %v), want %+v", n, s.NativeOK, want.Mode)
	}
}

func TestNativeOnAClockThePLAYCannotMake(t *testing.T) {
	s := ReadScreen(fakeSys(t, "connected", edid(15400, "WUXGA")))
	if s.Native == nil || s.NativeOK {
		t.Fatalf("a 154 MHz native timing passed: %+v", s.Native)
	}
}

func TestNoScreen(t *testing.T) {
	s := ReadScreen(fakeSys(t, "disconnected", nil))
	if s.Connected || s.Native != nil {
		t.Fatalf("%+v", s)
	}
	if s := ReadScreen(SysPaths{DRM: t.TempDir(), Summary: "/nonexistent"}); s.Connected || s.Connector != "" {
		t.Fatalf("%+v", s)
	}
}
