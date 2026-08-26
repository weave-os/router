package observability_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// globalLoggerBudget caps observability.Get() call sites per package.
//
// Get() returns the global logger: no request_id/session_key. A line emitted
// through it on the request path cannot be found when filtering by session.
//
// Use observability.FromContext(ctx) for request-path sites. Only lower these.
var globalLoggerBudget = map[string]int{
	// Composition root: no request exists yet.
	"cmd/router": 11,

	// Pure schema-emit / validation helpers with no ctx in scope. Threading a
	// ctx through these is a wider refactor; they are diagnostics about a
	// tool schema, not about a request's fate.
	"internal/translate":           13,
	"internal/translate/toolcheck": 5,

	// The exporter itself: logging its own transport failures. A ctx here
	// would be the export's, not the request's.
	"internal/observability/otel": 8,
	"internal/observability/apm":  1,

	// Background Pub/Sub listeners; no inbound request.
	"internal/pubsub": 4,

	// Fire-and-forget writebacks (SafeGo) + a pure predicate. Documented in
	// root CLAUDE.md as the off-request-path exception.
	"internal/proxy": 4,
	"internal/auth":  2,

	// Allocation failures inside LRU construction and a row-parse fallback:
	// no ctx parameter at these call sites.
	"internal/router/cache":         2,
	"internal/router/banditexplore": 1,
	"internal/postgres":             1,
	"internal/billing":              1,
}

// TestGlobalLoggerBudget fails when a package gains an untagged log site.
func TestGlobalLoggerBudget(t *testing.T) {
	root := repoRoot(t)
	counts := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "node_modules", "smoke":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		n := countGlobalLoggerCalls(t, path)
		if n == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		counts[filepath.ToSlash(rel)] += n
		return nil
	})
	require.NoError(t, err)

	for pkg, got := range counts {
		budget, ok := globalLoggerBudget[pkg]
		assert.True(t, ok,
			"package %q gained %d observability.Get() call(s) with no budget entry. "+
				"Use observability.FromContext(ctx) so the line carries request_id/session_key; "+
				"only add a budget entry if the site is genuinely off the request path.", pkg, got)
		if ok {
			assert.LessOrEqual(t, got, budget,
				"package %q has %d observability.Get() calls, budget is %d. "+
					"Use observability.FromContext(ctx) instead of raising the budget.", pkg, got, budget)
		}
	}

	// A stale entry means someone did the work but left the budget high,
	// which would silently re-admit new untagged sites.
	for pkg, budget := range globalLoggerBudget {
		if got := counts[pkg]; got < budget {
			t.Errorf("package %q budget is %d but only %d observability.Get() calls remain; "+
				"lower the budget to %d to lock in the improvement", pkg, budget, got, got)
		}
	}
}

// countGlobalLoggerCalls counts observability.Get() selector calls in one file.
// Counting via the AST rather than grep so a comment mentioning the call, or a
// call split across lines, is scored correctly.
func countGlobalLoggerCalls(t *testing.T, path string) int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse %s", path)

	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Get" {
			return true
		}
		// Match both the qualified form from other packages and the bare
		// in-package call.
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "observability" {
			count++
		}
		return true
	})
	return count
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found walking up from test dir")
		dir = parent
	}
}

// alwaysBoundKeys are bound on every HTTP path: Middleware binds request_id,
// initLogger binds name. slog does not dedupe: re-passing one emits the key
// twice and JSON consumers keep the LAST value, overwriting the bound field.
var alwaysBoundKeys = []string{"request_id", "name"}

// bindScopedKeys are bound by bindRequestLogger, which runs inside the proxy
// handler. Code that runs BEFORE it (route middleware) is adding the field,
// not shadowing it — passing api_key_id there is correct and must not be
// renamed, so these are only checked on the post-bind request path.
var bindScopedKeys = []string{"session_key", "client_session_id", "api_key_id", "ingress"}

// postBindPrefixes are packages reached only after bindRequestLogger has run.
var postBindPrefixes = []string{
	"internal/proxy/",
	"internal/providers/",
	"internal/translate/",
}

// reservedKeyAllowlist are sites that bind a reserved key deliberately (the
// binder itself, or a logger with no bound value to collide with).
var reservedKeyAllowlist = map[string]bool{
	"internal/observability/logger.go":   true, // Middleware binds them
	"internal/proxy/session_key.go":      true, // bindRequestLogger binds them
	"internal/auth/service.go":           true, // SafeGo loggers, off request path
	"internal/proxy/service.go":          true, // OTel span attrs + telemetry rows
	"internal/proxy/usage_bypass.go":     true, // OTel span attrs + telemetry rows
	"internal/api/anthropic/messages.go": true, // pre-Middleware oversize-body log
}

// TestNoReservedLogKeyShadowing fails when a log call passes a key already
// bound to that logger, which would overwrite the bound value.
func TestNoReservedLogKeyShadowing(t *testing.T) {
	root := repoRoot(t)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "node_modules", "smoke", "scripts":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if reservedKeyAllowlist[rel] || strings.HasPrefix(rel, "cmd/") {
			return nil
		}

		check := append([]string{}, alwaysBoundKeys...)
		for _, prefix := range postBindPrefixes {
			if strings.HasPrefix(rel, prefix) {
				check = append(check, bindScopedKeys...)
				break
			}
		}

		for _, key := range findShadowedKeys(t, path, check) {
			t.Errorf("%s passes log key %q, which is already bound to the request "+
				"logger here. slog keeps the last value, so this overwrites the bound "+
				"field and makes the line unfindable by it. Rename to a distinct key "+
				"(upstream_%s / rated_%s / tool_%s), or drop it if the value is "+
				"identical to the bound one.", rel, key, key, key, key)
		}
		return nil
	})
	require.NoError(t, err)
}

// findShadowedKeys returns which of keys are passed as literal args to a
// .Debug/.Info/.Warn/.Error call in the file.
func findShadowedKeys(t *testing.T, path string, keys []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse %s", path)

	reserved := map[string]bool{}
	for _, k := range keys {
		reserved[k] = true
	}

	var found []string
	seen := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Debug", "Info", "Warn", "Error", "DebugContext", "InfoContext", "WarnContext", "ErrorContext":
		default:
			return true
		}
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val := strings.Trim(lit.Value, `"`)
			if reserved[val] && !seen[val] {
				seen[val] = true
				found = append(found, val)
			}
		}
		return true
	})
	return found
}
