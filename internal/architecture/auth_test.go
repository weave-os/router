package architecture_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

func TestAuthDoesNotImportTelemetryAdapters(t *testing.T) {
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedImports, Tests: true,
	}, modulePath+"/internal/auth")
	require.NoError(t, err)
	require.NotEmpty(t, pkgs)
	for _, pkg := range pkgs {
		require.NotContains(t, pkg.Imports, modulePath+"/internal/observability/otel", "auth must own its observer interface")
		require.NotContains(t, pkg.Imports, modulePath+"/internal/observability/apm", "auth must not depend on exporters")
	}
}
