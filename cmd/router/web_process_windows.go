//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func configureDetachedProcess(_ *exec.Cmd) {}

func signalWebProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid Router process pid %d", pid)
	}
	process, err := exec.Command("taskkill", "/PID", fmt.Sprint(pid), "/T").CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill: %w (%s)", err, strings.TrimSpace(string(process)))
	}
	return nil
}
