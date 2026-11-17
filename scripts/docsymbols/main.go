package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
)

func main() {
	symbols, err := declaredSymbols(bufio.NewScanner(os.Stdin))
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(symbols)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func declaredSymbols(paths *bufio.Scanner) (map[string][]string, error) {
	byPackage := map[string]map[string]bool{}
	for paths.Scan() {
		file, err := parser.ParseFile(token.NewFileSet(), paths.Text(), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		names := byPackage[file.Name.Name]
		if names == nil {
			names = map[string]bool{}
			byPackage[file.Name.Name] = names
		}
		ast.Inspect(file, func(node ast.Node) bool {
			for _, ident := range declaredIdents(node) {
				names[ident.Name] = true
			}
			return true
		})
	}
	if err := paths.Err(); err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(byPackage))
	for pkg, names := range byPackage {
		sorted := make([]string, 0, len(names))
		for name := range names {
			sorted = append(sorted, name)
		}
		slices.Sort(sorted)
		result[pkg] = sorted
	}
	return result, nil
}

func declaredIdents(node ast.Node) []*ast.Ident {
	switch declaration := node.(type) {
	case *ast.FuncDecl:
		return []*ast.Ident{declaration.Name}
	case *ast.TypeSpec:
		return []*ast.Ident{declaration.Name}
	case *ast.ValueSpec:
		return declaration.Names
	case *ast.StructType:
		return fieldNames(declaration.Fields)
	case *ast.InterfaceType:
		return fieldNames(declaration.Methods)
	}
	return nil
}

func fieldNames(fields *ast.FieldList) []*ast.Ident {
	var names []*ast.Ident
	for _, field := range fields.List {
		names = append(names, field.Names...)
	}
	return names
}
