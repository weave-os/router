package proxy

// retiredModelIDs is intentionally separate from the catalog. The router
// permits catalog-missing passthrough models, so deleting a catalog row alone
// does not stop a previously persisted session pin from dispatching it.
var retiredModelIDs = map[string]struct{}{
	"grok-4.5": {},
}

func isRetiredModel(id string) bool {
	_, retired := retiredModelIDs[id]
	return retired
}
