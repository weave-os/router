package architecture_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const (
	proxyPackagePath          = modulePath + "/internal/proxy"
	requestcontextPackagePath = modulePath + "/internal/requestcontext"
)

// Provider adapters read request-scoped credentials and identity through
// internal/requestcontext; importing the dispatch orchestrator from an adapter
// would let provider code reach routing state and reintroduce an
// adapter -> orchestrator cycle once dispatch owns the provider clients.
func TestProviderAdaptersDoNotImportProxy(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports | packages.NeedFiles, Tests: true}
	pkgs, err := packages.Load(cfg, providersPackagePath+"/...")
	require.NoError(t, err)

	var offenders []string
	for _, pkg := range pkgs {
		for importPath := range pkg.Imports {
			if importPath == proxyPackagePath {
				offenders = append(offenders, pkg.PkgPath)
			}
		}
	}
	require.Empty(t, offenders, "provider adapters must depend on %s, not %s", requestcontextPackagePath, proxyPackagePath)
}

// requestcontext is inner-ring: it must never import an adapter or the
// orchestrator, or the adapters that depend on it would inherit the cycle.
func TestRequestContextStaysInnerRing(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports, Tests: false}
	pkgs, err := packages.Load(cfg, requestcontextPackagePath)
	require.NoError(t, err)
	require.Len(t, pkgs, 1)

	for importPath := range pkgs[0].Imports {
		if !strings.HasPrefix(importPath, modulePath+"/") {
			continue
		}
		require.NotEqual(t, proxyPackagePath, importPath)
		require.False(t, strings.HasPrefix(importPath, providersPackagePath+"/"), "requestcontext imports concrete adapter %s", importPath)
		require.False(t, strings.HasPrefix(importPath, modulePath+"/internal/postgres"), "requestcontext imports adapter %s", importPath)
		require.False(t, strings.HasPrefix(importPath, modulePath+"/internal/api"), "requestcontext imports presentation %s", importPath)
	}
}
