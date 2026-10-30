//go:build !linux

package main

import "os"

// setPipeSize is Linux-only (F_SETPIPE_SZ); elsewhere pipes keep their size.
func setPipeSize(*os.File, int) {}
