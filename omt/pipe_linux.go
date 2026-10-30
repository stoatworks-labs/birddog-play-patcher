package main

import (
	"os"
	"syscall"
)

// setPipeSize asks for a pipe buffer of n bytes (F_SETPIPE_SZ). For video that
// is a frame, so a 3 MB frame crosses in a few writes instead of fifty; for
// audio it is small on purpose, to cap the delay the kernel can add. The kernel
// clamps it to /proc/sys/fs/pipe-max-size (1 MB by default), which is fine.
func setPipeSize(f *os.File, n int) {
	const fSetPipeSz = 1031
	_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), fSetPipeSz, uintptr(n))
}
