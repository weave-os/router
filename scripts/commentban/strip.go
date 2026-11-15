package main

import (
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
)

type span struct{ start, end int }

func stripAll(files []fileInfo) error {
	for _, info := range files {
		if info.generated {
			continue
		}
		out, err := stripFile(info)
		if errors.Is(err, errNoChange) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", info.path, err)
		}
		if err := os.WriteFile(filepath.Join(rootFromFlags(), info.path), out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func rootFromFlags() string {
	return rootDir
}

var rootDir = "."

func stripFile(info fileInfo) ([]byte, error) {
	var spans []span
	for _, rec := range classifyFile(info) {
		if !rec.Allowed {
			spans = append(spans, deletionSpan(info.src, rec.start, rec.end))
		}
	}
	if len(spans) == 0 {
		return nil, errNoChange
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	src := append([]byte(nil), info.src...)
	limit := len(src)
	for _, s := range spans {
		if s.end > limit {
			s.end = limit
		}
		src = append(src[:s.start], src[s.end:]...)
		limit = s.start
	}
	return format.Source(src)
}

func deletionSpan(src []byte, start, end int) span {
	lineStart := start
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}
	lineEnd := end
	for lineEnd < len(src) && src[lineEnd] != '\n' {
		lineEnd++
	}
	if blank(src[lineStart:start]) && blank(src[end:lineEnd]) {
		if lineEnd < len(src) {
			lineEnd++
		}
		return span{lineStart, lineEnd}
	}
	for start > lineStart && (src[start-1] == ' ' || src[start-1] == '\t') {
		start--
	}
	return span{start, end}
}

func blank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}
