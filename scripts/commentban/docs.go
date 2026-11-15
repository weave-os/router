package main

import (
	"go/ast"
	"go/token"
)

func docKinds(file *ast.File) map[*ast.CommentGroup]kind {
	docs := map[*ast.CommentGroup]kind{}
	if file.Doc != nil {
		docs[file.Doc] = kindPackageDoc
	}
	ast.Inspect(file, func(node ast.Node) bool {
		group, exported, special := nodeDoc(node)
		if special != "" && group != nil {
			docs[group] = special
		} else if group != nil {
			docs[group] = docKind(exported)
		}
		return true
	})
	return docs
}

func nodeDoc(node ast.Node) (*ast.CommentGroup, bool, kind) {
	switch decl := node.(type) {
	case *ast.FuncDecl:
		return decl.Doc, decl.Name.IsExported(), ""
	case *ast.GenDecl:
		if isCgoImport(decl) {
			return decl.Doc, false, kindCgoPreamble
		}
		return decl.Doc, genDeclExported(decl), ""
	case *ast.TypeSpec:
		return decl.Doc, decl.Name.IsExported(), ""
	case *ast.ValueSpec:
		return decl.Doc, anyExported(decl.Names), ""
	case *ast.Field:
		return decl.Doc, fieldExported(decl), ""
	}
	return nil, false, ""
}

func docKind(exported bool) kind {
	if exported {
		return kindDocExported
	}
	return kindDocUnexported
}

func isCgoImport(decl *ast.GenDecl) bool {
	if decl.Tok != token.IMPORT {
		return false
	}
	for _, spec := range decl.Specs {
		if imp, ok := spec.(*ast.ImportSpec); ok && imp.Path.Value == `"C"` {
			return true
		}
	}
	return false
}

func genDeclExported(decl *ast.GenDecl) bool {
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name.IsExported() {
				return true
			}
		case *ast.ValueSpec:
			if anyExported(s.Names) {
				return true
			}
		}
	}
	return false
}

func anyExported(names []*ast.Ident) bool {
	for _, name := range names {
		if name.IsExported() {
			return true
		}
	}
	return false
}

func fieldExported(field *ast.Field) bool {
	if len(field.Names) > 0 {
		return anyExported(field.Names)
	}
	switch t := field.Type.(type) {
	case *ast.Ident:
		return t.IsExported()
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.IsExported()
		}
	case *ast.SelectorExpr:
		return t.Sel.IsExported()
	}
	return false
}
