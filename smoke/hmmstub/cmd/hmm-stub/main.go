package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/smoke/hmmstub"
)

func main() {
	config.ConfigureLogging()
	listen := flag.String("listen", ":8093", "listen address")
	releaseDir := flag.String("release-dir", "/release", "directory to bake the stub policy release into")
	registryDir := flag.String("registry-dir", "/registry", "directory to bake the stub v5 serving registry into")
	classifierURL := flag.String("classifier-url", "http://hmm-stub:8093", "classifier origin the stub serving registry binds")
	healthcheck := flag.Bool("healthcheck", false, "probe a running stub's /readyz and exit")
	flag.Parse()

	if *healthcheck {
		client := &http.Client{Timeout: 2 * time.Second}
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+*listen+"/readyz", nil)
		if err != nil {
			os.Exit(1)
		}
		response, err := client.Do(request)
		if err != nil {
			os.Exit(1)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	if err := hmmstub.WriteRelease(*releaseDir); err != nil {
		slog.Error("Bake stub policy release", "err", err)
		os.Exit(1)
	}
	graph, err := hmmstub.NewServingGraph(*classifierURL)
	if err == nil {
		err = graph.Write(*registryDir)
	}
	if err != nil {
		slog.Error("Bake stub serving registry", "err", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: *listen, Handler: hmmstub.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		slog.Error("Serve stub classifier", "err", err)
		os.Exit(1)
	}
}
