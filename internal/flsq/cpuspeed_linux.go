package flsq

import (
	"os"
	"strconv"
	"strings"
)

// cpuGHz reads the average clock speed from /proc/cpuinfo.
func cpuGHz() float64 {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return baselineGHz
	}
	var total float64
	var count int
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu MHz") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		mhz, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			continue
		}
		total += mhz
		count++
	}
	if count == 0 {
		return baselineGHz
	}
	return (total / float64(count)) / 1000.0
}
