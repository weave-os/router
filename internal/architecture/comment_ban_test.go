package architecture_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	commentBanAllowlistPath   = "comment_ban_allowlist.json"
	commentBanSchemaVersion   = "comment_ban_allowlist_v1"
	commentBanFix             = "move what it says into a name, type or test (or docs/ for operator knowledge), then delete it"
	commentBanFixturePackage  = "testdata/comment_ban"
	deprecatedParagraphPrefix = "// Deprecated:"
)

var (
	allowedDirectivePrefixes = []string{"//go:", "//line ", "//export ", "//extern "}
	skippedSourceDirectories = map[string]bool{"node_modules": true, "vendor": true, "testdata": true}
)

type bannedComment struct {
	File  string
	Line  int
	Lines int
}

type commentBanAllowlist struct {
	SchemaVersion string                  `json:"schema_version"`
	Packages      []commentBanPackageDebt `json:"packages"`
}

type commentBanPackageDebt struct {
	Package     string `json:"package"`
	BannedLines int    `json:"banned_lines"`
}

func TestProductionGoHasNoComments(t *testing.T) {
	root := repositoryRoot(t)
	found := bannedCommentsByPackage(t, root, root)
	allowlist := readCommentBanAllowlist(t)

	var failures []string
	budgets := make(map[string]int, len(allowlist.Packages))
	for _, debt := range allowlist.Packages {
		budgets[debt.Package] = debt.BannedLines
		failures = append(failures, allowlistedPackageFailure(debt, bannedLineCount(found[debt.Package]))...)
	}
	for _, pkg := range sortedKeys(found) {
		if _, listed := budgets[pkg]; listed {
			continue
		}
		for _, comment := range found[pkg] {
			failures = append(failures, fmt.Sprintf("%s:%d: comments are banned in production Go; %s", comment.File, comment.Line, commentBanFix))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("comment ban:\n%s", strings.Join(failures, "\n"))
	}
}

func TestCommentBanAllowlistIsSortedAndUnique(t *testing.T) {
	allowlist := readCommentBanAllowlist(t)
	require.Equal(t, commentBanSchemaVersion, allowlist.SchemaVersion)
	names := make([]string, len(allowlist.Packages))
	for index, debt := range allowlist.Packages {
		names[index] = debt.Package
		assert.Positive(t, debt.BannedLines, "%s needs a positive banned_lines budget", debt.Package)
	}
	assert.True(t, slices.IsSorted(names), "comment ban allowlist packages must be sorted")
	assert.Len(t, slices.Compact(slices.Clone(names)), len(names), "comment ban allowlist packages must be unique")
}

func TestCommentBanKeepsOnlyToolAndSuppressionComments(t *testing.T) {
	root := repositoryRoot(t)
	found := bannedCommentsByPackage(t, filepath.Join(root, "internal", "architecture", commentBanFixturePackage), root)
	file := "internal/architecture/testdata/comment_ban/banned/banned.go"

	assert.Equal(t, map[string][]bannedComment{
		"internal/architecture/testdata/comment_ban/banned": {
			{File: file, Line: 1, Lines: 1},
			{File: file, Line: 4, Lines: 2},
			{File: file, Line: 9, Lines: 1},
			{File: file, Line: 10, Lines: 1},
			{File: file, Line: 11, Lines: 1},
			{File: file, Line: 14, Lines: 1},
			{File: file, Line: 15, Lines: 1},
			{File: file, Line: 21, Lines: 1},
			{File: file, Line: 24, Lines: 1},
			{File: file, Line: 27, Lines: 1},
		},
	}, found)
}

func TestCommentBanAllowlistOnlyShrinks(t *testing.T) {
	debt := commentBanPackageDebt{Package: "internal/example", BannedLines: 10}
	comment := bannedComment{File: "internal/example/example.go", Line: 3, Lines: 1}

	assert.Empty(t, allowlistedPackageFailure(debt, 10))
	assert.Equal(t, []string{"internal/example has 11 banned comment lines but its budget in internal/architecture/comment_ban_allowlist.json is 10: new comments are banned in packages awaiting migration too; " + commentBanFix}, allowlistedPackageFailure(debt, 11))
	assert.Equal(t, []string{"internal/example has 9 banned comment lines: lower its banned_lines in internal/architecture/comment_ban_allowlist.json from 10 to 9, the allowlist only shrinks"}, allowlistedPackageFailure(debt, 9))
	assert.Equal(t, []string{"internal/example has no banned comments left: remove it from internal/architecture/comment_ban_allowlist.json"}, allowlistedPackageFailure(debt, bannedLineCount(nil)))
	assert.Equal(t, 1, bannedLineCount([]bannedComment{comment}))
}

func allowlistedPackageFailure(debt commentBanPackageDebt, lines int) []string {
	location := "internal/architecture/" + commentBanAllowlistPath
	switch {
	case lines == 0:
		return []string{fmt.Sprintf("%s has no banned comments left: remove it from %s", debt.Package, location)}
	case lines > debt.BannedLines:
		return []string{fmt.Sprintf("%s has %d banned comment lines but its budget in %s is %d: new comments are banned in packages awaiting migration too; %s", debt.Package, lines, location, debt.BannedLines, commentBanFix)}
	case lines < debt.BannedLines:
		return []string{fmt.Sprintf("%s has %d banned comment lines: lower its banned_lines in %s from %d to %d, the allowlist only shrinks", debt.Package, lines, location, debt.BannedLines, lines)}
	}
	return nil
}

func bannedCommentsByPackage(t *testing.T, scanRoot, root string) map[string][]bannedComment {
	t.Helper()
	found := map[string][]bannedComment{}
	err := filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != scanRoot && skippedSourceDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		comments, err := bannedCommentsInFile(path, filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		if len(comments) > 0 {
			pkg := filepath.ToSlash(filepath.Dir(relative))
			found[pkg] = append(found[pkg], comments...)
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

func skippedSourceDirectory(name string) bool {
	return skippedSourceDirectories[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func bannedCommentsInFile(path, relative string) ([]bannedComment, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	if ast.IsGenerated(file) {
		return nil, nil
	}
	var banned []bannedComment
	for _, group := range file.Comments {
		inDeprecatedParagraph := false
		for _, comment := range group.List {
			inDeprecatedParagraph = continuesDeprecatedParagraph(comment.Text, inDeprecatedParagraph)
			if inDeprecatedParagraph || allowedComment(comment.Text) {
				continue
			}
			start, end := fileSet.Position(comment.Pos()), fileSet.Position(comment.End())
			banned = append(banned, bannedComment{File: relative, Line: start.Line, Lines: end.Line - start.Line + 1})
		}
	}
	return banned, nil
}

func continuesDeprecatedParagraph(text string, inParagraph bool) bool {
	if strings.HasPrefix(text, deprecatedParagraphPrefix) {
		return true
	}
	return inParagraph && strings.TrimSpace(text) != "//"
}

func allowedComment(text string) bool {
	for _, prefix := range allowedDirectivePrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return strings.HasPrefix(text, "// codeql[")
}

func bannedLineCount(comments []bannedComment) int {
	lines := 0
	for _, comment := range comments {
		lines += comment.Lines
	}
	return lines
}

func sortedKeys(found map[string][]bannedComment) []string {
	keys := make([]string, 0, len(found))
	for key := range found {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func readCommentBanAllowlist(t *testing.T) commentBanAllowlist {
	t.Helper()
	payload, err := os.ReadFile(commentBanAllowlistPath)
	require.NoError(t, err)
	var allowlist commentBanAllowlist
	require.NoError(t, json.Unmarshal(payload, &allowlist))
	return allowlist
}
