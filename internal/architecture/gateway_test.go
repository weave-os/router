package architecture_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

func TestGatewayDoesNotLinkWorkerRuntime(t *testing.T) {
	loaded, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps}, modulePath+"/cmd/router-gateway")
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	var forbidden []string
	packages.Visit(loaded, func(pkg *packages.Package) bool {
		name := pkg.PkgPath
		if name == modulePath+"/internal/proxy" || strings.HasPrefix(name, modulePath+"/internal/providers/") || strings.HasPrefix(name, modulePath+"/internal/router/cluster") || strings.Contains(name, "onnxruntime") || strings.Contains(name, "/hugot") {
			forbidden = append(forbidden, name)
		}
		return true
	}, nil)
	require.Empty(t, forbidden, "gateway must not initialize or link inference/provider/ONNX adapters")
}
