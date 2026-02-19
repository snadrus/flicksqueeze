package flsq

import (
	"os/exec"
	"strconv"
	"strings"
)

// cpuGHz reads the CPU max speed from the Windows registry via wmic.
func cpuGHz() float64 {
	out, err := exec.Command("wmic", "cpu", "get", "MaxClockSpeed", "/value").Output()
	if err != nil {
		return baselineGHz
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "MaxClockSpeed=") {
			mhz, err := strconv.ParseFloat(strings.TrimPrefix(line, "MaxClockSpeed="), 64)
			if err != nil || mhz <= 0 {
				return baselineGHz
			}
			return mhz / 1000.0
		}
	}
	return baselineGHz
}
