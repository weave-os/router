package proxy

// mergeClusterOverrides intersects the key-scoped (org default) and user
// cluster lists, preserving user order. Intersection, not "user wins": a plain
// override lets a user re-admit a model the org removed (privilege escalation).
// Empty intersection falls back to the key list; the org allowlist is desugared
// upstream so needs no handling here. Returns nil when neither is configured.
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
