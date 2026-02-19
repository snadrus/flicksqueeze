package ffmpeglib

import (
	"os/exec"
	"syscall"
)

// configureCmd sets Windows-specific process attributes.
// CREATE_NEW_PROCESS_GROUP + IDLE_PRIORITY_CLASS gives lowest CPU priority.
const (
	createNewProcessGroup = 0x00000200
	idlePriorityClass     = 0x00000040
)

func configureCmd(cmd *exec.Cmd, bin string, args []string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | idlePriorityClass,
	}
}
