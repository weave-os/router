// Command arch_ratchet fails when a change grows code past the repository's size and
// complexity limits. It compares the working tree with its merge base, so existing
// over-limit code may shrink or move but never grow, and new code must meet the limits.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	base := flag.String("base", "origin/main", "ref whose merge base with HEAD is the comparison point")
	flag.Parse()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "arch_ratchet: locate repository root:", err)
		os.Exit(2)
	}
	root := strings.TrimSpace(string(out))
	result, err := run(repo{root: root}, *base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "arch_ratchet:", err)
		os.Exit(2)
	}
	if len(result.violations) == 0 {
		fmt.Printf("arch_ratchet: OK (%d changed Go files checked against %s)\n", result.checkedFiles, short(result.baseRev))
		return
	}
	report(os.Stderr, result)
	os.Exit(1)
}

func report(w io.Writer, result runResult) {
	fmt.Fprintf(w, "arch_ratchet: %d violation(s) against merge base %s\n", len(result.violations), short(result.baseRev))
	fmt.Fprintf(w, "Limits: cognitive complexity %d, %d lines and %d params per function, nesting depth %d, %d lines per file.\n",
		funcLimits[metricCognitive], funcLimits[metricLines], funcLimits[metricParams], funcLimits[metricNesting], limitFileLines)
	for _, v := range result.violations {
		fmt.Fprintf(w, "  %s\n", v)
	}
	fmt.Fprintln(w, "Fix: split the code along a concept boundary (.agents/skills/fix-lint-gate-failure/SKILL.md).")
	fmt.Fprintf(w, "Raising limits or adding //nolint is not an option; reviewed exceptions live in %s.\n", exceptionsPath)
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
