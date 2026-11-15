package main

import (
	"go/ast"
	"go/token"
	"strings"
)

type kind string

const (
	kindDocExported   kind = "doc-exported"
	kindDocUnexported kind = "doc-unexported"
	kindPackageDoc    kind = "package-doc"
	kindTrailing      kind = "trailing"
	kindInline        kind = "inline"
	kindBlock         kind = "block"
	kindDirective     kind = "directive"
	kindBadDirective  kind = "directive-disallowed"
	kindGenerated     kind = "generated-header"
	kindLicense       kind = "license"
	kindExampleOutput kind = "example-output"
	kindCgoPreamble   kind = "cgo-preamble"
)

var allowedKinds = map[kind]bool{
	kindDirective:     true,
	kindGenerated:     true,
	kindExampleOutput: true,
	kindCgoPreamble:   true,
}

var allowedDirectivePrefixes = []string{"//go:", "//line ", "//export ", "//extern ", "// +build"}

type record struct {
	File       string `json:"file"`
	Package    string `json:"package"`
	Test       bool   `json:"test"`
	Kind       kind   `json:"kind"`
	Line       int    `json:"line"`
	Lines      int    `json:"lines"`
	Group      int    `json:"group"`
	Allowed    bool   `json:"allowed"`
	Deprecated bool   `json:"deprecated"`
	Text       string `json:"text"`
	start      int
	end        int
}

type fileInfo struct {
	path      string
	pkg       string
	test      bool
	src       []byte
	fset      *token.FileSet
	file      *ast.File
	generated bool
}

func classifyFile(info fileInfo) []record {
	docs := docKinds(info.file)
	examples := exampleBodies(info)
	var records []record
	for groupIndex, group := range info.file.Comments {
		groupKind, groupKnown := docs[group]
		deprecated := strings.Contains(group.Text(), "Deprecated:")
		for _, comment := range group.List {
			k := commentKind(info, group, comment, groupKind, groupKnown, examples)
			records = append(records, newRecord(info, comment, k, groupIndex, deprecated))
		}
	}
	return records
}

func newRecord(info fileInfo, comment *ast.Comment, k kind, group int, deprecated bool) record {
	start := info.fset.Position(comment.Pos())
	end := info.fset.Position(comment.End())
	return record{
		File:       info.path,
		Package:    info.pkg,
		Test:       info.test,
		Kind:       k,
		Line:       start.Line,
		Lines:      end.Line - start.Line + 1,
		Group:      group,
		Allowed:    allowedKinds[k],
		Deprecated: deprecated,
		Text:       comment.Text,
		start:      start.Offset,
		end:        end.Offset,
	}
}

func commentKind(info fileInfo, group *ast.CommentGroup, comment *ast.Comment, groupKind kind, groupKnown bool, examples []*ast.BlockStmt) kind {
	if k, ok := directiveKind(comment.Text); ok {
		return k
	}
	if info.generated {
		return kindGenerated
	}
	if group.End() < info.file.Package && isLicense(group.Text()) {
		return kindLicense
	}
	if groupKnown {
		return groupKind
	}
	if inExampleOutput(group, examples) {
		return kindExampleOutput
	}
	if strings.HasPrefix(comment.Text, "/*") {
		return kindBlock
	}
	if isTrailing(info.src, info.fset.Position(comment.Pos()).Offset) {
		return kindTrailing
	}
	return kindInline
}

func directiveKind(text string) (kind, bool) {
	for _, prefix := range allowedDirectivePrefixes {
		if strings.HasPrefix(text, prefix) {
			return kindDirective, true
		}
	}
	if isDirectiveSyntax(text) {
		return kindBadDirective, true
	}
	return "", false
}

func isDirectiveSyntax(text string) bool {
	body, ok := strings.CutPrefix(text, "//")
	if !ok {
		return false
	}
	name, _, found := strings.Cut(body, ":")
	if !found || name == "" {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func isLicense(text string) bool {
	return strings.Contains(text, "Copyright") || strings.Contains(text, "SPDX-License-Identifier") || strings.Contains(text, "Licensed under")
}

func isTrailing(src []byte, offset int) bool {
	for i := offset - 1; i >= 0 && src[i] != '\n'; i-- {
		if src[i] != ' ' && src[i] != '\t' {
			return true
		}
	}
	return false
}

func exampleBodies(info fileInfo) []*ast.BlockStmt {
	if !info.test {
		return nil
	}
	var bodies []*ast.BlockStmt
	for _, decl := range info.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && strings.HasPrefix(fn.Name.Name, "Example") {
			bodies = append(bodies, fn.Body)
		}
	}
	return bodies
}

func inExampleOutput(group *ast.CommentGroup, bodies []*ast.BlockStmt) bool {
	text := strings.TrimSpace(group.Text())
	if !strings.HasPrefix(text, "Output:") && !strings.HasPrefix(text, "Unordered output:") {
		return false
	}
	for _, body := range bodies {
		if group.Pos() > body.Lbrace && group.End() < body.Rbrace {
			return true
		}
	}
	return false
}
