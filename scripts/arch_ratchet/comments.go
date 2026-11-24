package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"weave-os/router/internal/architecture/commentban"
)

const (
	metricNewComment    metric = "new production comment"
	metricCommentBudget metric = "comment ban budget"
)

type locatedComment struct {
	file    string
	comment commentban.Comment
}

func (c locatedComment) packageText() string {
	return path.Dir(c.file) + "\x00" + c.comment.Text
}

func compareComments(baseSources, headSources map[string][]byte) ([]violation, error) {
	base, err := productionComments(baseSources)
	if err != nil {
		return nil, err
	}
	head, err := productionComments(headSources)
	if err != nil {
		return nil, err
	}
	remaining := map[string]int{}
	for _, comment := range base {
		remaining[comment.packageText()]++
	}
	var violations []violation
	for _, comment := range head {
		if remaining[comment.packageText()] > 0 {
			remaining[comment.packageText()]--
			continue
		}
		violations = append(violations, violation{
			subject: fmt.Sprintf("%s:%d", comment.file, comment.comment.Line),
			metric:  metricNewComment,
			note:    commentban.Fix,
		})
	}
	return violations, nil
}

func productionComments(sources map[string][]byte) ([]locatedComment, error) {
	var found []locatedComment
	for _, filePath := range sortedKeys(sources) {
		if strings.HasSuffix(filePath, "_test.go") {
			continue
		}
		comments, err := commentban.InSource(filePath, sources[filePath])
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filePath, err)
		}
		for _, comment := range comments {
			found = append(found, locatedComment{file: filePath, comment: comment})
		}
	}
	return found, nil
}

func compareCommentBudgets(ctx context.Context, r repo, baseRev string, allowed exceptionSet) ([]violation, error) {
	baseRaw, err := r.readAt(ctx, baseRev, []string{commentban.AllowlistPath})
	if err != nil {
		return nil, err
	}
	basePayload, tracked := baseRaw[commentban.AllowlistPath]
	if !tracked {
		return nil, nil
	}
	base, err := commentban.ParseAllowlist(basePayload)
	if err != nil {
		return nil, err
	}
	headPayload, err := os.ReadFile(filepath.Join(r.root, commentban.AllowlistPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", commentban.AllowlistPath, err)
	}
	head, err := commentban.ParseAllowlist(headPayload)
	if err != nil {
		return nil, err
	}
	baseBudgets := base.Budgets()
	var violations []violation
	for _, debt := range head.Packages {
		if before := baseBudgets[debt.Package]; debt.BannedLines > before && !allowed.permits(debt.Package, metricCommentBudget, debt.BannedLines) {
			violations = append(violations, violation{subject: debt.Package, metric: metricCommentBudget, before: before, after: debt.BannedLines, limit: -1})
		}
	}
	return violations, nil
}
