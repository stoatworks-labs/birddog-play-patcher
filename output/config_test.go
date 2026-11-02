package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatResolve(t *testing.T) {
	if m, err := (Format{Kind: FormatSource}).Resolve(); m != nil || err != nil {
		t.Fatal("following the source forces nothing")
	}
	if m, err := (Format{Kind: FormatStandard, Standard: "1080p50"}).Resolve(); err != nil || m.ClockKHz != 148500 {
		t.Fatal(m, err)
	}
	if m, err := (Format{Kind: FormatCustom, Width: 3840, Height: 1080, Refresh: 60}).Resolve(); err != nil || m.HDisplay != 3840 {
		t.Fatal(m, err)
	}
	std, _ := StandardByID("720p60")
	tm := std.Mode
	tm.Name = ""
	if m, err := (Format{Kind: FormatTiming, Timing: &tm}).Resolve(); err != nil || m.Name != "1280x720" {
		t.Fatal(m, err)
	}
	for _, bad := range []Format{
		{Kind: FormatStandard, Standard: "1080p61"},
		{Kind: FormatCustom, Width: 1921, Height: 1080, Refresh: 60},
		{Kind: FormatTiming},
		{Kind: "magic"},
	} {
		if _, err := bad.Resolve(); err == nil {
			t.Errorf("%+v resolved", bad)
		}
	}
}

func TestFormatLabel(t *testing.T) {
	for f, want := range map[*Format]string{
		{Kind: FormatSource}:                                           "follow the source",
		{Kind: FormatStandard, Standard: "1080p59.94"}:                 "1080p59.94",
		{Kind: FormatCustom, Width: 1920, Height: 1200, Refresh: 60}:   "1920x1200 at 60.013 Hz",
		{Kind: FormatStandard, Standard: "nope"}:                       "an invalid format",
		{Kind: FormatCustom, Width: 1280, Height: 720, Refresh: 59.94}: "1280x720 at 59.94",
	} {
		if got := f.Label(); !strings.HasPrefix(got, want) {
			t.Errorf("%+v: %q, want %q", *f, got, want)
		}
	}
}

func TestRectAndAOI(t *testing.T) {
	if !wholeRect.Valid() || !wholeRect.Whole() {
		t.Fatal("the whole picture")
	}
	for _, r := range []Rect{{0, 0, 0, Unit}, {500001, 0, 500000, Unit}, {-1, 0, 10, 10}, {0, 0, Unit + 1, Unit}} {
		if r.Valid() {
			t.Errorf("%+v valid", r)
		}
	}
	if (AOI{On: true, Src: wholeRect, Dst: wholeRect}).Active() {
		t.Error("the whole picture on the whole screen is not an AOI")
	}
	if (AOI{Src: Rect{0, 0, 10, 10}, Dst: wholeRect}).Active() {
		t.Error("off is off")
	}
}

func TestConfigStore(t *testing.T) {
	dir := t.TempDir()
	s := &ConfigStore{Path: filepath.Join(dir, "config.json")}
	c, err := s.Load()
	if err != nil || c.Format.Kind != FormatSource || !c.AOI.Src.Whole() {
		t.Fatal(c, err)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatal("defaults not written")
	}
	c.Format = Format{Kind: FormatStandard, Standard: "720p50"}
	c.AOI = AOI{On: true, Src: Rect{0, 0, 500000, Unit}, Dst: wholeRect, CanvasW: 3840, CanvasH: 1080}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || got.Format.Standard != "720p50" || got.AOI.CanvasW != 3840 || got.AOI.Src.W != 500000 {
		t.Fatal(got, err)
	}
	// A file that does not validate is the PLAY as shipped, with an error.
	os.WriteFile(s.Path, []byte(`{"format":{"kind":"timing","timing":{"clock_khz":154000}}}`), 0o644)
	got, err = s.Load()
	if err == nil || got.Format.Kind != FormatSource {
		t.Fatal(got, err)
	}
	os.WriteFile(s.Path, []byte(`{not json`), 0o644)
	if got, err = s.Load(); err == nil || got.Format.Kind != FormatSource {
		t.Fatal(got, err)
	}
}
