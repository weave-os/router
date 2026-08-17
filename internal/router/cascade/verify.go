package cascade

import (
	"strings"

	"workweave/router/internal/translate"
)

// maxScannedResults bounds how far back a turn's tool results are scanned. The
// verification that matters is the one the agent just ran; older results are
// history the previous turn already acted on.
const maxScannedResults = 4

// maxScannedLines bounds per-result scanning. A runner summary is at the end of
// its output, and a full test log can be megabytes.
const maxScannedLines = 40

// DetectVerification reads a turn's tool results for a test-runner summary.
//
// **Conservative by construction.** Output that is not recognizably a test
// summary returns NoSignal, so an unparsed runner degrades to "never escalate"
// — the cheap model, which is current behaviour — rather than to "always
// escalate", which would be strong-model prices for every session. The offline
// sweep says a trigger at 50% accuracy still beats the cost-matched frontier, so
// detector *coverage* is not the critical path; detector *bias* is.
//
// A failure anywhere in the scanned window wins over a pass. A single test
// command commonly prints per-package lines where some pass and some fail, and
// the question this answers is "did verification succeed", which it did not.
//
// `IsError` on the tool result is deliberately **not** treated as a failed
// verification on its own. A tool erroring means the command did not run — a bad
// path, a missing binary, a malformed grep — which is not evidence about the
// code, and counting it would escalate sessions for typos.
func DetectVerification(messages []translate.ConversationMessage) Verdict {
	results := recentToolResults(messages)
	verdict := NoSignal
	for _, text := range results {
		switch classify(text) {
		case Failed:
			return Failed
		case Passed:
			verdict = Passed
		}
	}
	return verdict
}

// recentToolResults collects the last few tool-result texts, newest first.
func recentToolResults(messages []translate.ConversationMessage) []string {
	var out []string
	for i := len(messages) - 1; i >= 0 && len(out) < maxScannedResults; i-- {
		for j := len(messages[i].ToolResults) - 1; j >= 0 && len(out) < maxScannedResults; j-- {
			if text := messages[i].ToolResults[j].Text; text != "" {
				out = append(out, text)
			}
		}
		// Stop at the first assistant turn: anything before it belongs to an
		// earlier round-trip the previous turn already saw.
		if messages[i].Role == "assistant" && len(out) > 0 {
			break
		}
	}
	return out
}

// classify looks for a runner summary line in the tail of one tool result.
func classify(text string) Verdict {
	lines := tailLines(text, maxScannedLines)
	verdict := NoSignal
	for _, line := range lines {
		switch classifyLine(strings.TrimSpace(line)) {
		case Failed:
			return Failed
		case Passed:
			verdict = Passed
		}
	}
	return verdict
}

func tailLines(text string, n int) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// classifyLine recognizes the summary lines the common runners emit.
//
// Each pattern is anchored on a shape a runner produces and prose does not.
// "Error:" and "failed" on their own are excluded deliberately: they appear
// constantly in ordinary agent output and in source code, and a detector that
// fires on them escalates everything.
func classifyLine(line string) Verdict {
	lower := strings.ToLower(line)

	switch {
	// go test: "--- FAIL: TestX", a leading "FAIL\tpkg", or a bare "FAIL".
	case strings.HasPrefix(line, "--- FAIL:"), strings.HasPrefix(line, "FAIL\t"),
		line == "FAIL":
		return Failed
	// go test success: "ok  \tpkg\t0.1s", "--- PASS:", or a bare "PASS".
	case strings.HasPrefix(line, "ok  \t"), strings.HasPrefix(line, "--- PASS:"),
		line == "PASS":
		return Passed

	// pytest banner: "=== 3 failed, 5 passed in 1.2s ===".
	case strings.HasPrefix(line, "="), strings.HasPrefix(line, "-"):
		if !strings.Contains(lower, "passed") && !strings.Contains(lower, "failed") &&
			!strings.Contains(lower, "error") {
			return NoSignal
		}
		if strings.Contains(lower, "failed") || strings.Contains(lower, "error") {
			return Failed
		}
		return Passed

	// jest / vitest: "Tests:  2 failed, 8 passed, 10 total".
	case strings.HasPrefix(lower, "tests:"), strings.HasPrefix(lower, "test suites:"):
		if strings.Contains(lower, "failed") {
			return Failed
		}
		if strings.Contains(lower, "passed") {
			return Passed
		}
		return NoSignal

	// jest: a suite that could not even start.
	case strings.Contains(lower, "test suite failed to run"):
		return Failed
	}
	return NoSignal
}
