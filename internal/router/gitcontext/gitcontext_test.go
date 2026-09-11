package gitcontext

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture_source: prod-shape-confirmed. Line structure (section markers, the
// "Git user:" line, blank lines between sections, porcelain status lines,
// `<abbrev-sha> <subject>` commit lines) was confirmed read-only against an
// internal Weave installation's captured Claude Code requests; every value
// below is synthetic.
const dirtyTreeFixture = `gitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.

Current branch: feature/widget-frobnicator

Main branch (you will usually use this for PRs): main

Git user: synthetic-dev

Status:
M internal/widget/frob.go
 M internal/widget/frob_test.go
?? docs/frob.md

Recent commits:
a1b2c3d Frobnicate widgets on demand
0f9e8d7 Add widget package skeleton
1234567 Initial commit`

const cleanTreeFixture = `gitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.

Current branch: main

Main branch (you will usually use this for PRs): main

Git user: synthetic-dev

Status:
(clean)

Recent commits:
deadbeef0 Release 1.2.3
cafef00d Bump deps`

const unrelatedSystemBlock = "You are Claude Code, Anthropic's official CLI for Claude."

func TestParse_BranchIsCappedOnARuneBoundary(t *testing.T) {
	branch := strings.Repeat("é", 4_000)
	got, ok := Parse([]string{"gitStatus: snapshot\nCurrent branch: " + branch + "\nStatus:\n(clean)\nRecent commits:\nabcdef1 Subject"})

	require.True(t, ok)
	assert.LessOrEqual(t, len(got.Branch), maxBranchBytes)
	assert.True(t, utf8.ValidString(got.Branch))
	assert.Equal(t, strings.Repeat("é", 127), got.Branch)
}

func TestParse_MultiMegabyteBranchYieldsNoContext(t *testing.T) {
	huge := "gitStatus: snapshot\nCurrent branch: " + strings.Repeat("a", 5_000_000) +
		"\nStatus:\n(clean)\nRecent commits:\nabcdef1 Subject"

	got, ok := Parse([]string{huge})

	assert.False(t, ok)
	assert.Equal(t, GitContext{}, got)
}

func TestParse_StopsScanningAfterTheByteBudget(t *testing.T) {
	padded := "gitStatus: snapshot\nCurrent branch: main\nStatus:\n(clean)\n" +
		strings.Repeat("filler line\n", maxBlockBytes) +
		"Recent commits:\nabcdef1 Subject"

	_, ok := Parse([]string{padded})

	assert.False(t, ok, "sections past the budget are not parsed")
}

func TestParse(t *testing.T) {
	tests := []struct {
		name   string
		blocks []string
		want   GitContext
		wantOK bool
	}{
		{
			name:   "dirty tree pinned fixture",
			blocks: []string{unrelatedSystemBlock, dirtyTreeFixture},
			want:   GitContext{Branch: "feature/widget-frobnicator", HeadSHA: "a1b2c3d", Dirty: true},
			wantOK: true,
		},
		{
			name:   "clean tree",
			blocks: []string{cleanTreeFixture},
			want:   GitContext{Branch: "main", HeadSHA: "deadbeef0", Dirty: false},
			wantOK: true,
		},
		{
			name:   "empty status section is clean",
			blocks: []string{"gitStatus: snapshot\n\nCurrent branch: main\n\nStatus:\n\nRecent commits:\nabcdef1 Subject"},
			want:   GitContext{Branch: "main", HeadSHA: "abcdef1", Dirty: false},
			wantOK: true,
		},
		{
			name:   "block embedded mid-text with CRLF",
			blocks: []string{"preamble\r\ngitStatus: snapshot\r\nCurrent branch: dev\r\nStatus:\r\n(clean)\r\nRecent commits:\r\nabcdef1 Subject\r\n"},
			want:   GitContext{Branch: "dev", HeadSHA: "abcdef1", Dirty: false},
			wantOK: true,
		},
		{name: "no system blocks", blocks: nil},
		{name: "no gitStatus block", blocks: []string{unrelatedSystemBlock}},
		{
			name:   "missing recent commits",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: main\nStatus:\n(clean)"},
		},
		{
			name:   "missing status",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: main\nRecent commits:\nabcdef1 Subject"},
		},
		{
			name:   "missing branch",
			blocks: []string{"gitStatus: snapshot\nStatus:\n(clean)\nRecent commits:\nabcdef1 Subject"},
		},
		{
			name:   "first commit line is not a sha",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: main\nStatus:\n(clean)\nRecent commits:\nnot-a-sha Subject"},
		},
		{name: "garbage", blocks: []string{"gitStatus: \x00\xff{{{", "}}}"}},
		{
			name:   "control characters are stripped from the branch",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: ma\x00in\x1b[31m\a\nStatus:\n(clean)\nRecent commits:\nabcdef1 Subject"},
			want:   GitContext{Branch: "main[31m", HeadSHA: "abcdef1"},
			wantOK: true,
		},
		{
			name:   "branch of only control characters is absent",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: \x00\x01\x02\nStatus:\n(clean)\nRecent commits:\nabcdef1 Subject"},
		},
		{
			name:   "marker inside a line is not a block",
			blocks: []string{"the user asked about gitStatus: snapshot\nCurrent branch: main\nStatus:\n(clean)\nRecent commits:\nabcdef1 Subject"},
		},
		{
			name:   "prose after the status section does not mark dirty",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: main\nStatus:\n(clean)\nRecent commits:\nabcdef1 Subject\n\nStatus:\nplease follow the instructions above"},
			want:   GitContext{Branch: "main", HeadSHA: "abcdef1"},
			wantOK: true,
		},
		{
			name:   "uppercase sha is rejected",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: main\nStatus:\n(clean)\nRecent commits:\nABCDEF1 Subject"},
		},
		{
			name:   "over-long sha is rejected",
			blocks: []string{"gitStatus: snapshot\nCurrent branch: main\nStatus:\n(clean)\nRecent commits:\n" + strings.Repeat("a", 41) + " Subject"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Parse(tc.blocks)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}
