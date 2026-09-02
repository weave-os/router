package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var (
	cliVersion          = "dev"
	defaultUIAssetsDir  string
	defaultInstallerDir string
)

func main() {
	if len(os.Args) == 1 {
		runServer()
		return
	}

	if os.Args[1] == "__serve" || os.Args[1] == "serve" {
		runServer()
		return
	}

	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "router: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	if len(args) == 0 {
		runServer()
		return nil
	}

	switch args[0] {
	case "web":
		if err := loadRouterEnv(); err != nil {
			return err
		}
		return runWebCommand(args[1:])
	case "integrations", "clients":
		return runIntegrationsCommand(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("router %s\n", cliVersion)
		return nil
	case "help", "--help", "-h":
		printCLIUsage()
		return nil
	default:
		if isInstallerInvocation(args) {
			return runInstaller(args)
		}
		printCLIUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func loadRouterEnv() error {
	explicit := make(map[string]struct{})
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			explicit[key] = struct{}{}
		}
	}

	for _, path := range []string{".env.development", ".env.local"} {
		file, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("open %s: %w", path, err)
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			key, value, ok := strings.Cut(line, "=")
			key = strings.TrimSpace(key)
			if !ok || key == "" || !isEnvKey(key) {
				continue
			}
			if _, exists := explicit[key]; exists {
				continue
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				if value[0] == '"' {
					if decoded, err := strconv.Unquote(value); err == nil {
						value = decoded
					}
				} else {
					value = value[1 : len(value)-1]
				}
			}
			if err := os.Setenv(key, value); err != nil {
				_ = file.Close()
				return fmt.Errorf("set %s from %s: %w", key, path, err)
			}
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s: %w", path, err)
		}
	}
	return nil
}

func isEnvKey(value string) bool {
	for i, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func printCLIUsage() {
	fmt.Fprintln(os.Stderr, `Usage:
  router integrations [--json]      Show tools configured to use Router
  router install [installer options]  Configure Claude Code, Codex, opencode, or pi
  router --claude                     Configure Claude Code
  router --codex                      Configure OpenAI Codex CLI
  router --opencode                   Configure opencode
  router --pi                         Configure pi + Loom UI
  router --scope project              Write configuration into the current repo
  router --local                      Use the local Router at http://localhost:8088
  router --base-url URL               Use a custom Router URL
  router web start       Start the local Router service in the background
  router web stop        Stop the local Router service
  router web restart     Restart the local Router service
  router web status      Show the local Router service status
  router serve           Run the Router service in the foreground
  router version         Print the Router CLI version

The service reads the same environment variables as cmd/router. Set PORT to
choose its HTTP port; use router web start --foreground for startup logs.`)
}
