package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// confcheck is shim/test/confcheck.c built for this machine: core.c, the
// shim's own parser and AOI maths. Built once per test run; the tests that
// need it skip when there is no C compiler (build.sh and CI always have one).
var (
	confcheckOnce sync.Once
	confcheckPath string
	confcheckErr  error
)

func confcheck(t *testing.T) string {
	t.Helper()
	confcheckOnce.Do(func() {
		cc, err := exec.LookPath("cc")
		if err != nil {
			confcheckErr = err
			return
		}
		dir, err := os.MkdirTemp("", "bdout-confcheck")
		if err != nil {
			confcheckErr = err
			return
		}
		confcheckPath = filepath.Join(dir, "confcheck")
		out, err := exec.Command(cc, "-std=gnu11", "-O2", "-Wall", "-Wextra", "-Werror", "-o", confcheckPath,
			"shim/test/confcheck.c", "shim/core.c").CombinedOutput()
		if err != nil {
			confcheckErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if confcheckErr != nil {
		if strings.Contains(confcheckErr.Error(), "executable file not found") {
			t.Skip("no C compiler: cannot build the shim's parser to check against")
		}
		t.Fatal(confcheckErr)
	}
	return confcheckPath
}

// runConfcheck gives confcheck's one or two lines and whether it accepted.
func runConfcheck(t *testing.T, conf string, sizes ...string) (string, bool) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "shim.conf")
	if err := os.WriteFile(f, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(confcheck(t), append([]string{f}, sizes...)...).Output()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 1 {
		t.Fatalf("confcheck: %v", err)
	}
	return strings.TrimSpace(string(out)), err == nil
}

// Every format the tab can choose, written as bdout writes it, is read by the
// shim's own parser as the same mode, with the same refresh.
func TestShimReadsWhatWeWrite(t *testing.T) {
	aoi := AOI{On: true, Src: Rect{500000, 0, 500000, Unit}, Dst: wholeRect, KeepAspect: true}
	var modes []Mode
	for _, s := range Standards {
		modes = append(modes, s.Mode)
	}
	for _, s := range [][2]int{{1920, 1200}, {2560, 1080}, {1080, 1920}, {1024, 600}} {
		m, err := Fit(s[0], s[1], 60)
		if err != nil {
			t.Fatal(err)
		}
		modes = append(modes, m)
	}
	for _, m := range modes {
		for _, a := range []AOI{{}, aoi} {
			out, ok := runConfcheck(t, RenderShimConf(&m, a))
			want := fmt.Sprintf("ok mode %s %d", m.Name, int(m.Refresh()+0.5))
			if a.Active() {
				want += " aoi 1"
			}
			if !ok || out != want {
				t.Errorf("%s: shim read %q, want %q\n%s", m.Name, out, want, RenderShimConf(&m, a))
			}
		}
	}
	if out, ok := runConfcheck(t, RenderShimConf(nil, AOI{})); !ok || out != "ok" {
		t.Errorf("following the source: shim read %q", out)
	}
}

// The shim refuses what Mode.Check refuses, so nothing gets past one and not
// the other.
func TestShimAndCheckAgree(t *testing.T) {
	base, _ := StandardByID("1080p60")
	for _, f := range []func(*Mode){
		func(m *Mode) { m.ClockKHz = 154000 },
		func(m *Mode) { m.HSyncEnd = m.HTotal + 1 },
		func(m *Mode) { m.VDisplay = 1081 },
		func(m *Mode) { m.Flags = 16 },
		func(m *Mode) { m.ClockKHz = 27000 },
	} {
		m := base.Mode
		f(&m)
		goErr := m.Check()
		out, ok := runConfcheck(t, "version 1\n"+m.ShimLine()+"\n")
		if (goErr == nil) != ok {
			t.Errorf("%s: Go says %v, the shim says %q", m.ShimLine(), goErr, out)
		}
	}
}

func TestAOILineOnlyWhenItDoesSomething(t *testing.T) {
	if strings.Contains(RenderShimConf(nil, AOI{On: true, Src: wholeRect, Dst: wholeRect}), "aoi") {
		t.Error("the whole picture stretched over the whole screen needs no aoi line")
	}
	if strings.Contains(RenderShimConf(nil, AOI{On: false, Src: Rect{0, 0, 500000, 500000}, Dst: wholeRect}), "aoi") {
		t.Error("an AOI switched off wrote an aoi line")
	}
	if !strings.Contains(RenderShimConf(nil, AOI{On: true, Src: wholeRect, Dst: wholeRect, KeepAspect: true}), "aoi") {
		t.Error("keeping the aspect ratio of the whole picture does something")
	}
}

func TestShimModeLine(t *testing.T) {
	m, _ := StandardByID("720p50")
	if got := ShimModeLine(RenderShimConf(&m.Mode, AOI{})); got != m.Mode.ShimLine() {
		t.Fatalf("got %q", got)
	}
	if ShimModeLine(RenderShimConf(nil, AOI{})) != "" {
		t.Fatal("found a mode line in a file without one")
	}
}

func TestReadShimStatus(t *testing.T) {
	f := filepath.Join(t.TempDir(), "status")
	if st := ReadShimStatus(f); st.Present {
		t.Fatal("a missing file is not present")
	}
	os.WriteFile(f, []byte("pid=412\nconf=ok\nmode=forced 1920x1200 at 60 Hz (148352 kHz), in place of the screen's own list\n"+
		"aoi=on\naoi_geometry=1920x1080 at 1920,0 of a 3840x1080 picture -> 1920x1080 at 0,60 of 1920x1200\n"+
		"picture=3840x1080 on 1920x1200\nupdated=1793633808\n"), 0o644)
	st := ReadShimStatus(f)
	if !st.Present || st.PID != 412 || st.Conf != "ok" || st.AOI != "on" || !strings.HasPrefix(st.Mode, "forced 1920x1200") ||
		st.Updated != 1793633808 || !strings.Contains(st.AOIGeom, "0,60") {
		t.Fatalf("%+v", st)
	}
	pw, ph, sw, sh, ok := st.PictureSizes()
	if !ok || pw != 3840 || ph != 1080 || sw != 1920 || sh != 1200 {
		t.Fatal(pw, ph, sw, sh, ok)
	}
}
