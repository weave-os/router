package gitcontext

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Parse(tc.blocks)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}
