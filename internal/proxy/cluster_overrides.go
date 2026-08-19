package proxy

// mergeClusterOverrides composes the API-key-scoped cluster lists (the org
// default) with the router user's own per-cluster selection.
//
// Composition is per-cluster INTERSECTION with the user's ordering preserved —
// deliberately not "user wins". A plain override would let an individual
// re-admit a model the org's key-scoped list removed, which is a privilege
// escalation through an admin control. Intersection lets a user narrow within a
// cluster and never widen past what the org already permits.
//
// Rules per cluster label:
//   - both configured  → intersection, user order preserved; an empty
//     intersection falls back to the key list (a stale personal pick must not
//     silently drop the org's restriction)
//   - user only        → user list
//   - key only         → key list
//
// The org allowlist needs no handling here: it is desugared into the request's
// exclusion set upstream, and effectiveArms already intersects any override
// against the request-resolved eligible candidates.
//
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
