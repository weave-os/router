package translate

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notRouterHandled names the install/directives.tsv entries the router never
// sees as a typed directive, with the reason. Anything else in the file must be
// in routerDirectives, or history cleanup silently misses it.
var notRouterHandled = map[string]string{
	"router-on":       "client-side toggle of the local router config",
	"router-off":      "client-side toggle of the local router config",
	"router-status":   "client-side read of the local router config",
	"disable-routing": "client-side toggle of the local router config",
	"beta":            "stripped from history by StripBetaArtifacts",
}

// directiveSampleArgs are arguments a directive needs before its parser
// accepts it.
var directiveSampleArgs = map[string]string{
	"force-model":  " gpt-5",
	"fm":           " gpt-5",
	"force-parent": " gpt-5",
}

type installerDirective struct {
	name  string
	codex bool
}

func readInstallerDirectives(t *testing.T) []installerDirective {
	t.Helper()
	f, err := os.Open("../../install/directives.tsv")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var out []installerDirective
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "|")
		require.Len(t, cols, 9, "directives.tsv row %q", line)
		codex := cols[4] == "yes"
		out = append(out, installerDirective{name: cols[0], codex: codex})
		for _, alias := range strings.Split(cols[1], ",") {
			if alias != "" {
				out = append(out, installerDirective{name: alias, codex: codex})
			}
		}
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, out)
	return out
}

func TestRouterDirectives_MatchInstallerRegistry(t *testing.T) {
	declared := map[string]bool{}
	for _, d := range readInstallerDirectives(t) {
		declared[d.name] = true
		if _, skip := notRouterHandled[d.name]; skip {
			continue
		}
		sigils := []string{"/"}
		if d.codex {
			sigils = append(sigils, "$")
		}
		for _, sigil := range sigils {
			typed := sigil + d.name + directiveSampleArgs[d.name]
			stripped, found := stripHistoricalDirective(typed)
			assert.True(t, found, "%s is declared in install/directives.tsv but no routerDirectives entry strips it; add one or list it in notRouterHandled", typed)
			assert.Empty(t, strings.TrimSpace(stripped), "%s", typed)
		}
	}
	for _, directive := range routerDirectives {
		for _, name := range directive.names {
			assert.True(t, declared[name], "routerDirectives lists %q, which install/directives.tsv does not declare", name)
		}
	}
	for name := range notRouterHandled {
		assert.True(t, declared[name], "notRouterHandled lists %q, which install/directives.tsv does not declare", name)
	}
}
