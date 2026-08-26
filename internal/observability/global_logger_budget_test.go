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
// Get() returns the global logger, which carries the service tag but NO
// request correlation (request_id / session_key / client_session_id). A line
// emitted through it on the request path cannot be found when filtering a
// session's logs — the defect this budget exists to stop from spreading.
//
// Every entry below is an off-request-path site (process startup, background
// listeners, fire-and-forget writebacks, pure helpers with no ctx or receiver
// to thread one through) where the global logger is the correct choice.
//
// Adding a request-path Get() call fails this test. The fix is to use
// observability.FromContext(ctx) — not to raise the number. Only lower these.
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
//
// This is the regression guard for correlation-tagged logs: the codebase
// previously drifted to 136 acquisition points that emitted lines with no
// request_id, which is why a session's logs could not be read end to end.
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
