package architecture_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/architecture/commentban"
)

const commentBanFixturePackage = "testdata/comment_ban"

var skippedSourceDirectories = map[string]bool{"node_modules": true, "vendor": true, "testdata": true}

type bannedComment struct {
	File      string
	Line      int
	LineCount int
}

func TestProductionGoHasNoComments(t *testing.T) {
	root := repositoryRoot(t)
	found := bannedCommentsByPackage(t, root, root)
	allowlist := readCommentBanAllowlist(t)

	var failures []string
	budgets := allowlist.Budgets()
	for _, debt := range allowlist.Packages {
		failures = append(failures, allowlistedPackageFailure(debt, bannedLineCount(found[debt.Package]))...)
	}
	for _, pkg := range sortedKeys(found) {
		if _, listed := budgets[pkg]; listed {
			continue
		}
		for _, comment := range found[pkg] {
			failures = append(failures, fmt.Sprintf("%s:%d: comments are banned in production Go; %s", comment.File, comment.Line, commentban.Fix))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("comment ban:\n%s", strings.Join(failures, "\n"))
	}
}

func TestCommentBanAllowlistIsSortedAndUnique(t *testing.T) {
	allowlist := readCommentBanAllowlist(t)
	require.Equal(t, commentban.SchemaVersion, allowlist.SchemaVersion)
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
			{File: file, Line: 1, LineCount: 1},
			{File: file, Line: 4, LineCount: 2},
			{File: file, Line: 9, LineCount: 1},
			{File: file, Line: 10, LineCount: 1},
			{File: file, Line: 11, LineCount: 1},
			{File: file, Line: 14, LineCount: 1},
			{File: file, Line: 15, LineCount: 1},
			{File: file, Line: 21, LineCount: 1},
			{File: file, Line: 24, LineCount: 1},
			{File: file, Line: 27, LineCount: 1},
			{File: file, Line: 30, LineCount: 1},
		},
	}, found)
}

func TestCommentBanAllowlistOnlyShrinks(t *testing.T) {
	debt := commentban.PackageDebt{Package: "internal/example", BannedLines: 10}
	comment := bannedComment{File: "internal/example/example.go", Line: 3, LineCount: 1}

	assert.Empty(t, allowlistedPackageFailure(debt, 10))
	assert.Equal(t, []string{"internal/example has 11 banned comment lines but its budget in internal/architecture/comment_ban_allowlist.json is 10: new comments are banned in packages awaiting migration too; " + commentban.Fix}, allowlistedPackageFailure(debt, 11))
	assert.Equal(t, []string{"internal/example has 9 banned comment lines: lower its banned_lines in internal/architecture/comment_ban_allowlist.json from 10 to 9, the allowlist only shrinks"}, allowlistedPackageFailure(debt, 9))
	assert.Equal(t, []string{"internal/example has no banned comments left: remove it from internal/architecture/comment_ban_allowlist.json"}, allowlistedPackageFailure(debt, bannedLineCount(nil)))
	assert.Equal(t, 1, bannedLineCount([]bannedComment{comment}))
}

func allowlistedPackageFailure(debt commentban.PackageDebt, lines int) []string {
	location := commentban.AllowlistPath
	switch {
	case lines == 0:
		return []string{fmt.Sprintf("%s has no banned comments left: remove it from %s", debt.Package, location)}
	case lines > debt.BannedLines:
		return []string{fmt.Sprintf("%s has %d banned comment lines but its budget in %s is %d: new comments are banned in packages awaiting migration too; %s", debt.Package, lines, location, debt.BannedLines, commentban.Fix)}
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
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	comments, err := commentban.InSource(path, source)
	if err != nil {
		return nil, err
	}
	banned := make([]bannedComment, len(comments))
	for index, comment := range comments {
		banned[index] = bannedComment{File: relative, Line: comment.Line, LineCount: comment.LineCount}
	}
	return banned, nil
}

func bannedLineCount(comments []bannedComment) int {
	lines := 0
	for _, comment := range comments {
		lines += comment.LineCount
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

func readCommentBanAllowlist(t *testing.T) commentban.Allowlist {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(repositoryRoot(t), commentban.AllowlistPath))
	require.NoError(t, err)
	allowlist, err := commentban.ParseAllowlist(payload)
	require.NoError(t, err)
	return allowlist
}
