//go:build linux && !dot && !spot && !panel

package display

import (
	"os"
	"strconv"
	"strings"
)

// childCPU is the processor time, in seconds, the processes this one started have run so far
// (/proc/<pid>/stat's utime and stime, in hundredths).
func childCPU() float64 {
	var total float64
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		b, err := os.ReadFile("/proc/" + d.Name() + "/stat")
		if err != nil {
			continue
		}
		_, rest, ok := strings.Cut(string(b), ") ")
		f := strings.Fields(rest)
		if !ok || len(f) < 13 {
			continue
		}
		if ppid, _ := strconv.Atoi(f[1]); ppid != os.Getpid() {
			continue
		}
		u, _ := strconv.ParseFloat(f[11], 64)
		s, _ := strconv.ParseFloat(f[12], 64)
		total += (u + s) / 100
	}
	return total
}
