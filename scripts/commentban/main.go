package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var skippedDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "testdata": true, ".context": true}

func main() {
	mode := flag.String("mode", "check", "report, check or strip")
	root := flag.String("root", ".", "module root")
	tests := flag.Bool("tests", false, "include _test.go files")
	flag.Parse()
	rootDir = *root
	if err := run(os.Stdout, *mode, *root, *tests); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(w io.Writer, mode, root string, tests bool) error {
	files, err := loadFiles(root, tests)
	if err != nil {
		return err
	}
	switch mode {
	case "report":
		return report(w, files)
	case "check":
		return check(w, files)
	case "strip":
		return stripAll(files)
	}
	return fmt.Errorf("unknown mode %q", mode)
}

func loadFiles(root string, tests bool) ([]fileInfo, error) {
	var files []fileInfo
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && skippedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		isTest := strings.HasSuffix(path, "_test.go")
		if !strings.HasSuffix(path, ".go") || (isTest && !tests) {
			return nil
		}
		info, err := loadFile(root, path, isTest)
		files = append(files, info)
		return err
	})
	return files, err
}

func loadFile(root, path string, test bool) (fileInfo, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return fileInfo{}, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return fileInfo{}, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fileInfo{}, err
	}
	return fileInfo{path: rel, pkg: filepath.Dir(rel), test: test, src: src, fset: fset, file: file, generated: ast.IsGenerated(file)}, nil
}

func report(w io.Writer, files []fileInfo) error {
	encoder := json.NewEncoder(w)
	for _, info := range files {
		for _, rec := range classifyFile(info) {
			if err := encoder.Encode(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

func check(w io.Writer, files []fileInfo) error {
	violations := 0
	for _, info := range files {
		for _, rec := range classifyFile(info) {
			if rec.Allowed {
				continue
			}
			violations++
			if _, err := fmt.Fprintf(w, "%s:%d: %s comment is not allowed\n", rec.File, rec.Line, rec.Kind); err != nil {
				return err
			}
		}
	}
	if violations > 0 {
		return fmt.Errorf("%d disallowed comments; only //go: directives and generated headers may remain", violations)
	}
	return nil
}

var errNoChange = errors.New("no change")
