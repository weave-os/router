package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

const structuralLabel = "structural"

var refactorTitle = regexp.MustCompile(`^refactor(\([^)]*\))?!?:`)

var protectedPrefixes = []string{
	"internal/proxy/testdata/golden/",
	"internal/proxy/testdata/conformance/",
}

var protectedFiles = []string{
	"internal/proxy/testdata/paid_fallback_rules.golden",
	"internal/proxy/golden_test.go",
	"internal/proxy/conformance_golden_test.go",
}

var protectedGlobs = []string{
	"internal/proxy/golden_*_test.go",
}

func main() {
	var labels []string
	if raw := os.Getenv("PR_LABELS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &labels); err != nil {
			fmt.Fprintln(os.Stderr, "structural_pr_guard: parse PR_LABELS:", err)
			os.Exit(2)
		}
	}
	changed, err := readPaths(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "structural_pr_guard: read changed files:", err)
		os.Exit(2)
	}
	if !checkStructuralGoldens(os.Stdout, os.Getenv("PR_TITLE"), labels, changed) {
		os.Exit(1)
	}
}

func checkStructuralGoldens(w io.Writer, title string, labels, changed []string) bool {
	if !isStructural(title, labels) {
		_, _ = fmt.Fprintln(w, "structural_pr_guard: not a structural PR (no refactor title, no structural label); nothing to check")
		return true
	}
	blocked := protectedChanges(changed)
	if len(blocked) == 0 {
		_, _ = fmt.Fprintf(w, "structural_pr_guard: OK (%d changed files, no golden changes)\n", len(changed))
		return true
	}
	_, _ = fmt.Fprintf(w, "structural_pr_guard: a structural PR must not change goldens; %d protected file(s) changed:\n", len(blocked))
	for _, file := range blocked {
		_, _ = fmt.Fprintf(w, "  %q\n", file)
	}
	_, _ = fmt.Fprintln(w, "Revert those files, or split the behavior change into a PR that is neither titled refactor nor labeled structural.")
	return false
}

func isStructural(title string, labels []string) bool {
	return refactorTitle.MatchString(strings.TrimSpace(title)) || slices.Contains(labels, structuralLabel)
}

func protectedChanges(changed []string) []string {
	var blocked []string
	for _, file := range changed {
		if isProtected(file) {
			blocked = append(blocked, file)
		}
	}
	return blocked
}

func isProtected(file string) bool {
	if slices.Contains(protectedFiles, file) {
		return true
	}
	for _, prefix := range protectedPrefixes {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	for _, glob := range protectedGlobs {
		if matched, _ := path.Match(glob, file); matched {
			return true
		}
	}
	return false
}

func readPaths(r io.Reader) ([]string, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, file := range strings.Split(string(payload), "\x00") {
		if file != "" {
			paths = append(paths, file)
		}
	}
	return paths, nil
}
