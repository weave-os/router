package envreads

import (
	"flag"
	"net/http"
	aliasedOS "os"
	"strings"

	"weave-os/router/internal/config"
)

type keyValueStore struct{}

func (keyValueStore) Get(string) string { return "" }

func Reads() []any {
	_ = flag.Bool("synthetic", false, "")
	local := keyValueStore{}
	return []any{
		aliasedOS.Getenv("FIXTURE_A"),
		config.GetOr("FIXTURE_B", ""),
		http.ProxyFromEnvironment,
		strings.TrimSpace(local.Get("not-an-env-read")),
	}
}

func Lookup() bool {
	_, ok := aliasedOS.LookupEnv("FIXTURE_C")
	return ok
}
