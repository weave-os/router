package architecture_test

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

var ioFreePackagePatterns = []string{
	"./internal/inference/...",
	"./internal/credentials/...",
	"./internal/router",
	"./internal/router/breaker/...",
	"./internal/router/catalog/...",
	"./internal/router/handover/...",
	"./internal/router/planner/...",
	"./internal/router/policy/...",
	"./internal/router/sessionpin/...",
	"./internal/router/turntype/...",
	"./internal/providers",
	"./internal/translate/...",
	"./internal/sse/...",
	"./internal/timing/...",
	"./internal/telemetry/...",
	"./internal/requestcontext/...",
	"./internal/feedback/...",
	"./internal/proxy/usage/...",
	"./internal/routingpolicy/...",
}

var injectedClockPackagePatterns = []string{
	"./internal/auth/...",
	"./internal/proxy/usage/...",
	"./internal/router/planner/...",
}

var safeGoPackagePatterns = []string{
	"./internal/auth/...",
	"./internal/proxy/...",
	"./internal/pubsub/...",
}

var netHTTPTransportAPI = map[string]bool{
	"Client": true, "Transport": true, "Server": true, "DefaultClient": true, "DefaultTransport": true,
	"Get": true, "Post": true, "Head": true, "PostForm": true, "NewRequest": true, "NewRequestWithContext": true,
	"ListenAndServe": true, "ListenAndServeTLS": true, "Serve": true, "ServeTLS": true,
}

func TestIOFreePackagesDoNoIO(t *testing.T) {
	findings := ioUses(t, repositoryRoot(t), ioFreePackagePatterns...)
	assert.Empty(t, findings, "I/O-free inner-ring packages must not touch HTTP, sockets, databases, queues or the filesystem: declare a port (interface) here and implement it in an adapter package or proxy.Service, wired in cmd/router (see .agents/skills/add-context-port)")
}

func TestProviderAdaptersBuildClientsWithHTTPUtil(t *testing.T) {
	root := repositoryRoot(t)
	findings := rawProviderClients(t, root, providerAdapterPackages(t, root)...)
	assert.Empty(t, findings, "provider adapters build their client with httputil.NewClient(httputil.NewTransport(...)) so redirects are refused and the managed-mode destination policy applies")
}

func TestInjectedClockPackagesDoNotReadWallClock(t *testing.T) {
	findings := wallClockCalls(t, repositoryRoot(t), injectedClockPackagePatterns...)
	assert.Empty(t, findings, "these packages take their clock as a dependency: call the injected clock (auth.Clock, the usage Observer's now func) instead of time.Now/Since/Until; the planner has no clock and receives cache warmth as Inputs.PinCacheCold")
}

func TestSafeGoPackagesHaveNoRawGoroutines(t *testing.T) {
	findings := rawGoStatements(t, repositoryRoot(t), safeGoPackagePatterns...)
	assert.Empty(t, findings, "launch best-effort background work with observability.SafeGo or observability.SafeGoContext (bounded timeout, panic recovery), never a raw go statement")
}

func TestProseRuleChecksRejectNegativeFixtures(t *testing.T) {
	root := repositoryRoot(t)
	assert.Equal(t, []string{
		"internal/architecture/testdata/io_free_http/fixture.go: imports os",
		"internal/architecture/testdata/io_free_http/fixture.go: net/http.Client",
		"internal/architecture/testdata/io_free_http/fixture.go: net/http.Client.Do",
		"internal/architecture/testdata/io_free_http/fixture.go: net/http.NewRequestWithContext",
	}, ioUses(t, root, "./internal/architecture/testdata/io_free_http"))
	assert.Equal(t, []string{
		"internal/architecture/testdata/provider_raw_client/fixture.go: net/http.Client literal",
		"internal/architecture/testdata/provider_raw_client/fixture.go: net/http.DefaultTransport",
		"internal/architecture/testdata/provider_raw_client/fixture.go: net/http.Get",
	}, rawProviderClients(t, root, "./internal/architecture/testdata/provider_raw_client"))
	assert.Equal(t, []string{
		"internal/architecture/testdata/wall_clock/fixture.go: time.Now()",
		"internal/architecture/testdata/wall_clock/fixture.go: time.Since()",
	}, wallClockCalls(t, root, "./internal/architecture/testdata/wall_clock"))
	assert.Equal(t, []string{
		"internal/architecture/testdata/raw_goroutine/fixture.go:4: go statement",
		"internal/architecture/testdata/raw_goroutine/fixture.go:5: go statement",
	}, rawGoStatements(t, root, "./internal/architecture/testdata/raw_goroutine"))
}

func ioUses(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	var findings []string
	inspectNonTestFiles(t, root, patterns, func(info *types.Info, _ func(token.Pos) int, relative string, node ast.Node) {
		switch typed := node.(type) {
		case *ast.ImportSpec:
			path, err := strconv.Unquote(typed.Path.Value)
			require.NoError(t, err)
			if isIOImport(path) {
				findings = append(findings, fmt.Sprintf("%s: imports %s", relative, path))
			}
		case *ast.Ident:
			if use := netHTTPTransportUse(info.Uses[typed]); use != "" {
				findings = append(findings, fmt.Sprintf("%s: net/http.%s", relative, use))
			}
		}
	})
	return sortedUnique(findings)
}

func netHTTPTransportUse(object types.Object) string {
	if object == nil || object.Pkg() == nil || object.Pkg().Path() != "net/http" {
		return ""
	}
	receiver := methodReceiver(object)
	if receiver == nil {
		if netHTTPTransportAPI[object.Name()] {
			return object.Name()
		}
		return ""
	}
	for _, owner := range []string{"Client", "Transport", "Server"} {
		if isNamedType(receiver, "net/http", owner) {
			return owner + "." + object.Name()
		}
	}
	return ""
}

func methodReceiver(object types.Object) types.Type {
	function, ok := object.(*types.Func)
	if !ok {
		return nil
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return nil
	}
	return signature.Recv().Type()
}

func providerAdapterPackages(t *testing.T, root string) []string {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{Dir: root, Mode: packages.NeedName}, "./internal/providers/...")
	require.NoError(t, err)
	var adapters []string
	for _, loadedPackage := range loaded {
		switch loadedPackage.PkgPath {
		case providersPackagePath, providersPackagePath + "/httputil":
		default:
			adapters = append(adapters, loadedPackage.PkgPath)
		}
	}
	require.NotEmpty(t, adapters, "no provider adapter packages under internal/providers")
	return adapters
}

func isIOImport(path string) bool {
	switch {
	case path == "os", path == "net", path == "database/sql", path == "syscall":
		return true
	case strings.HasPrefix(path, "os/"), strings.HasPrefix(path, "database/"), strings.HasPrefix(path, "cloud.google.com/go/"):
		return true
	}
	return false
}

func rawProviderClients(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	var findings []string
	inspectNonTestFiles(t, root, patterns, func(info *types.Info, _ func(token.Pos) int, relative string, node ast.Node) {
		switch typed := node.(type) {
		case *ast.CompositeLit:
			for _, name := range []string{"Client", "Transport"} {
				if isNamedType(info.TypeOf(typed), "net/http", name) {
					findings = append(findings, fmt.Sprintf("%s: net/http.%s literal", relative, name))
				}
			}
		case *ast.Ident:
			object := info.Uses[typed]
			if object == nil || object.Pkg() == nil || object.Pkg().Path() != "net/http" {
				return
			}
			switch object.Name() {
			case "DefaultClient", "DefaultTransport", "Get", "Post", "Head", "PostForm":
				if methodReceiver(object) != nil {
					return
				}
				findings = append(findings, fmt.Sprintf("%s: net/http.%s", relative, object.Name()))
			}
		}
	})
	return sortedUnique(findings)
}

func wallClockCalls(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	var findings []string
	inspectNonTestFiles(t, root, patterns, func(info *types.Info, _ func(token.Pos) int, relative string, node ast.Node) {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		object := info.Uses[selector.Sel]
		if object == nil || object.Pkg() == nil || object.Pkg().Path() != "time" {
			return
		}
		switch object.Name() {
		case "Now", "Since", "Until":
			findings = append(findings, fmt.Sprintf("%s: time.%s()", relative, object.Name()))
		}
	})
	return sortedUnique(findings)
}

func rawGoStatements(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	var findings []string
	inspectNonTestFiles(t, root, patterns, func(_ *types.Info, line func(token.Pos) int, relative string, node ast.Node) {
		if statement, ok := node.(*ast.GoStmt); ok {
			findings = append(findings, fmt.Sprintf("%s:%d: go statement", relative, line(statement.Pos())))
		}
	})
	return sortedUnique(findings)
}

func inspectNonTestFiles(t *testing.T, root string, patterns []string, visit func(info *types.Info, position func(token.Pos) int, relative string, node ast.Node)) {
	t.Helper()
	configuration := &packages.Config{
		Dir:  root,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}
	loaded, err := packages.Load(configuration, patterns...)
	require.NoError(t, err)
	require.Zero(t, packages.PrintErrors(loaded), "load packages %v", patterns)
	require.NotEmpty(t, loaded, "no packages match %v", patterns)
	for _, loadedPackage := range loaded {
		for _, file := range loadedPackage.Syntax {
			relative, err := filepath.Rel(root, loadedPackage.Fset.Position(file.Package).Filename)
			require.NoError(t, err)
			ast.Inspect(file, func(node ast.Node) bool {
				if node != nil {
					visit(loadedPackage.TypesInfo, func(pos token.Pos) int { return loadedPackage.Fset.Position(pos).Line }, filepath.ToSlash(relative), node)
				}
				return true
			})
		}
	}
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			unique = append(unique, value)
		}
	}
	sort.Strings(unique)
	return unique
}
