//go:build linux

package agent

import (
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func statExtra(fi fs.FileInfo) (ctimeNs int64, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Sec*1e9 + st.Ctim.Nsec, st.Ino
	}
	return 0, 0
}

func kernelRelease() string {
	var uts syscall.Utsname
	if syscall.Uname(&uts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range uts.Release {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}

// procStart returns the process start time in clock ticks since boot, used
// together with the PID as a process identity that survives PID reuse.
func procStart(pid int) (uint64, bool) {
	if pid <= 0 {
		return 0, false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	// comm (field 2) may contain spaces and parens; fields resume after the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); starttime is field 22.
	if len(f) < 20 {
		return 0, false
	}
	if f[0] == "Z" || f[0] == "X" {
		return 0, false // zombie: effectively gone
	}
	v, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
