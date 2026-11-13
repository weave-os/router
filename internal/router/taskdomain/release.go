package taskdomain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
)

// SystemPrompt is the exact training-time text; changing it requires a new release.
const SystemPrompt = "Classify the work requested in the current user turn. Use earlier turns only to resolve context. Return exactly five comma-separated integers, each 0 or 1, in this order: ui,logic,data,infra,docs. No other text."

// Release pins every loaded model/tokenizer file and each supported roster's evidence.
type Release struct {
	SchemaVersion     string            `json:"schema_version"`
	ProjectionVersion string            `json:"projection_version"`
	PromptSHA256      string            `json:"prompt_sha256"`
	Files             map[string]string `json:"files"`
	Evidence          map[string]string `json:"evidence"`
}

// ParseRelease accepts only the reviewed text-only inference contract.
func ParseRelease(payload []byte, expectedSHA256 string) (Release, error) {
	var release Release
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return release, errors.New("task release digest mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&release); err != nil {
		return release, errors.New("invalid task release")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return release, errors.New("trailing task release content")
	}
	promptDigest := sha256.Sum256([]byte(SystemPrompt))
	if release.SchemaVersion != SchemaVersion || release.ProjectionVersion != ProjectionVersion || release.PromptSHA256 != hex.EncodeToString(promptDigest[:]) {
		return release, errors.New("unsupported task classifier contract")
	}
	for _, required := range []string{"model.safetensors", "config.json", "tokenizer.json", "tokenizer_config.json", "generation_config.json"} {
		if !ValidDigest(release.Files[required]) {
			return release, errors.New("task release missing model/tokenizer identity")
		}
	}
	for name, digest := range release.Files {
		if name == "." || name == ".." || path.Base(name) != name || !ValidDigest(digest) {
			return release, errors.New("invalid task release file identity")
		}
	}
	if len(release.Evidence) == 0 {
		return release, errors.New("task release has no roster evidence")
	}
	for roster, digest := range release.Evidence {
		if !ValidDigest(roster) || !ValidDigest(digest) {
			return release, errors.New("invalid task scoring evidence identity")
		}
	}
	return release, nil
}
