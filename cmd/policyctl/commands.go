package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/policycompiler"
)

type commandName string

const (
	commandCompile      commandName = "compile"
	commandCheckRosters commandName = "check-rosters"
	commandPublish      commandName = "publish"
	commandStatus       commandName = "status"
	commandServing      commandName = "serving"
)

const defaultRegistryURI = "gs://weave_ml/weave_registry"

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: policyctl <compile|check-rosters|serving> [flags]")
	}
	switch commandName(args[0]) {
	case commandServing:
		return runServing(ctx, args[1:])
	case commandCompile:
		return runCompile(args[1:])
	case commandCheckRosters:
		return runCheckRosters(args[1:])
	default:
		return fmt.Errorf("unknown policyctl command %q", args[0])
	}
}

func runCompile(args []string) error {
	flags := flag.NewFlagSet(string(commandCompile), flag.ContinueOnError)
	sourcePath := flags.String("source", "", "reviewed roster source JSON")
	outputPath := flags.String("output", "", "canonical policy output JSON")
	classOrderRaw := flags.String("class-order", "", "comma-separated classifier class order")
	sourceRevision := flags.String("source-revision", "", "reviewed source revision")
	evidenceURI := flags.String("evidence-uri", "", "optional offline-evidence URI")
	evidenceSHA := flags.String("evidence-sha256", "", "optional offline-evidence digest")
	checkOnly := flags.Bool("check", false, "validate and print identity without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sourcePath == "" || (!*checkOnly && *outputPath == "") {
		return errors.New("compile requires --source and either --output or --check")
	}
	source, err := os.ReadFile(*sourcePath)
	if err != nil {
		return fmt.Errorf("read policy source: %w", err)
	}
	canonical, policy, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: parseList(*classOrderRaw), SourceRevision: *sourceRevision,
		EvidenceURI: *evidenceURI, EvidenceSHA256: *evidenceSHA,
	})
	if err != nil {
		return err
	}
	if !*checkOnly {
		if err := os.WriteFile(*outputPath, canonical, 0o600); err != nil {
			return fmt.Errorf("write compiled policy: %w", err)
		}
	}
	return writeJSON(map[string]any{
		"schema_version": policy.SchemaVersion,
		"policy_sha256":  policyregistry.Digest(canonical),
		"class_order":    policy.ClassOrder,
		"arms":           len(policy.AllArms()),
		"output":         *outputPath,
	})
}

func parseList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
