package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
)

// rosterCheckReport is the machine-readable result of `policyctl check-rosters`.
// Rosters in schemas the compiler does not accept as source are historical
// artifacts and are reported as skipped, not failed.
type rosterCheckReport struct {
	Compiled []compiledRosterEntry `json:"compiled"`
	Skipped  []skippedRosterEntry  `json:"skipped"`
	Failed   []failedRosterEntry   `json:"failed"`
}

type compiledRosterEntry struct {
	File          string                   `json:"file"`
	SchemaVersion rosterdata.SchemaVersion `json:"schema_version"`
	RosterSHA256  string                   `json:"roster_sha256"`
	PolicySHA256  string                   `json:"policy_sha256"`
}

type skippedRosterEntry struct {
	File          string                   `json:"file"`
	SchemaVersion rosterdata.SchemaVersion `json:"schema_version,omitempty"`
	Reason        string                   `json:"reason"`
}

type failedRosterEntry struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

func runCheckRosters(args []string) error {
	flags := flag.NewFlagSet(string(commandCheckRosters), flag.ContinueOnError)
	rosterDir := flags.String("dir", "", "directory of reviewed roster JSON files (e.g. rosters/frozen)")
	sourceRevision := flags.String("source-revision", "", "reviewed source revision recorded in each compiled policy")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *rosterDir == "" {
		return errors.New("check-rosters requires --dir")
	}
	report, err := checkRosterDir(*rosterDir, policycompiler.Options{SourceRevision: *sourceRevision})
	if err != nil {
		return err
	}
	if writeErr := writeJSON(report); writeErr != nil {
		return writeErr
	}
	if len(report.Failed) > 0 {
		return fmt.Errorf("%d roster(s) failed to compile into a serving policy", len(report.Failed))
	}
	if len(report.Compiled) == 0 {
		return fmt.Errorf("no roster under %s is in a compilable schema; check the directory", *rosterDir)
	}
	return nil
}

// checkRosterDir compiles every roster whose source schema the compiler
// accepts. A reviewed v7 roster that the compiler rejects is exactly the drift
// that strands production without a publishable policy.
func checkRosterDir(dir string, options policycompiler.Options) (rosterCheckReport, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return rosterCheckReport{}, fmt.Errorf("list rosters: %w", err)
	}
	if len(paths) == 0 {
		return rosterCheckReport{}, fmt.Errorf("no roster JSON files under %s", dir)
	}
	var report rosterCheckReport
	for _, path := range paths {
		file := filepath.Base(path)
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return rosterCheckReport{}, fmt.Errorf("read roster %s: %w", path, readErr)
		}
		schema, schemaErr := sourceSchemaVersion(source)
		if schemaErr != nil {
			report.Failed = append(report.Failed, failedRosterEntry{File: file, Error: schemaErr.Error()})
			continue
		}
		if !policycompiler.CompilableSource(schema) {
			report.Skipped = append(report.Skipped, skippedRosterEntry{
				File: file, SchemaVersion: schema, Reason: "schema predates Go selection; not a compilable policy source",
			})
			continue
		}
		canonical, compileErr := compilePublishable(source, options)
		if compileErr != nil {
			report.Failed = append(report.Failed, failedRosterEntry{File: file, Error: compileErr.Error()})
			continue
		}
		report.Compiled = append(report.Compiled, compiledRosterEntry{
			File: file, SchemaVersion: schema, RosterSHA256: rosterdata.SHA256Hex(source),
			PolicySHA256: policyregistry.Digest(canonical),
		})
	}
	return report, nil
}

// compilePublishable applies the same gate `publish` does: the compiled policy
// must also pass catalog validation, or no release could ever be built from it.
func compilePublishable(source []byte, options policycompiler.Options) ([]byte, error) {
	canonical, _, err := policycompiler.Compile(source, options)
	if err != nil {
		return nil, err
	}
	if _, err := rosterdata.ParseValidated(canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

func sourceSchemaVersion(source []byte) (rosterdata.SchemaVersion, error) {
	var header struct {
		SchemaVersion rosterdata.SchemaVersion `json:"schema_version"`
	}
	if err := json.Unmarshal(source, &header); err != nil {
		return "", fmt.Errorf("parse roster header: %w", err)
	}
	if header.SchemaVersion == "" {
		return "", errors.New("missing schema_version")
	}
	return header.SchemaVersion, nil
}
