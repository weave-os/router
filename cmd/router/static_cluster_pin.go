package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"workweave/router/internal/router/catalog"
)

func parseStaticClusterPins(raw string, logger *slog.Logger) map[int]string {
	pins := make(map[int]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		clusterID, model, found := strings.Cut(pair, ":")
		if !found || strings.TrimSpace(model) == "" {
			panic(fmt.Sprintf("invalid ROUTER_STATIC_CLUSTER_PIN entry %q: expected cluster_id:model", pair))
		}
		id, err := strconv.Atoi(strings.TrimSpace(clusterID))
		if err != nil {
			panic(fmt.Sprintf("invalid ROUTER_STATIC_CLUSTER_PIN cluster ID in %q", pair))
		}
		model = strings.TrimSpace(model)
		if _, known := catalog.ByID(model); !known {
			logger.Warn("Static cluster pin model is not in catalog; skipping",
				"cluster_id", id,
				"model", model,
			)
			continue
		}
		pins[id] = model
	}
	return pins
}
