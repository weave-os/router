// Package gitcontext parses the client-reported git snapshot that Claude Code
// writes into its system prompt (the "gitStatus:" block) into a small value
// type for telemetry. Pure, fail-open: anything that does not match the
// documented shape yields the zero value and ok == false.
package gitcontext

import (
	"strings"
	"unicode/utf8"
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
	markerMainBranch    blockMarker = "Main branch"
	markerGitUser       blockMarker = "Git user:"
	markerStatus        blockMarker = "Status:"
	markerRecentCommits blockMarker = "Recent commits:"
)

// cleanStatusPlaceholder is what Claude Code prints under "Status:" when
// `git status --short` is empty.
const cleanStatusPlaceholder = "(clean)"

// The system prompt is client-controlled and unbounded; the block itself is a
// handful of lines. Parsing stops after this many bytes so a multi-megabyte
// prompt cannot turn capture into a scan of the whole payload.
const maxBlockBytes = 64 * 1024

// Git ref names are practically far below this; the branch column is
// VARCHAR(255).
const maxBranchBytes = 255

// Parse finds the gitStatus block among the request's system blocks and
// extracts the current branch, the head sha (first line of "Recent commits:"),
// and whether the tree was dirty. Returns (zero, false) when no block is
// present or any required section is missing or malformed.
func Parse(systemBlocks []string) (GitContext, bool) {
	for _, block := range systemBlocks {
		start, found := markerLineStart(block, markerBlock)
		if !found {
			continue
		}
		rest := block[start:]
		if len(rest) > maxBlockBytes {
			rest = rest[:maxBlockBytes]
		}
		return parseBlock(rest)
	}
	return GitContext{}, false
}

// markerLineStart reports the offset of the first occurrence of marker that
// begins a line, so a mention of the marker inside prose is not treated as the
// start of a block.
func markerLineStart(block string, marker blockMarker) (int, bool) {
	for offset := 0; offset < len(block); {
		i := strings.Index(block[offset:], string(marker))
		if i < 0 {
			return 0, false
		}
		at := offset + i
		if at == 0 || block[at-1] == '\n' || block[at-1] == '\r' {
			return at, true
		}
		offset = at + len(marker)
	}
	return 0, false
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
			branch = sanitizeBranch(strings.TrimPrefix(line, string(markerCurrentBranch)))
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

// sanitizeBranch drops control characters (including NUL, which Postgres
// rejects outright, and the escape sequences that would carry into any
// terminal printing a branch name) and caps the result at maxBranchBytes on a
// rune boundary. A branch that sanitizes to nothing is treated as absent.
func sanitizeBranch(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxBranchBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// parseStatusSection scans the porcelain lines of the Status section and
// returns whether any of them reports a modified path. Scanning stops at the
// next section marker or at the first line that is not porcelain-shaped, so
// prose after the block cannot mark the tree dirty. Returns the index of the
// last line consumed.
func parseStatusSection(lines []string, from int) (dirty bool, last int) {
	last = from - 1
	for i := from; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || line == cleanStatusPlaceholder {
			last = i
			continue
		}
		if isSectionMarker(line) || !isPorcelainLine(line) {
			return dirty, i - 1
		}
		last = i
		dirty = true
	}
	return dirty, last
}

func isSectionMarker(line string) bool {
	for _, m := range []blockMarker{markerBlock, markerCurrentBranch, markerMainBranch, markerGitUser, markerStatus, markerRecentCommits} {
		if strings.HasPrefix(line, string(m)) {
			return true
		}
	}
	return false
}

// porcelainStatusCodes are the XY code characters of `git status --short`.
const porcelainStatusCodes = "MADRCUT?!"

// isPorcelainLine reports whether a trimmed Status line is `<XY> <path>`.
func isPorcelainLine(line string) bool {
	code, path, found := strings.Cut(line, " ")
	if !found || path == "" || len(code) == 0 || len(code) > 2 {
		return false
	}
	for _, c := range code {
		if !strings.ContainsRune(porcelainStatusCodes, c) {
			return false
		}
	}
	return true
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
