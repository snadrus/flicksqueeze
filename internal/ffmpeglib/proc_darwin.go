package ffmpeglib

import "os/exec"

func configureCmd(cmd *exec.Cmd, bin string, args []string) {
	if nicePath, err := exec.LookPath("nice"); err == nil {
		cmd.Path = nicePath
		cmd.Args = append([]string{"nice", "-n", "19", bin}, args...)
	}
}
