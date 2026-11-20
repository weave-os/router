package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

// Wafer keys are deployment-only: one WAFER_API_KEY backs two provider names.
var envOnlyProviders = map[string]struct{}{
	providers.ProviderWafer:          {},
	providers.ProviderWaferAnthropic: {},
}

var providerCheckRE = regexp.MustCompile(`(?s)ADD CONSTRAINT model_router_external_api_keys_provider_check\s+CHECK \(provider IN \(([^)]*)\)\)`)

// latestExternalKeyProviderCheck returns the provider allowlist the newest up
// migration leaves on router.model_router_external_api_keys.
func latestExternalKeyProviderCheck(t *testing.T) map[string]struct{} {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "*.up.sql"))
	require.NoError(t, err)
	sort.Strings(files)
	for i := len(files) - 1; i >= 0; i-- {
		body, err := os.ReadFile(files[i])
		require.NoError(t, err)
		m := providerCheckRE.FindSubmatch(body)
		if m == nil {
			continue
		}
		allowed := map[string]struct{}{}
		for _, raw := range strings.Split(string(m[1]), ",") {
			allowed[strings.Trim(strings.TrimSpace(raw), "'")] = struct{}{}
		}
		return allowed
	}
	t.Fatal("no migration sets model_router_external_api_keys_provider_check")
	return nil
}

func TestExternalKeyProviderCheckAcceptsEveryKeyedProvider(t *testing.T) {
	allowed := latestExternalKeyProviderCheck(t)
	for provider := range providers.APIKeyEnvVars {
		if _, envOnly := envOnlyProviders[provider]; envOnly {
			continue
		}
		assert.Contains(t, allowed, provider, "a dashboard key for %s would fail the table CHECK", provider)
	}
}
