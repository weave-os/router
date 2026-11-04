package main

import "go/ast"

// maxNesting returns the deepest control-statement nesting in body. An else-if chain
// counts as one level, and function literals are measured as their own scope.
func maxNesting(body *ast.BlockStmt) int {
	walker := nestingWalker{}
	walker.block(body, 0)
	return walker.deepest
}

type nestingWalker struct {
	deepest int
}

func (w *nestingWalker) block(node ast.Node, depth int) {
	ast.Inspect(node, func(child ast.Node) bool {
		if child == node {
			return true
		}
		switch stmt := child.(type) {
		case *ast.FuncLit:
			w.deepest = max(w.deepest, maxNesting(stmt.Body))
			return false
		case *ast.IfStmt:
			w.ifChain(stmt, depth+1)
			return false
		case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			w.deepest = max(w.deepest, depth+1)
			w.block(stmt, depth+1)
			return false
		}
		return true
	})
}

func (w *nestingWalker) ifChain(stmt *ast.IfStmt, depth int) {
	w.deepest = max(w.deepest, depth)
	if stmt.Init != nil {
		w.block(stmt.Init, depth)
	}
	w.block(stmt.Cond, depth)
	w.block(stmt.Body, depth)
	switch elseBranch := stmt.Else.(type) {
	case *ast.IfStmt:
		w.ifChain(elseBranch, depth)
	case *ast.BlockStmt:
		w.block(elseBranch, depth)
	}
}
