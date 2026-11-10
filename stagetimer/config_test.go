package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	good := DefaultConfig()
	good.RoomID, good.APIKey = "ABC123_x-y", "sk_9f8e7d6c5b4a"
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Config){
		"room with a slash": func(c *Config) { c.RoomID = "a/b" },
		"room with a space": func(c *Config) { c.RoomID = "a b" },
		"short key":         func(c *Config) { c.APIKey = "short" },
		"key with a space":  func(c *Config) { c.APIKey = "abc defghij" },
		"ftp base":          func(c *Config) { c.APIBase = "ftp://x" },
		"hostless base":     func(c *Config) { c.APIBase = "https://" },
		"view":              func(c *Config) { c.Display.View = "wall" },
		"mirror":            func(c *Config) { c.Display.Mirror = "x" },
	} {
		c := good
		edit(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestKeyHint(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "…", "abcd": "…", "secret-key-9f3a": "…9f3a"} {
		if got := KeyHint(in); got != want {
			t.Errorf("KeyHint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigStore(t *testing.T) {
	dir := t.TempDir()
	st := &ConfigStore{Path: filepath.Join(dir, "config.json")}
	c, err := st.Load()
	if err != nil || c.Configured() || !c.Resume || c.Display.View != ViewTimer {
		t.Fatalf("first load: %+v %v", c, err)
	}
	if fi, err := os.Stat(st.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("defaults not written 0600: %v %v", fi.Mode(), err)
	}
	c, err = st.Update(func(c *Config) { c.RoomID, c.APIKey = "room1", "key-abcdef-1234" })
	if err != nil || !c.Configured() {
		t.Fatal(err)
	}
	// A bad change is refused and leaves the file as it was.
	if _, err := st.Update(func(c *Config) { c.RoomID = "bad room" }); err == nil {
		t.Fatal("bad room saved")
	}
	c, _ = st.Load()
	if c.RoomID != "room1" {
		t.Fatalf("after a refused update: %q", c.RoomID)
	}
	if fi, _ := os.Stat(st.Path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved %v", fi.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".bdstage-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	// A hand-edited bad view loads as the timer, not an error.
	_ = os.WriteFile(st.Path, []byte(`{"room_id":"r","api_key":"k-12345678","display":{"view":"x"}}`), 0o600)
	if c, err := st.Load(); err != nil || c.Display.View != ViewTimer || !c.Resume {
		t.Fatalf("bad view: %+v %v", c, err)
	}
}

func TestNormalizeRoomID(t *testing.T) {
	for in, want := range map[string]string{
		"  ABCD1234 ":                                   "ABCD1234",
		"https://stagetimer.io/r/ABCD1234/":             "ABCD1234",
		"https://stagetimer.io/r/ABCD1234/controller":   "ABCD1234",
		"https://stagetimer.io/r/ABCD1234/?signature=x": "ABCD1234",
		"stagetimer.io/r/XYZ":                           "XYZ",
		"https://stagetimer.io/pricing/":                "https://stagetimer.io/pricing/",
	} {
		if got := NormalizeRoomID(in); got != want {
			t.Errorf("NormalizeRoomID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResumeAllowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "resume")
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 3; i++ {
		if !resumeAllowed(p, now.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("resume %d refused", i+1)
		}
	}
	if resumeAllowed(p, now.Add(4*time.Minute)) {
		t.Fatal("a fourth resume within ten minutes allowed")
	}
	if !resumeAllowed(p, now.Add(11*time.Minute)) {
		t.Fatal("resume after the window refused")
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "1 ") {
		t.Fatalf("window not restarted: %q", b)
	}
}
