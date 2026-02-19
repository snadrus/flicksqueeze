//go:build !windows

package main

import (
	"os"
	"syscall"
)

var extraSignals = []os.Signal{syscall.SIGTERM}
