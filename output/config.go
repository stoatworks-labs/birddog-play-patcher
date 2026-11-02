package main

// config.json: the HDMI format and the area of interest, as last kept.
//
// Never shipped in the package — bdout writes the defaults on first start, and
// an existing file survives a reinstall, because it is how the box is set up.
// It only ever holds a kept format: one being tried lives in memory and in
// shim.conf until Keep, so a power cut mid-try comes back to the last one that
// worked (runner.go). Written atomically (temp file and rename): /userdata is
// the PLAY's only writable store of any size, and a half-written config must
// not be what a power cut leaves behind.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// FormatKind is how the HDMI format is chosen.
type FormatKind string

const (
	// FormatSource is the PLAY's own behaviour: PPApp picks from the screen's
	// list to match the stream. Nothing is forced.
	FormatSource FormatKind = "source"
	// FormatStandard is one of Standards, by ID.
	FormatStandard FormatKind = "standard"
	// FormatCustom is a size and refresh, fitted onto a clock the PHY makes.
	FormatCustom FormatKind = "custom"
	// FormatTiming is every number given by hand.
	FormatTiming FormatKind = "timing"
)

type Format struct {
	Kind     FormatKind `json:"kind"`
	Standard string     `json:"standard,omitempty"`
	Width    int        `json:"width,omitempty"`
	Height   int        `json:"height,omitempty"`
	Refresh  float64    `json:"refresh,omitempty"`
	Timing   *Mode      `json:"timing,omitempty"`
}

// Resolve gives the timing a format forces, or nil for FormatSource.
func (f Format) Resolve() (*Mode, error) {
	switch f.Kind {
	case FormatSource, "":
		return nil, nil
	case FormatStandard:
		s, ok := StandardByID(f.Standard)
		if !ok {
			return nil, fmt.Errorf("no standard format called %q", f.Standard)
		}
		m := s.Mode
		return &m, nil
	case FormatCustom:
		m, err := Fit(f.Width, f.Height, f.Refresh)
		if err != nil {
			return nil, err
		}
		return &m, nil
	case FormatTiming:
		if f.Timing == nil {
			return nil, fmt.Errorf("no timing given")
		}
		m := *f.Timing
		if m.Name == "" {
			m.Name = fmt.Sprintf("%dx%d", m.HDisplay, m.VDisplay)
		}
		if err := m.Check(); err != nil {
			return nil, err
		}
		return &m, nil
	}
	return nil, fmt.Errorf("format kind must be source, standard, custom or timing")
}

// Label is the format in a few words, for the log and the tab.
func (f Format) Label() string {
	m, err := f.Resolve()
	switch {
	case err != nil:
		return "an invalid format (" + err.Error() + ")"
	case m == nil:
		return "follow the source"
	case f.Kind == FormatStandard:
		if s, ok := StandardByID(f.Standard); ok {
			return s.Label
		}
	}
	return fmt.Sprintf("%dx%d at %s Hz", m.HDisplay, m.VDisplay, hz(m.Refresh()))
}

// hz prints a refresh the way people say it: 60, 59.94, 23.976.
func hz(v float64) string {
	for _, d := range []int{0, 2, 3} {
		p := math.Pow(10, float64(d))
		if math.Abs(math.Round(v*p)/p-v) < 0.0005 {
			return fmt.Sprintf("%.*f", d, v)
		}
	}
	return fmt.Sprintf("%.3f", v)
}

// Unit is a whole picture or screen, in the millionths shim.conf counts in.
const Unit = 1000000

// Rect is part of the picture or the screen, in millionths of it.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

var wholeRect = Rect{0, 0, Unit, Unit}

func (r Rect) Valid() bool {
	return r.X >= 0 && r.Y >= 0 && r.W > 0 && r.H > 0 && r.X <= Unit && r.Y <= Unit &&
		r.W <= Unit-r.X && r.H <= Unit-r.Y
}

func (r Rect) Whole() bool { return r == wholeRect }

// AOI is the area of interest: which part of the picture is shown, and where
// on the screen.
type AOI struct {
	On bool `json:"on"`
	// Src is the part of the source picture shown, Dst where it goes on the
	// screen, both in millionths, so the same setting holds whatever size the
	// stream or the format turns out to be.
	Src Rect `json:"src"`
	Dst Rect `json:"dst"`
	// KeepAspect letterboxes or pillarboxes Src inside Dst; off, it stretches.
	KeepAspect bool `json:"keep_aspect"`
	// Canvas is the picture size the area was drawn against, kept only so the
	// tab can show the numbers in pixels again. The shim never sees it.
	CanvasW int `json:"canvas_w,omitempty"`
	CanvasH int `json:"canvas_h,omitempty"`
}

func (a AOI) Validate() error {
	if !a.Src.Valid() {
		return fmt.Errorf("the part of the picture to show is empty or runs off the picture")
	}
	if !a.Dst.Valid() {
		return fmt.Errorf("the place on the screen is empty or runs off the screen")
	}
	if a.CanvasW < 0 || a.CanvasH < 0 || a.CanvasW > 16384 || a.CanvasH > 16384 {
		return fmt.Errorf("canvas size out of range")
	}
	return nil
}

// Active reports whether the AOI changes anything: on, and not the whole
// picture stretched over the whole screen.
func (a AOI) Active() bool {
	return a.On && !(a.Src.Whole() && a.Dst.Whole() && !a.KeepAspect)
}

type Config struct {
	Format Format `json:"format"`
	AOI    AOI    `json:"aoi"`
}

func DefaultConfig() Config {
	return Config{
		Format: Format{Kind: FormatSource},
		AOI:    AOI{Src: wholeRect, Dst: wholeRect},
	}
}

func (c Config) Validate() error {
	if _, err := c.Format.Resolve(); err != nil {
		return err
	}
	return c.AOI.Validate()
}

// ConfigStore loads and saves config.json.
type ConfigStore struct {
	Path string
	mu   sync.Mutex
}

// Load returns the stored config, writing the defaults if there is none. A
// file that does not parse or does not validate gives the defaults and an
// error: the defaults are the PLAY as BirdDog ships it, which is never dark.
func (s *ConfigStore) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		c := DefaultConfig()
		return c, s.saveLocked(c)
	}
	if err != nil {
		return DefaultConfig(), err
	}
	c := DefaultConfig()
	if err := json.Unmarshal(b, &c); err != nil {
		return DefaultConfig(), fmt.Errorf("%s: %v", s.Path, err)
	}
	if err := c.Validate(); err != nil {
		return DefaultConfig(), fmt.Errorf("%s: %v", s.Path, err)
	}
	return c, nil
}

func (s *ConfigStore) Save(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(c)
}

func (s *ConfigStore) saveLocked(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Path, append(b, '\n'), 0o644)
}

// writeFileAtomic writes via a temp file and rename, so a crash mid-write
// leaves the old file, never a truncated one — and the shim, which looks at
// shim.conf twice a second, never reads half of one.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bdout-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}
