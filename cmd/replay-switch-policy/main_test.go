package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/switchpolicy"
)

const (
	modelSol            = "gpt-6.1-sol"
	independentTestRole = "independent-role"
)

func syntheticRecord(id, session string, group escalation.Group, model string, second int) record {
	confidence, eligible := .9, true
	target := switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: model}
	return record{
		ID: id, SessionKey: session, Role: sessionpin.DefaultRole,
		Timestamp:  time.Date(2026, 1, 1, 0, 0, second, 0, time.UTC),
		Proposal:   switchpolicy.Selection{Target: target, Group: group},
		Confidence: &confidence, Eligible: &eligible, Observed: target,
	}
}

func encodeRecords(t *testing.T, rows ...record) []byte {
	t.Helper()
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	return input.Bytes()
}

func TestReplayPartitionsStateAndUsesActualPolicy(t *testing.T) {
	rows := []record{
		syntheticRecord("a0", "a", escalation.High, modelSol, 0),
		syntheticRecord("b0", "b", escalation.Low, catalog.ModelGPT6Luna, 0),
		syntheticRecord("a1", "a", escalation.Low, catalog.ModelGPT6Luna, 1),
		syntheticRecord("b1", "b", escalation.High, modelSol, 1),
		syntheticRecord("a2", "a", escalation.Low, catalog.ModelGPT6Luna, 2),
		syntheticRecord("b2", "b", escalation.High, modelSol, 2),
		syntheticRecord("b3", "b", escalation.High, modelSol, 3),
	}
	// Two roles in one session must not share the default-role challenger.
	otherRole := syntheticRecord("a0", "a", escalation.Low, catalog.ModelGPT6Luna, 0)
	otherRole.Role = independentTestRole
	rows = append(rows, otherRole)
	input := encodeRecords(t, rows...)
	var trace bytes.Buffer
	output, err := replay(bytes.NewReader(input), &trace, switchpolicy.Config{MinimumConfidence: .5, UpgradeVotes: 3, DowngradeVotes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if output.Sessions != 3 || output.Observations != 8 || output.CandidateTransitions != 1 || output.ObservedTransitions != 2 || output.ChangedRequests != 4 {
		t.Fatalf("unexpected partitioned replay: %+v", output)
	}
	if output.Reasons[switchpolicy.ReasonInitial] != 3 || output.Reasons[switchpolicy.ReasonAgreement] != 4 || output.Reasons[switchpolicy.ReasonSwitch] != 1 {
		t.Fatalf("trace must use the evaluator's real decisions: %+v", output.Reasons)
	}
	checksum := sha256.Sum256(input)
	if output.InputSHA256 != hex.EncodeToString(checksum[:]) {
		t.Fatal("input provenance hash does not match the complete evidence")
	}
	decoder := json.NewDecoder(&trace)
	for i := range rows {
		var row traceRecord
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		if row.ID != rows[i].ID || row.Decision.Version != switchpolicy.VersionAgreementV1 {
			t.Fatalf("trace %d does not describe the input decision: %+v", i, row)
		}
	}
}

func TestReplayRejectsIncompleteDuplicateAndUnorderedEvidence(t *testing.T) {
	first := syntheticRecord("first", "session", escalation.High, modelSol, 2)
	missingConfidence := first
	missingConfidence.Confidence = nil
	missingEligibility := first
	missingEligibility.Eligible = nil
	earlier := syntheticRecord("earlier", "session", escalation.High, modelSol, 1)
	valid := encodeRecords(t, first)
	tests := []struct {
		name  string
		input []byte
	}{
		{"empty", nil},
		{"missing confidence", encodeRecords(t, missingConfidence)},
		{"missing eligibility", encodeRecords(t, missingEligibility)},
		{"duplicate identity", encodeRecords(t, first, first)},
		{"reverse bucket time", encodeRecords(t, first, earlier)},
		{"truncated input", valid[:len(valid)/2]},
		{"unknown content field", []byte(strings.Replace(string(valid), `"id":`, `"prompt":"must not be accepted","id":`, 1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := replay(bytes.NewReader(test.input), io.Discard, switchpolicy.Config{MinimumConfidence: .5, UpgradeVotes: 3, DowngradeVotes: 5}); err == nil {
				t.Fatal("invalid evidence was silently accepted")
			}
		})
	}
}

type brokenTrace struct{}

func (brokenTrace) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestReplayDoesNotClaimCompletionAfterTraceFailure(t *testing.T) {
	input := encodeRecords(t, syntheticRecord("first", "session", escalation.High, modelSol, 0))
	_, err := replay(bytes.NewReader(input), brokenTrace{}, switchpolicy.Config{MinimumConfidence: .5, UpgradeVotes: 3, DowngradeVotes: 5})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("trace failure was not propagated: %v", err)
	}
}

func TestRunProducesPrivateTraceWithoutOverwriting(t *testing.T) {
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "input.jsonl")
	tracePath := filepath.Join(directory, "trace.jsonl")
	input := encodeRecords(t, syntheticRecord("first", "session", escalation.High, modelSol, 0))
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"-input", inputPath, "-trace", tracePath}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var output summary
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Observations != 1 {
		t.Fatalf("missing complete JSON summary: %+v, %v", output, err)
	}
	info, err := os.Stat(tracePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("trace permissions: %v, %v", info, err)
	}
	stdout.Reset()
	if err := run(args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
		t.Fatal("existing trace must not be overwritten or reported complete")
	}
}
