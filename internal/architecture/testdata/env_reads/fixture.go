package envreads

import (
	"flag"
	"net/http"
	xos "os"
	"strings"

	"weave-os/router/internal/config"
)

type gjsonLike struct{}

func (gjsonLike) Get(string) string { return "" }

func Reads() []any {
	_ = flag.Bool("synthetic", false, "")
	local := gjsonLike{}
	return []any{
		xos.Getenv("FIXTURE_A"),
		config.GetOr("FIXTURE_B", ""),
		http.ProxyFromEnvironment,
		strings.TrimSpace(local.Get("not-an-env-read")),
	}
}

func Lookup() bool {
	_, ok := xos.LookupEnv("FIXTURE_C")
	return ok
}
