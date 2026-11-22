package architecture_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

var cloudSDKFreePackagePatterns = append([]string{
	"./internal/router/...",
	"./internal/policyregistry/...",
	"./internal/servingvalidate/...",
	"./internal/proxy/...",
}, ioFreePackagePatterns...)

var linkedCloudSDKPrefixes = []string{"cloud.google.com/go/storage", "cloud.google.com/go/pubsub"}

func TestInnerRingPackagesDoNotLinkCloudSDKs(t *testing.T) {
	root := repositoryRoot(t)
	require.Equal(t, []string{"internal/gcsregistry links cloud.google.com/go/storage"}, cloudSDKLinks(t, root, "./internal/gcsregistry"), "the linkage check must see the adapter's SDK import")
	findings := cloudSDKLinks(t, root, cloudSDKFreePackagePatterns...)
	assert.Empty(t, findings, "inner-ring packages must not link the Cloud Storage or Pub/Sub SDKs, even transitively: declare a port in the inner ring and implement it in an adapter package (internal/gcsregistry, internal/pubsub) constructed in cmd/")
}

func cloudSDKLinks(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{Dir: root, Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps}, patterns...)
	require.NoError(t, err)
	require.Zero(t, packages.PrintErrors(loaded), "load packages %v", patterns)
	require.NotEmpty(t, loaded)
	var findings []string
	for _, pkg := range loaded {
		for _, sdk := range linkedCloudSDKs(pkg) {
			findings = append(findings, fmt.Sprintf("%s links %s", relativePackage(pkg.PkgPath), sdk))
		}
	}
	sort.Strings(findings)
	return findings
}

func linkedCloudSDKs(start *packages.Package) []string {
	seen := map[string]bool{}
	linked := map[string]bool{}
	stack := []*packages.Package{start}
	for len(stack) > 0 {
		pkg := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for path, imported := range pkg.Imports {
			if seen[path] {
				continue
			}
			seen[path] = true
			for _, prefix := range linkedCloudSDKPrefixes {
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					linked[prefix] = true
				}
			}
			stack = append(stack, imported)
		}
	}
	out := make([]string, 0, len(linked))
	for sdk := range linked {
		out = append(out, sdk)
	}
	sort.Strings(out)
	return out
}
