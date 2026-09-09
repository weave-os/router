// Command geninferencepolicy writes the static inference-policy projections.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"weave-os/router/internal/router/policy"
)

const (
	markdownPath = "docs/POLICY_INFERENCE.md"
	jsonPath     = "docs/POLICY_INFERENCE.json"
)

func main() {
	check := flag.Bool("check", false, "fail when generated projections are stale")
	flag.Parse()

	registry := policy.DefaultRegistry()
	jsonProjection, err := registry.StaticJSON()
	if err != nil {
		fail(err)
	}
	files := map[string][]byte{
		markdownPath: registry.StaticMarkdown(),
		jsonPath:     jsonProjection,
	}
	for path, expected := range files {
		if *check {
			checkFile(path, expected)
			continue
		}
		if err := os.WriteFile(path, expected, 0o644); err != nil {
			fail(fmt.Errorf("write %s: %w", path, err))
		}
	}
}

func checkFile(path string, expected []byte) {
	actual, err := os.ReadFile(path)
	if err != nil {
		fail(fmt.Errorf("read %s: %w", path, err))
	}
	if !bytes.Equal(actual, expected) {
		fail(fmt.Errorf("%s is stale; run make generate-inference-policy", path))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "geninferencepolicy:", err)
	os.Exit(1)
}
