package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"strings"

	"github.com/uudashr/gocognit"
)

type metric string

const (
	metricCognitive      metric = "cognitive"
	metricLines          metric = "lines"
	metricParams         metric = "params"
	metricNesting        metric = "nesting"
	metricFileLines      metric = "file_lines"
	metricNolint         metric = "nolint"
	metricModelMatching  metric = "model_matching"
	metricServiceFields  metric = "service_fields"
	metricServiceMethods metric = "service_methods"
	metricContextKeys    metric = "context_keys"
)

// The limits mirror the weave qa gate (clippy's cognitive_complexity, too_many_lines and
// too_many_arguments defaults, and its guard's file limit).
var funcLimits = map[metric]int{
	metricCognitive: 25,
	metricLines:     100,
	metricParams:    7,
	metricNesting:   5,
}

const limitFileLines = 500

const catalogDir = "internal/router/catalog"

type funcMetrics struct {
	key    string
	values map[metric]int
}

type fileMetrics struct {
	path          string
	codeLines     int
	nolint        int
	modelMatching int
	funcs         []funcMetrics
}

// measureFiles measures every non-generated file; generated files have no metrics.
func measureFiles(sources map[string][]byte) (map[string]fileMetrics, error) {
	measured := make(map[string]fileMetrics, len(sources))
	for _, filePath := range sortedKeys(sources) {
		src := sources[filePath]
		if isGenerated(src) {
			continue
		}
		metrics, err := measureFile(filePath, src)
		if err != nil {
			return nil, err
		}
		measured[filePath] = metrics
	}
	return measured, nil
}

func measureFile(filePath string, src []byte) (fileMetrics, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return fileMetrics{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	codeLines := codeLineSet(src)
	metrics := fileMetrics{
		path:      filePath,
		codeLines: len(codeLines),
		nolint:    countNolint(file),
	}
	if !strings.HasPrefix(filePath, catalogDir+"/") {
		metrics.modelMatching = countModelMatching(file)
	}
	dir := path.Dir(filePath)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		metrics.funcs = append(metrics.funcs, funcMetrics{
			key: funcKey(dir, filePath, fn),
			values: map[metric]int{
				metricCognitive: gocognit.Complexity(fn),
				metricLines:     linesWithin(fset, fn, codeLines),
				metricParams:    paramCount(fn.Type.Params),
				metricNesting:   maxNesting(fn.Body),
			},
		})
	}
	return metrics, nil
}

func isGenerated(src []byte) bool {
	head := src[:min(len(src), 2048)]
	return bytes.Contains(head, []byte("Code generated")) && bytes.Contains(head, []byte("DO NOT EDIT"))
}

// codeLineSet returns the lines holding at least one non-comment token.
func codeLineSet(src []byte) map[int]bool {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	lines := map[int]bool{}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return lines
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		lines[file.Line(pos)] = true
	}
}

func linesWithin(fset *token.FileSet, fn *ast.FuncDecl, codeLines map[int]bool) int {
	count := 0
	for line := fset.Position(fn.Pos()).Line; line <= fset.Position(fn.End()).Line; line++ {
		if codeLines[line] {
			count++
		}
	}
	return count
}

// funcKey identifies a function by package directory, receiver and name, so moving it
// between files of one package keeps its identity.
func funcKey(dir, filePath string, fn *ast.FuncDecl) string {
	name := fn.Name.Name
	if fn.Recv == nil && name == "init" {
		return dir + ".init@" + path.Base(filePath)
	}
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		name = receiverName(fn.Recv.List[0].Type) + "." + name
	}
	return dir + "." + name
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

func paramCount(params *ast.FieldList) int {
	count := 0
	for _, field := range params.List {
		count += max(len(field.Names), 1)
	}
	return count
}

func countNolint(file *ast.File) int {
	count := 0
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//nolint") {
				count++
			}
		}
	}
	return count
}

var nameMatchers = map[string]bool{"HasPrefix": true, "HasSuffix": true, "Contains": true, "EqualFold": true}

// countModelMatching counts string matches of a model- or provider-named value against
// a literal, the pattern that hard-codes model quirks outside catalog data.
func countModelMatching(file *ast.File) int {
	count := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 || !isStringsCall(call, nameMatchers) {
			return true
		}
		if _, literal := call.Args[1].(*ast.BasicLit); literal && namesModelOrProvider(call.Args[0]) {
			count++
		}
		return true
	})
	return count
}

func isStringsCall(call *ast.CallExpr, names map[string]bool) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "strings" && names[selector.Sel.Name]
}

var normalizers = map[string]bool{"ToLower": true, "ToUpper": true, "TrimSpace": true}

func namesModelOrProvider(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return mentionsModelOrProvider(e.Name)
	case *ast.SelectorExpr:
		return mentionsModelOrProvider(e.Sel.Name)
	case *ast.CallExpr:
		return len(e.Args) == 1 && isStringsCall(e, normalizers) && namesModelOrProvider(e.Args[0])
	}
	return false
}

func mentionsModelOrProvider(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "model") || strings.Contains(lower, "provider")
}
