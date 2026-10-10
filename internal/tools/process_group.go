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

func interruptProcessGroup(process *os.Process) error {
	if process == nil {
		return os.ErrProcessDone
	}
	if runtime.GOOS == "windows" {
		return process.Kill()
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// signalProcessGroup delivers sig to the command's whole process group. It
// returns an error when no process received it.
func signalProcessGroup(process *os.Process, sig os.Signal) error {
	if process == nil {
		return os.ErrProcessDone
	}
	if runtime.GOOS == "windows" {
		return process.Kill()
	}
	signal, ok := sig.(syscall.Signal)
	if !ok {
		return process.Signal(sig)
	}
	// ESRCH is returned, not swallowed: callers report whether a signal was
	// actually delivered.
	return syscall.Kill(-process.Pid, signal)
}
