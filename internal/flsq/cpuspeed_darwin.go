package flsq

import (
	"os/exec"
	"strconv"
	"strings"
)

// cpuGHz reads the CPU frequency via sysctl on macOS.
func cpuGHz() float64 {
	out, err := exec.Command("sysctl", "-n", "hw.cpufrequency_max").Output()
	if err != nil {
		return baselineGHz
	}
	hz, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || hz <= 0 {
		return baselineGHz
	}
	return hz / 1e9
}
