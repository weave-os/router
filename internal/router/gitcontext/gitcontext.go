package gitcontext

import (
	"strings"
	"unicode/utf8"
)

type GitContext struct {
	Branch  string
	HeadSHA string
	Dirty   bool
}

type blockMarker string

const (
	markerBlock         blockMarker = "gitStatus:"
	markerCurrentBranch blockMarker = "Current branch:"
	markerMainBranch    blockMarker = "Main branch"
	markerGitUser       blockMarker = "Git user:"
	markerStatus        blockMarker = "Status:"
	markerRecentCommits blockMarker = "Recent commits:"
)

const cleanStatusPlaceholder = "(clean)"

const maxBlockBytes = 64 * 1024

const maxBranchBytes = 255

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

const porcelainStatusCodes = "MADRCUT?!"

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
