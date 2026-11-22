package architecture_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const postgresPackagePath = modulePath + "/internal/postgres"

// The Postgres adapter implements ports declared by the inner-ring contexts
// that own them; importing the orchestrator would invert that dependency.
func TestPostgresAdapterDoesNotImportProxy(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports | packages.NeedFiles, Tests: true}
	pkgs, err := packages.Load(cfg, postgresPackagePath+"/...")
	require.NoError(t, err)
	require.NotEmpty(t, pkgs)

	var offenders []string
	for _, pkg := range pkgs {
		for importPath := range pkg.Imports {
			if importPath == proxyPackagePath {
				offenders = append(offenders, pkg.PkgPath)
			}
		}
	}
	require.Empty(t, offenders, "%s must implement ports declared by the owning inner-ring context, not import %s", postgresPackagePath, proxyPackagePath)
}
