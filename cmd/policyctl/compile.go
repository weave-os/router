package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
)

func runCompile(args []string) error {
	flags := flag.NewFlagSet(string(commandCompile), flag.ContinueOnError)
	sourcePath := flags.String("source", "", "reviewed roster source JSON")
	outputPath := flags.String("output", "", "canonical policy output JSON")
	classOrderRaw := flags.String("class-order", "", "comma-separated classifier class order")
	sourceRevision := flags.String("source-revision", "", "reviewed source revision")
	evidenceURI := flags.String("evidence-uri", "", "optional offline-evidence URI")
	evidenceSHA := flags.String("evidence-sha256", "", "optional offline-evidence digest")
	checkOnly := flags.Bool("check", false, "validate and print identity without writing")
	taskEvidencePath := flags.String("task-domain-evidence", "", "task-domain evidence JSON to validate with --check")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sourcePath == "" || (!*checkOnly && *outputPath == "") {
		return errors.New("compile requires --source and either --output or --check")
	}
	if *taskEvidencePath != "" && !*checkOnly {
		return errors.New("--task-domain-evidence requires compile --check")
	}
	source, err := os.ReadFile(*sourcePath)
	if err != nil {
		return fmt.Errorf("read policy source: %w", err)
	}
	canonical, admitted, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: parseList(*classOrderRaw), SourceRevision: *sourceRevision,
		EvidenceURI: *evidenceURI, EvidenceSHA256: *evidenceSHA,
	})
	if err != nil {
		return err
	}
	policy := admitted.Roster()
	if *taskEvidencePath != "" {
		if err := checkTaskDomainEvidence(*taskEvidencePath, policy); err != nil {
			return err
		}
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

func checkTaskDomainEvidence(path string, roster *rosterdata.Roster) error {
	evidenceJSON, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read task-domain evidence: %w", err)
	}
	if _, err := selection.ParseDomainEvidence(evidenceJSON, roster); err != nil {
		return fmt.Errorf("validate task-domain evidence: %w", err)
	}
	return nil
}
