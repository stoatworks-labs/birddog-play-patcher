package main

// config.json: the source and how to show it.
//
// Never shipped in the package — bdomt writes the defaults on first start, and
// an existing file survives a reinstall, because it is how the box is set up.
// Written atomically (temp file and rename): /userdata is the PLAY's only
// writable store of any size and a half-written config must not be what a
// power cut leaves behind.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	// Source is the last one chosen, by name and by address.
	Source Source `json:"source"`
	// OnAir records that a source was being shown, so a restart (a power cut,
	// an upgrade) puts it back. Stopping from the tab clears it.
	OnAir bool `json:"on_air"`
	// Resume decides whether OnAir is acted on at start.
	Resume bool `json:"resume"`

	Quality     Quality `json:"quality"`
	Audio       bool    `json:"audio"`
	Tally       bool    `json:"tally"`
	Threads     int     `json:"threads"`
	HoldOnLoss  bool    `json:"hold_on_loss"`
	MatchOutput bool    `json:"match_output"`
}

func DefaultConfig() Config {
	return Config{
		Resume:     true,
		Quality:    QualityDefault,
		Audio:      true,
		Tally:      true,
		Threads:    4,
		HoldOnLoss: true,
	}
}

func (c Config) Settings() Settings {
	return Settings{
		Quality:     c.Quality,
		Audio:       c.Audio,
		Tally:       c.Tally,
		Threads:     c.Threads,
		HoldOnLoss:  c.HoldOnLoss,
		MatchOutput: c.MatchOutput,
	}
}

func (c Config) Validate() error {
	if !c.Quality.Valid() {
		return fmt.Errorf("quality must be Default, Low, Medium or High")
	}
	if c.Threads < 0 || c.Threads > 8 {
		return fmt.Errorf("threads must be 0 (libvmx's choice) to 8")
	}
	if c.Source.Address != "" {
		if _, _, err := parseAddress(c.Source.Address); err != nil {
			return err
		}
	}
	return nil
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
	if !c.Quality.Valid() {
		c.Quality = QualityDefault
	}
	return c, nil
}

func (s *ConfigStore) Save(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(c)
}

// Update loads, applies fn and saves, under one lock.
func (s *ConfigStore) Update(fn func(*Config)) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := DefaultConfig()
	if b, err := os.ReadFile(s.Path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	fn(&c)
	return c, s.saveLocked(c)
}

func (s *ConfigStore) saveLocked(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Path, append(b, '\n'), 0o644)
}

// writeFileAtomic writes via a temp file and rename, so a crash mid-write
// leaves the old file, never a truncated one.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bdomt-*")
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
