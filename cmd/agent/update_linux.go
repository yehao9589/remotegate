//go:build linux

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func detachUpdateProcess(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func prepareUpdateInstaller(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 5 * time.Second
	command.Cancel = func() error {
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}
