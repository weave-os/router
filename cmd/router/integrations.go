package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	integrationStatusOn            = "on"
	integrationStatusOff           = "off"
	integrationStatusNotConfigured = "not_configured"
)

type integrationStatus struct {
	Tool        string   `json:"tool"`
	Scope       string   `json:"scope"`
	Status      string   `json:"status"`
	Installed   bool     `json:"installed"`
	UsingRouter bool     `json:"using_router"`
	ConfigPaths []string `json:"config_paths,omitempty"`
}

type integrationReport struct {
	Directory    string              `json:"directory"`
	Integrations []integrationStatus `json:"integrations"`
}

type integrationTool struct {
	name    string
	command string
}

var integrationTools = []integrationTool{
	{name: "Claude Code", command: "claude"},
	{name: "Codex", command: "codex"},
	{name: "opencode", command: "opencode"},
	{name: "pi", command: "pi"},
}

func runIntegrationsCommand(args []string) error {
	flags := flag.NewFlagSet("router integrations", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	report, err := collectIntegrationReport()
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("encode integrations report: %w", err)
		}
		fmt.Println(string(encoded))
		return nil
	}
	printIntegrationReport(os.Stdout, report)
	return nil
}

func collectIntegrationReport() (integrationReport, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return integrationReport{}, fmt.Errorf("find user home directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return integrationReport{}, fmt.Errorf("find current directory: %w", err)
	}

	report := integrationReport{Directory: cwd}
	for _, tool := range integrationTools {
		installed := commandInstalled(tool.command)
		userStatus, userPaths, userConfigured := scanIntegration(tool.name, home, "user")
		if userConfigured {
			report.Integrations = append(report.Integrations, integrationStatus{
				Tool: tool.name, Scope: "user", Status: userStatus,
				Installed: true, UsingRouter: userStatus == integrationStatusOn,
				ConfigPaths: userPaths,
			})
		} else {
			report.Integrations = append(report.Integrations, integrationStatus{
				Tool: tool.name, Scope: "user", Status: integrationStatusNotConfigured,
				Installed: installed,
			})
		}

		projectStatus, projectPaths, projectConfigured := scanIntegration(tool.name, cwd, "project")
		if projectConfigured {
			report.Integrations = append(report.Integrations, integrationStatus{
				Tool: tool.name, Scope: "project", Status: projectStatus,
				Installed: true, UsingRouter: projectStatus == integrationStatusOn,
				ConfigPaths: projectPaths,
			})
		}
	}
	return report, nil
}

func commandInstalled(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func scanIntegration(tool, baseDir, scope string) (status string, paths []string, configured bool) {
	switch tool {
	case "Claude Code":
		return scanClaudeIntegration(baseDir)
	case "Codex":
		return scanCodexIntegration(baseDir)
	case "opencode":
		return scanOpenCodeIntegration(baseDir, scope)
	case "pi":
		return scanPiIntegration(baseDir, scope)
	default:
		return integrationStatusNotConfigured, nil, false
	}
}

func scanClaudeIntegration(baseDir string) (string, []string, bool) {
	settingsDir := filepath.Join(baseDir, ".claude")
	livePaths := []string{
		filepath.Join(settingsDir, "settings.json"),
		filepath.Join(settingsDir, "settings.local.json"),
	}
	parkedPath := filepath.Join(settingsDir, ".weave-parked.json")
	liveRouter := false
	for _, path := range livePaths {
		config, exists := readJSONConfig(path)
		if exists && claudeConfigUsesRouter(config) {
			liveRouter = true
		}
	}
	parkedConfig, parkedExists := readJSONConfig(parkedPath)
	parkedRouter := parkedExists && claudeConfigUsesRouter(parkedConfig)
	if !liveRouter && !parkedRouter {
		return integrationStatusNotConfigured, nil, false
	}
	paths := existingPaths(append(livePaths, parkedPath))
	if liveRouter {
		return integrationStatusOn, paths, true
	}
	return integrationStatusOff, paths, true
}

func claudeConfigUsesRouter(config map[string]any) bool {
	env, ok := config["env"].(map[string]any)
	if !ok {
		return false
	}
	if value, ok := env["ANTHROPIC_CUSTOM_HEADERS"].(string); ok && strings.Contains(strings.ToLower(value), "x-weave-router-key") {
		return true
	}
	baseURL, ok := env["ANTHROPIC_BASE_URL"].(string)
	if !ok {
		return false
	}
	baseURL = strings.TrimRight(strings.ToLower(strings.TrimSpace(baseURL)), "/")
	return baseURL != "" && baseURL != "https://api.anthropic.com" && baseURL != "http://api.anthropic.com"
}

func scanCodexIntegration(baseDir string) (string, []string, bool) {
	path := filepath.Join(baseDir, ".codex", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		return integrationStatusNotConfigured, nil, false
	}
	text := string(contents)
	if !strings.Contains(text, ">>> weave-router managed (do not edit between markers) >>>") ||
		!strings.Contains(text, "<<< weave-router managed <<<") {
		return integrationStatusNotConfigured, nil, false
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == `model_provider = "weave"` {
			return integrationStatusOn, []string{path}, true
		}
	}
	return integrationStatusOff, []string{path}, true
}

func scanOpenCodeIntegration(baseDir, scope string) (string, []string, bool) {
	path := filepath.Join(baseDir, "opencode.json")
	if scope == "user" {
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			configDir = filepath.Join(baseDir, ".config")
		}
		path = filepath.Join(configDir, "opencode", "opencode.json")
	}
	config, exists := readJSONConfig(path)
	if !exists {
		return integrationStatusNotConfigured, nil, false
	}
	providers, ok := config["provider"].(map[string]any)
	if !ok {
		return integrationStatusNotConfigured, nil, false
	}
	if _, ok := providers["weave"]; !ok {
		return integrationStatusNotConfigured, nil, false
	}
	model, _ := config["model"].(string)
	if strings.HasPrefix(model, "weave/") {
		return integrationStatusOn, []string{path}, true
	}
	return integrationStatusOff, []string{path}, true
}

func scanPiIntegration(baseDir, scope string) (string, []string, bool) {
	piDir := filepath.Join(baseDir, ".pi")
	if scope == "user" {
		piDir = filepath.Join(piDir, "agent")
	}
	modelsPath := filepath.Join(piDir, "models.json")
	settingsPath := filepath.Join(piDir, "settings.json")
	models, _ := readJSONConfig(modelsPath)
	settings, _ := readJSONConfig(settingsPath)
	providers, _ := models["providers"].(map[string]any)
	_, providerConfigured := providers["weave"]
	defaultProvider, _ := settings["defaultProvider"].(string)
	if !providerConfigured && defaultProvider != "weave" {
		return integrationStatusNotConfigured, nil, false
	}
	paths := existingPaths([]string{modelsPath, settingsPath})
	if defaultProvider == "weave" {
		return integrationStatusOn, paths, true
	}
	return integrationStatusOff, paths, true
}

func readJSONConfig(path string) (map[string]any, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var config map[string]any
	if err := json.Unmarshal(contents, &config); err != nil {
		return nil, false
	}
	return config, true
}

func existingPaths(paths []string) []string {
	existing := make([]string, 0, len(paths))
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			existing = append(existing, path)
		}
	}
	return existing
}

func printIntegrationReport(writer io.Writer, report integrationReport) {
	fmt.Fprintf(writer, "Router integrations in %s\n", report.Directory)
	for _, integration := range report.Integrations {
		installed := "no"
		if integration.Installed {
			installed = "yes"
		}
		path := "-"
		if len(integration.ConfigPaths) > 0 {
			prettyPaths := make([]string, len(integration.ConfigPaths))
			for i, configPath := range integration.ConfigPaths {
				prettyPaths[i] = displayPath(configPath)
			}
			path = strings.Join(prettyPaths, ", ")
		}
		fmt.Fprintf(writer, "  %-12s scope=%-7s installed=%-3s router=%-14s %s\n", integration.Tool, integration.Scope, installed, integration.Status, path)
	}
}

func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil {
		if relative, ok := strings.CutPrefix(path, filepath.Clean(home)+string(os.PathSeparator)); ok {
			return filepath.Join("~", relative)
		}
	}
	return path
}
