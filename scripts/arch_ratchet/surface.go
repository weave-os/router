package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

const proxyDir = "internal/proxy"

type proxySurface struct {
	serviceFields  int
	serviceMethods int
	contextKeys    int
}

// measureSurface counts proxy.Service's fields and methods and the distinct keys
// internal/proxy stores in a context.
func measureSurface(sources map[string][]byte) (proxySurface, error) {
	surface := proxySurface{}
	keys := map[string]bool{}
	for _, filePath := range sortedKeys(sources) {
		file, err := parser.ParseFile(token.NewFileSet(), filePath, sources[filePath], parser.ParseComments)
		if err != nil {
			return proxySurface{}, err
		}
		if ast.IsGenerated(file) {
			continue
		}
		surface.serviceFields += serviceFieldCount(file)
		surface.serviceMethods += serviceMethodCount(file)
		collectContextKeys(file, keys)
	}
	surface.contextKeys = len(keys)
	return surface, nil
}

func serviceFieldCount(file *ast.File) int {
	count := 0
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Service" {
			return true
		}
		if structType, isStruct := spec.Type.(*ast.StructType); isStruct {
			count += fieldListSize(structType.Fields)
		}
		return false
	})
	return count
}

func serviceMethodCount(file *ast.File) int {
	count := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv != nil && len(fn.Recv.List) > 0 && receiverName(fn.Recv.List[0].Type) == "Service" {
			count++
		}
	}
	return count
}

func collectContextKeys(file *ast.File, keys map[string]bool) {
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 3 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "WithValue" {
			return true
		}
		if pkg, isIdent := selector.X.(*ast.Ident); isIdent && pkg.Name == "context" {
			keys[types.ExprString(call.Args[1])] = true
		}
		return true
	})
}
