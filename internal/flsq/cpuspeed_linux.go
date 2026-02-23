package flsq

import (
	"os"
	"strconv"
	"strings"
)

// cpuGHz returns the CPU's max frequency in GHz. It prefers the stable
// sysfs max-freq value over /proc/cpuinfo, which reports the *instantaneous*
// clock and can read as low as 1 GHz on an idle system with power-saving.
func cpuGHz() float64 {
	if ghz := cpuGHzFromSysfs(); ghz > 0 {
		return ghz
	}
	return cpuGHzFromProcCpuinfo()
}

func cpuGHzFromSysfs() float64 {
	data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq")
	if err != nil {
		return 0
	}
	khz, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil || khz <= 0 {
		return 0
	}
	return khz / 1e6
}

func cpuGHzFromProcCpuinfo() float64 {
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
