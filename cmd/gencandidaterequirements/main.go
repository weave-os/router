// Command gencandidaterequirements writes the Router candidate requirements declaration.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"weave-os/router/internal/policyregistry"
)

func main() {
	check := flag.Bool("check", false, "fail when the declaration is stale")
	flag.Parse()

	expected, err := policyregistry.RouterCandidateRequirementsJSON()
	if err != nil {
		fail(err)
	}
	path := policyregistry.RouterCandidateRequirementsPath
	if !*check {
		if err := os.WriteFile(path, expected, 0o644); err != nil {
			fail(fmt.Errorf("write %s: %w", path, err))
		}
		return
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		fail(fmt.Errorf("read %s: %w", path, err))
	}
	if !bytes.Equal(actual, expected) {
		fail(fmt.Errorf("%s is stale; run make generate-candidate-requirements", path))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gencandidaterequirements:", err)
	os.Exit(1)
}
