package commentban

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

const (
	AllowlistPath = "internal/architecture/comment_ban_allowlist.json"
	SchemaVersion = "comment_ban_allowlist_v1"
	Fix           = "move what it says into a name, type or test (or docs/ for operator knowledge), then delete it"

	deprecatedParagraphPrefix = "// Deprecated:"
	codeQLSuppressionPrefix   = "// codeql["
	spdxHeaderPrefix          = "// SPDX-"
)

var allowedDirectivePrefixes = []string{"//go:", "//line ", "//export ", "//extern "}

type Comment struct {
	Line      int
	LineCount int
	Text      string
}

type Allowlist struct {
	SchemaVersion string        `json:"schema_version"`
	Packages      []PackageDebt `json:"packages"`
}

type PackageDebt struct {
	Package     string `json:"package"`
	BannedLines int    `json:"banned_lines"`
}

func ParseAllowlist(payload []byte) (Allowlist, error) {
	var allowlist Allowlist
	if err := json.Unmarshal(payload, &allowlist); err != nil {
		return Allowlist{}, fmt.Errorf("parse %s: %w", AllowlistPath, err)
	}
	return allowlist, nil
}

func (allowlist Allowlist) Budgets() map[string]int {
	budgets := make(map[string]int, len(allowlist.Packages))
	for _, debt := range allowlist.Packages {
		budgets[debt.Package] = debt.BannedLines
	}
	return budgets
}

func InSource(filename string, source []byte) ([]Comment, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, filename, source, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	if ast.IsGenerated(file) {
		return nil, nil
	}
	var banned []Comment
	for _, group := range file.Comments {
		inDeprecatedParagraph := false
		for _, comment := range group.List {
			inDeprecatedParagraph = continuesDeprecatedParagraph(comment.Text, inDeprecatedParagraph)
			if inDeprecatedParagraph || isAllowedComment(comment.Text) || isSPDXFileHeader(comment, file) {
				continue
			}
			start, end := fileSet.Position(comment.Pos()), fileSet.Position(comment.End())
			banned = append(banned, Comment{Line: start.Line, LineCount: end.Line - start.Line + 1, Text: comment.Text})
		}
	}
	return banned, nil
}

func continuesDeprecatedParagraph(text string, inParagraph bool) bool {
	if strings.HasPrefix(text, deprecatedParagraphPrefix) {
		return true
	}
	return inParagraph && strings.HasPrefix(text, "//") && strings.TrimSpace(text) != "//"
}

func isSPDXFileHeader(comment *ast.Comment, file *ast.File) bool {
	return comment.Pos() < file.Package && strings.HasPrefix(comment.Text, spdxHeaderPrefix)
}

func isAllowedComment(text string) bool {
	for _, prefix := range allowedDirectivePrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return strings.HasPrefix(text, codeQLSuppressionPrefix)
}
