package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectIntegrationReportFindsActiveAndInactiveScopes(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://router.example.test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("# >>> weave-router managed (do not edit between markers) >>>\n# model_provider = \"weave\"\n# <<< weave-router managed <<<\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), []byte(`{"model":"weave/auto","provider":{"weave":{"npm":"@workweave/router"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".pi", "models.json"), []byte(`{"providers":{"weave":{"baseUrl":"https://router.example.test"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), []byte(`{"defaultProvider":"weave"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	report, err := collectIntegrationReport()
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]string, len(report.Integrations))
	for _, integration := range report.Integrations {
		statuses[integration.Tool+":"+integration.Scope] = integration.Status
	}
	if got := statuses["Claude Code:user"]; got != integrationStatusOn {
		t.Fatalf("Claude Code user status = %q, want %q", got, integrationStatusOn)
	}
	if got := statuses["Codex:user"]; got != integrationStatusOff {
		t.Fatalf("Codex user status = %q, want %q", got, integrationStatusOff)
	}
	if got := statuses["opencode:project"]; got != integrationStatusOn {
		t.Fatalf("opencode project status = %q, want %q", got, integrationStatusOn)
	}
	if got := statuses["pi:project"]; got != integrationStatusOn {
		t.Fatalf("pi project status = %q, want %q", got, integrationStatusOn)
	}
}

func TestScanClaudeIntegrationRecognizesParkedRouterConfig(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, ".weave-parked.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://router.example.test","ANTHROPIC_CUSTOM_HEADERS":"X-Weave-Router-Key: secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, _, configured := scanClaudeIntegration(dir)
	if !configured || status != integrationStatusOff {
		t.Fatalf("scanClaudeIntegration() = status %q, configured %t; want %q, true", status, configured, integrationStatusOff)
	}
}

func TestIntegrationReportJSONDoesNotContainRouterKey(t *testing.T) {
	report := integrationReport{
		Directory:    "/tmp/project",
		Integrations: []integrationStatus{{Tool: "Codex", Scope: "user", Status: integrationStatusOn, Installed: true, UsingRouter: true, ConfigPaths: []string{"/tmp/project/.codex/config.toml"}}},
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || strings.Contains(string(encoded), "X-Weave-Router-Key") || strings.Contains(string(encoded), "secret") {
		t.Fatal("integration report unexpectedly contains a secret")
	}
}
