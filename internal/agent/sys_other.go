//go:build !linux

package agent

import (
	"io/fs"
	"syscall"
)

func statExtra(fi fs.FileInfo) (ctimeNs int64, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec, st.Ino
	}
	return 0, 0
}

func kernelRelease() string { return "" }

// procStart is only exact on Linux; elsewhere fall back to liveness by PID.
func procStart(pid int) (uint64, bool) {
	if pid <= 0 {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.EPERM {
		return 0, false
	}
	return 1, true
}
