package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigStore(t *testing.T) {
	s := &ConfigStore{Path: filepath.Join(t.TempDir(), "sub", "config.json")}
	c, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c != DefaultConfig() {
		t.Fatalf("first load: got %+v", c)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatal("defaults were not written on first load")
	}
	if _, err := s.Update(func(c *Config) {
		c.Source = Source{Name: "PC (Out)", Address: "omt://10.0.0.2:6400"}
		c.OnAir = true
		c.Threads = 2
	}); err != nil {
		t.Fatal(err)
	}
	c, err = s.Load()
	if err != nil || !c.OnAir || c.Threads != 2 || c.Source.Name != "PC (Out)" || !c.Resume {
		t.Fatalf("after update: %+v %v", c, err)
	}

	// A file from an older version, missing fields, keeps the defaults for them.
	if err := os.WriteFile(s.Path, []byte(`{"source":{"address":"10.0.0.5"},"quality":"Bogus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = s.Load()
	if err != nil || c.Quality != QualityDefault || !c.Audio || c.Threads != 4 || c.Source.Address != "10.0.0.5" {
		t.Fatalf("old file: %+v %v", c, err)
	}
	// A broken file is reported and the defaults used.
	_ = os.WriteFile(s.Path, []byte(`{`), 0o644)
	if c, err = s.Load(); err == nil || c != DefaultConfig() {
		t.Fatalf("broken file: %+v %v", c, err)
	}
}

func TestConfigValidate(t *testing.T) {
	c := DefaultConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"quality": func(c *Config) { c.Quality = "Ultra" },
		"threads": func(c *Config) { c.Threads = 9 },
		"address": func(c *Config) { c.Source.Address = "http://x" },
	} {
		c := DefaultConfig()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: invalid config passed", name)
		}
	}
}

func TestResumeIsRateLimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume")
	now := timeAt(1_800_000_000)
	for i := 0; i < 3; i++ {
		if !resumeAllowed(path, now) {
			t.Fatalf("resume %d refused", i+1)
		}
	}
	if resumeAllowed(path, now.Add(5*minute)) {
		t.Fatal("a fourth resume inside ten minutes was allowed")
	}
	if !resumeAllowed(path, now.Add(11*minute)) {
		t.Fatal("resume still refused after ten minutes")
	}
}

const minute = time.Minute

func timeAt(unix int64) time.Time { return time.Unix(unix, 0) }
