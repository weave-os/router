package cascade_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"workweave/router/internal/router/cascade"
	"workweave/router/internal/translate"
)

func toolResult(text string) []translate.ConversationMessage {
	return []translate.ConversationMessage{
		{Role: "user", ToolResults: []translate.ConversationToolResult{{Text: text}}},
	}
}

func TestRecognizesRunnerFailures(t *testing.T) {
	for name, output := range map[string]string{
		"pytest":  "collected 8 items\n\n=== 3 failed, 5 passed in 1.24s ===",
		"go test": "--- FAIL: TestThing (0.00s)\nFAIL\nFAIL\tworkweave/router/internal/x\t0.3s",
		"go bare": "some log\nFAIL",
		"jest":    "Tests:       2 failed, 8 passed, 10 total",
		"suites":  "Test Suites: 1 failed, 2 passed, 3 total",
		"noStart": "● Test suite failed to run\n\n  Cannot find module 'x'",
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, cascade.Failed, cascade.DetectVerification(toolResult(output)))
		})
	}
}

func TestRecognizesRunnerPasses(t *testing.T) {
	for name, output := range map[string]string{
		"pytest":  "=== 12 passed in 3.01s ===",
		"go test": "ok  \tworkweave/router/internal/x\t0.412s",
		"go bare": "some log\nPASS",
		"jest":    "Tests:       10 passed, 10 total",
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, cascade.Passed, cascade.DetectVerification(toolResult(output)))
		})
	}
}

func TestUnrecognizedOutputIsNoSignalNotFailure(t *testing.T) {
	// The load-bearing bias. Anything not recognizably a test summary must degrade
	// to "never escalate" (the cheap model, current behaviour) rather than to
	// "always escalate", which would be strong-model prices for every session.
	for name, output := range map[string]string{
		"file listing":  "src/main.go\nsrc/util.go\nREADME.md",
		"grep hit":      "src/handler.go:42:\treturn fmt.Errorf(\"failed to parse: %w\", err)",
		"source code":   "if err != nil {\n\treturn errors.New(\"request failed\")\n}",
		"prose":         "The build failed earlier but I fixed the import.",
		"empty":         "",
		"git status":    "On branch main\nnothing to commit, working tree clean",
		"install noise": "added 402 packages, and audited 403 packages in 5s",
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, cascade.NoSignal, cascade.DetectVerification(toolResult(output)),
				"must not read ordinary output as a verification result")
		})
	}
}

func TestAFailureAnywhereInTheWindowWins(t *testing.T) {
	// A single test command routinely prints per-package lines where some pass and
	// some fail. "Did verification succeed" is the question, and it did not.
	messages := []translate.ConversationMessage{{
		Role: "user",
		ToolResults: []translate.ConversationToolResult{
			{Text: "ok  \tworkweave/router/internal/a\t0.1s"},
			{Text: "FAIL\tworkweave/router/internal/b\t0.2s"},
		},
	}}
	assert.Equal(t, cascade.Failed, cascade.DetectVerification(messages))
}

func TestAToolErrorAloneIsNotAVerificationFailure(t *testing.T) {
	// A tool erroring means the command never ran — a bad path, a missing binary.
	// That is not evidence about the code, and counting it would escalate sessions
	// for typos.
	messages := []translate.ConversationMessage{{
		Role: "user",
		ToolResults: []translate.ConversationToolResult{
			{IsError: true, Text: "bash: pytesst: command not found"},
		},
	}}
	assert.Equal(t, cascade.NoSignal, cascade.DetectVerification(messages))
}

func TestOnlyTheTailOfALongLogIsScanned(t *testing.T) {
	// A runner summary is at the end, and a full test log can be megabytes.
	noise := strings.Repeat("compiling package...\n", 500)
	assert.Equal(t, cascade.Failed,
		cascade.DetectVerification(toolResult(noise+"FAIL\tpkg\t0.1s")))

	// A summary buried far above the tail is not scanned — accepted, because the
	// alternative is scanning unbounded output on every turn.
	assert.Equal(t, cascade.NoSignal,
		cascade.DetectVerification(toolResult("FAIL\tpkg\t0.1s\n"+noise)))
}

func TestNoToolResultsIsNoSignal(t *testing.T) {
	messages := []translate.ConversationMessage{{Role: "user", Text: "add a test"}}
	assert.Equal(t, cascade.NoSignal, cascade.DetectVerification(messages))
	assert.Equal(t, cascade.NoSignal, cascade.DetectVerification(nil))
}

func TestAPytestBannerWithoutAnOutcomeIsNoSignal(t *testing.T) {
	// pytest prints decorative "=====" separators constantly; only the ones
	// carrying an outcome are summaries.
	assert.Equal(t, cascade.NoSignal,
		cascade.DetectVerification(toolResult("=========== test session starts ===========")))
}

func TestAPytestErrorBannerIsAFailure(t *testing.T) {
	assert.Equal(t, cascade.Failed,
		cascade.DetectVerification(toolResult("=== 1 error in 0.4s ===")))
}
