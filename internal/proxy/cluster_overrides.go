package proxy

// mergeClusterOverrides composes the API-key-scoped cluster lists (org default)
// with the router user's own per-cluster selection.
//
// Composition is INTERSECTION with user order preserved — not "user wins" —
// so an individual cannot re-admit a model the org's key list removed (privilege
// escalation through an admin control). An empty intersection falls back to the
// key list; the org allowlist is desugared upstream so needs no handling here.
// Returns nil when neither side is configured.
func mergeClusterOverrides(key, user map[string][]string) map[string][]string {
	if len(key) == 0 && len(user) == 0 {
		return nil
	}
	out := make(map[string][]string, len(key)+len(user))
	for cluster, models := range key {
		out[cluster] = models
	}
	for cluster, userModels := range user {
		if len(userModels) == 0 {
			continue
		}
		keyModels, hasKey := key[cluster]
		if !hasKey || len(keyModels) == 0 {
			out[cluster] = userModels
			continue
		}
		allowed := make(map[string]struct{}, len(keyModels))
		for _, m := range keyModels {
			allowed[m] = struct{}{}
		}
		intersected := make([]string, 0, len(userModels))
		for _, m := range userModels {
			if _, ok := allowed[m]; ok {
				intersected = append(intersected, m)
			}
		}
		if len(intersected) == 0 {
			// Keep the org's list rather than emptying the cluster.
			continue
		}
		out[cluster] = intersected
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
