package tools

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// setProcessGroup ensures context cancellation reaches a command's complete
// process tree rather than leaving background children holding its pipes open.
func setProcessGroup(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

func killProcessGroup(process *os.Process) error {
	if process == nil {
		return os.ErrProcessDone
	}
	if runtime.GOOS == "windows" {
		return process.Kill()
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
