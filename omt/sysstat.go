package main

// CPU and temperature, for the tab.
//
// Whether a PLAY keeps up with a source is the open question of this module
// (the 1080p60 SQ figures are modelled, not measured), so the tab shows the
// numbers that answer it: how much of the box is busy, how much of that is us,
// and how hot the SoC is running. Read from /proc and /sys; off Linux they
// report -1.

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SysStats is one reading.
type SysStats struct {
	CPUBusy    float64 `json:"cpu_busy"`    // % of all cores, 0–100
	CPUProcess float64 `json:"cpu_process"` // bdomt incl. decode threads, % of one core
	TempC      float64 `json:"temp_c"`
}

// sysSampler turns cumulative counters into rates between readings.
type sysSampler struct {
	mu        sync.Mutex
	at        time.Time
	busy, all float64
	proc      float64
	last      SysStats
}

func (s *sysSampler) Read() SysStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.at) < 900*time.Millisecond && !s.at.IsZero() {
		return s.last
	}
	busy, all, ok1 := readProcStat()
	proc, ok2 := readSelfCPU()
	out := SysStats{CPUBusy: -1, CPUProcess: -1, TempC: readTemp()}
	if !s.at.IsZero() {
		if ok1 && all > s.all {
			out.CPUBusy = round1((busy - s.busy) / (all - s.all) * 100)
		}
		if ok2 {
			out.CPUProcess = round1((proc - s.proc) / now.Sub(s.at).Seconds() * 100)
		}
	}
	s.at, s.busy, s.all, s.proc, s.last = now, busy, all, proc, out
	return out
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// readProcStat returns busy and total jiffies across all CPUs.
func readProcStat() (busy, all float64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	var idle float64
	for i, v := range f[1:] {
		n, _ := strconv.ParseFloat(v, 64)
		all += n
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	return all - idle, all, true
}

// readSelfCPU returns this process's user+system CPU seconds.
func readSelfCPU() (float64, bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// The command name is in parentheses and may hold spaces; fields start
	// after the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 13 {
		return 0, false
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / 100, true // USER_HZ is 100 on arm64 Linux
}

func readTemp() float64 {
	b, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return -1
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return -1
	}
	return round1(n / 1000)
}
