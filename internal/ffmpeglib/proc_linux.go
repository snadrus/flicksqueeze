package ffmpeglib

import (
	"os/exec"
	"syscall"
)

func configureCmd(cmd *exec.Cmd, bin string, args []string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGTERM,
	}
	if nicePath, err := exec.LookPath("nice"); err == nil {
		cmd.Path = nicePath
		cmd.Args = append([]string{"nice", "-n", "19", "ionice", "-c", "3", bin}, args...)
	}
}
