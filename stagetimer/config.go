package main

// config.json: the room, the key and how to show it.
//
// Never shipped in the package — bdstage writes the defaults on first start
// and an existing file survives a reinstall. It holds the room's API key, which
// can start, stop and edit that room's timers, so it is 0600, the open status
// endpoint never mentions it, and the tab only ever gets its last four
// characters back. Written atomically: /userdata is the PLAY's only writable
// store of any size and a power cut must not leave half a file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Config struct {
	RoomID  string      `json:"room_id"`
	APIKey  string      `json:"api_key"`
	APIBase string      `json:"api_base,omitempty"` // empty: api.stagetimer.io
	Display ViewOptions `json:"display"`
	// Showing records that the timer was on HDMI, so a restart (a power cut,
	// an upgrade) puts it back. Stopping from the tab clears it.
	Showing bool `json:"showing"`
	// Resume decides whether Showing is acted on at start.
	Resume bool `json:"resume"`
}

func DefaultConfig() Config {
	return Config{Display: DefaultViewOptions(), Resume: true}
}

func (c Config) Credentials() Credentials {
	return Credentials{RoomID: c.RoomID, APIKey: c.APIKey, Base: c.APIBase}
}

func (c Config) Configured() bool { return c.RoomID != "" && c.APIKey != "" }

var (
	roomIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	apiKeyPattern = regexp.MustCompile(`^[\x21-\x7e]{8,512}$`)
)

func (c Config) Validate() error {
	if c.RoomID != "" && !roomIDPattern.MatchString(c.RoomID) {
		return errors.New("the room ID is letters, digits, - and _ — it is the part of the room's address after /r/")
	}
	if c.APIKey != "" && !apiKeyPattern.MatchString(c.APIKey) {
		return errors.New("that does not look like a Stagetimer API key (no spaces, at least 8 characters)")
	}
	if c.APIBase != "" {
		u, err := url.Parse(c.APIBase)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("the API address must be an http(s) URL")
		}
	}
	if !validView(c.Display.View) {
		return fmt.Errorf("view must be one of %s", strings.Join(viewNames, ", "))
	}
	switch c.Display.Mirror {
	case "", "h", "v", "hv":
	default:
		return errors.New(`mirror must be "", "h", "v" or "hv"`)
	}
	return nil
}

// KeyHint is the key as the tab may see it.
func KeyHint(key string) string {
	if key == "" {
		return ""
	}
	r := []rune(key)
	if len(r) <= 4 {
		return "…"
	}
	return "…" + string(r[len(r)-4:])
}

// ConfigStore loads and saves config.json.
type ConfigStore struct {
	Path string
	mu   sync.Mutex
}

// Load returns the stored config, writing the defaults if there is none.
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
	if !validView(c.Display.View) {
		c.Display.View = ViewTimer
	}
	return c, nil
}

// Update loads, applies fn, validates and saves, under one lock. fn's
// changes are discarded if they do not validate.
func (s *ConfigStore) Update(fn func(*Config)) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := DefaultConfig()
	if b, err := os.ReadFile(s.Path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	old := c
	fn(&c)
	if err := c.Validate(); err != nil {
		return old, err
	}
	return c, s.saveLocked(c)
}

func (s *ConfigStore) saveLocked(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Path, append(b, '\n'), 0o600)
}

// writeFileAtomic writes via a temp file and rename, so a crash mid-write
// leaves the old file, never a truncated one.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bdstage-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
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
	return os.Rename(name, path)
}
