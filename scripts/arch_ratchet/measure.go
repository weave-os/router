package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"hash/fnv"
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
	codeLines     int
	nolint        int
	modelMatching int
	funcs         []funcMetrics
}

// measureFiles measures every non-generated file; generated files have no metrics.
func measureFiles(sources map[string][]byte) (map[string]fileMetrics, error) {
	measured := make(map[string]fileMetrics, len(sources))
	for _, filePath := range sortedKeys(sources) {
		metrics, generated, err := measureFile(filePath, sources[filePath])
		if err != nil {
			return nil, err
		}
		if !generated {
			measured[filePath] = metrics
		}
	}
	return measured, nil
}

func measureFile(filePath string, src []byte) (metrics fileMetrics, generated bool, err error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return fileMetrics{}, false, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if ast.IsGenerated(file) {
		return fileMetrics{}, true, nil
	}
	codeLines := codeLineSet(src)
	metrics = fileMetrics{
		codeLines: len(codeLines),
		nolint:    countNolint(file),
	}
	if !strings.HasPrefix(filePath, catalogDir+"/") {
		metrics.modelMatching = countModelMatching(file)
	}
	dir := path.Dir(filePath)
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				key := funcKey(dir, filePath, file.Name.Name, d)
				metrics.funcs = append(metrics.funcs, measureFunc(fset, codeLines, key, d))
				metrics.funcs = append(metrics.funcs, measureFuncLits(fset, codeLines, key, d.Body)...)
			}
		case *ast.GenDecl:
			metrics.funcs = append(metrics.funcs, measurePackageFuncLits(fset, codeLines, dir, d)...)
		}
	}
	return metrics, false, nil
}

func measureFunc(fset *token.FileSet, codeLines map[int]bool, key string, fn *ast.FuncDecl) funcMetrics {
	return funcMetrics{
		key: key,
		values: map[metric]int{
			metricCognitive: gocognit.Complexity(fn),
			metricLines:     linesWithin(fset, fn, codeLines),
			metricParams:    fieldListSize(fn.Type.Params),
			metricNesting:   maxNesting(fn.Body),
		},
	}
}

// measureFuncLits measures the function literals directly inside body, keyed by a
// content hash so an inserted or edited literal never re-keys an unchanged one.
func measureFuncLits(fset *token.FileSet, codeLines map[int]bool, prefix string, body *ast.BlockStmt) []funcMetrics {
	var funcs []funcMetrics
	ast.Inspect(body, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		fn := &ast.FuncDecl{Name: ast.NewIdent("func"), Type: lit.Type, Body: lit.Body}
		funcs = append(funcs, measureFunc(fset, codeLines, funcLitKey(prefix, lit), fn))
		return false
	})
	return funcs
}

// funcLitKey identifies a function literal by its rendered source, so it keeps its
// identity when other literals are inserted before it and counts as a move (not a
// rename) when its own body is edited.
func funcLitKey(prefix string, lit *ast.FuncLit) string {
	var src bytes.Buffer
	_ = printer.Fprint(&src, token.NewFileSet(), lit)
	sum := fnv.New32a()
	_, _ = sum.Write(src.Bytes())
	return fmt.Sprintf("%s.func%x", prefix, sum.Sum32())
}

// measurePackageFuncLits measures function literals in package-level var and const
// values, keyed <dir>.<var>.func<hash>.
func measurePackageFuncLits(fset *token.FileSet, codeLines map[int]bool, dir string, decl *ast.GenDecl) []funcMetrics {
	var funcs []funcMetrics
	for _, spec := range decl.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		name := value.Names[0].Name
		for _, expr := range value.Values {
			ast.Inspect(expr, func(node ast.Node) bool {
				lit, ok := node.(*ast.FuncLit)
				if !ok {
					return true
				}
				fn := &ast.FuncDecl{Name: ast.NewIdent(name), Type: lit.Type, Body: lit.Body}
				funcs = append(funcs, measureFunc(fset, codeLines, funcLitKey(dir+"."+name, lit), fn))
				return false
			})
		}
	}
	return funcs
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
		startLine := file.Line(pos)
		for line := startLine; line <= startLine+strings.Count(lit, "\n"); line++ {
			lines[line] = true
		}
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
// between files of one package keeps its identity. An external test package (foo_test)
// is qualified so its helpers can't reuse the tested package's keys.
func funcKey(dir, filePath, pkg string, fn *ast.FuncDecl) string {
	name := fn.Name.Name
	if pkg != path.Base(dir) {
		name = pkg + "." + name
	}
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

// fieldListSize counts declared names, counting an unnamed or embedded field as one.
func fieldListSize(fields *ast.FieldList) int {
	count := 0
	for _, field := range fields.List {
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
	var name string
	switch e := expr.(type) {
	case *ast.Ident:
		name = e.Name
	case *ast.SelectorExpr:
		name = e.Sel.Name
	case *ast.CallExpr:
		return len(e.Args) == 1 && isStringsCall(e, normalizers) && namesModelOrProvider(e.Args[0])
	}
	lower := strings.ToLower(name)
	return strings.Contains(lower, "model") || strings.Contains(lower, "provider")
}
