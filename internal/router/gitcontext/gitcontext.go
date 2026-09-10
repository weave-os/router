// Package gitcontext parses the client-reported git snapshot that Claude Code
// writes into its system prompt (the "gitStatus:" block) into a small value
// type for telemetry. Pure, fail-open: anything that does not match the
// documented shape yields the zero value and ok == false.
package gitcontext

import (
	"strings"
)

// GitContext is the starting tree of a coding-agent session as the client
// reported it. HeadSHA is stored as the client abbreviates it; consumers
// compare it by prefix against a full sha.
type GitContext struct {
	Branch  string
	HeadSHA string
	Dirty   bool
}

// blockMarker is a fixed line prefix of the Claude Code gitStatus block.
type blockMarker string

const (
	markerBlock         blockMarker = "gitStatus:"
	markerCurrentBranch blockMarker = "Current branch:"
	markerStatus        blockMarker = "Status:"
	markerRecentCommits blockMarker = "Recent commits:"
)

// cleanStatusPlaceholder is what Claude Code prints under "Status:" when
// `git status --short` is empty.
const cleanStatusPlaceholder = "(clean)"

// Parse finds the gitStatus block among the request's system blocks and
// extracts the current branch, the head sha (first line of "Recent commits:"),
// and whether the tree was dirty. Returns (zero, false) when no block is
// present or any required section is missing or malformed.
func Parse(systemBlocks []string) (GitContext, bool) {
	for _, block := range systemBlocks {
		start := strings.Index(block, string(markerBlock))
		if start < 0 {
			continue
		}
		return parseBlock(block[start:])
	}
	return GitContext{}, false
}

func parseBlock(block string) (GitContext, bool) {
	lines := strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n")

	var (
		branch     string
		statusSeen bool
		dirty      bool
		headSHA    string
	)

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, string(markerCurrentBranch)):
			branch = strings.TrimSpace(strings.TrimPrefix(line, string(markerCurrentBranch)))
		case line == string(markerStatus):
			statusSeen = true
			dirty, i = parseStatusSection(lines, i+1)
		case line == string(markerRecentCommits):
			headSHA = parseFirstCommitSHA(lines, i+1)
		}
	}

	if branch == "" || !statusSeen || headSHA == "" {
		return GitContext{}, false
	}
	return GitContext{Branch: branch, HeadSHA: headSHA, Dirty: dirty}, true
}

// parseStatusSection scans porcelain lines until the next section marker or
// the end of the block. Returns the dirty verdict and the index of the last
// line consumed.
func parseStatusSection(lines []string, from int) (dirty bool, last int) {
	last = from - 1
	for i := from; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, string(markerRecentCommits)) {
			return dirty, i - 1
		}
		last = i
		if line == "" || line == cleanStatusPlaceholder {
			continue
		}
		dirty = true
	}
	return dirty, last
}

// parseFirstCommitSHA returns the abbreviated sha leading the first non-blank
// line after "Recent commits:", or "" when that line is not `<hex> <subject>`.
func parseFirstCommitSHA(lines []string, from int) string {
	for i := from; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		sha, _, _ := strings.Cut(line, " ")
		if !isHexSHA(sha) {
			return ""
		}
		return sha
	}
	return ""
}

// Abbreviated shas are at least 7 hex chars; a full sha is 40.
const (
	minSHALen = 7
	maxSHALen = 40
)

func isHexSHA(s string) bool {
	if len(s) < minSHALen || len(s) > maxSHALen {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
