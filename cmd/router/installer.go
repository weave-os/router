package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var installerVerbs = map[string]struct{}{
	"disable-routing": {},
	"install":         {},
	"models":          {},
	"off":             {},
	"on":              {},
	"status":          {},
	"uninstall":       {},
	"update":          {},
}

var installerFlags = map[string]struct{}{
	"--base-url":        {},
	"--claude":          {},
	"--codex":           {},
	"--dir":             {},
	"--json":            {},
	"--local":           {},
	"--lsp":             {},
	"--non-interactive": {},
	"--opencode":        {},
	"--pi":              {},
	"--quiet":           {},
	"--rotate-key":      {},
	"--scope":           {},
	"--uninstall":       {},
}

func isInstallerInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if _, ok := installerVerbs[args[0]]; ok {
		return true
	}
	if _, ok := installerFlags[args[0]]; ok {
		return true
	}
	return args[0] == "--off" || args[0] == "--on" || args[0] == "--status" || args[0] == "--update" || args[0] == "--models"
}

func runInstaller(args []string) error {
	if len(args) > 0 && args[0] == "install" {
		args = args[1:]
	}
	script, err := findInstallerScript()
	if err != nil {
		return err
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return errors.New("bash is required to run the Router installer")
	}
	cmd := exec.Command(bash, append([]string{script}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run Router installer: %w", err)
	}
	return nil
}

func findInstallerScript() (string, error) {
	candidates := make([]string, 0, 5)
	if configured := os.Getenv("ROUTER_INSTALLER_DIR"); configured != "" {
		candidates = append(candidates, filepath.Join(configured, "install.sh"))
	}
	if defaultInstallerDir != "" {
		candidates = append(candidates, filepath.Join(defaultInstallerDir, "install.sh"))
	}
	if workingDir, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(workingDir, "install", "install.sh"))
	}
	if executable, err := os.Executable(); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		executableDir := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(executableDir, "install", "install.sh"))
		candidates = append(candidates, filepath.Join(executableDir, "..", "install", "install.sh"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			absolute, err := filepath.Abs(candidate)
			if err == nil {
				return absolute, nil
			}
		}
	}
	return "", errors.New("Router installer resources not found; reinstall Router or set ROUTER_INSTALLER_DIR")
}
