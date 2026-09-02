//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalWebProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid Router process pid %d", pid)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
