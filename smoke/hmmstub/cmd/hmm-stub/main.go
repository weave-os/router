// Command hmm-stub bakes the stub policy release and serves the stub HMM
// classifier for the smoke stack.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"weave-os/router/smoke/hmmstub"
)

func main() {
	listen := flag.String("listen", ":8093", "listen address")
	releaseDir := flag.String("release-dir", "/release", "directory to bake the stub policy release into")
	healthcheck := flag.Bool("healthcheck", false, "probe a running stub's /readyz and exit")
	flag.Parse()

	if *healthcheck {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://" + *listen + "/readyz")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	if err := hmmstub.WriteRelease(*releaseDir); err != nil {
		slog.Error("Bake stub policy release", "err", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: *listen, Handler: hmmstub.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		slog.Error("Serve stub classifier", "err", err)
		os.Exit(1)
	}
}
