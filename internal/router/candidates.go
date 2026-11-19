package router

type Candidates struct {
	ExcludedModels                map[string]struct{}
	AllowedModels                 map[string]struct{}
	SafetyExcludedModels          map[string]struct{}
	ContextWindowExcludedModels   map[string]struct{}
	ImageLimitExcludedModels      map[string]struct{}
	UnsignedHistoryExcludedModels map[string]struct{}
	OverflowAdmittedModels        map[string]struct{}
	AutomaticExcludedModels       map[string]struct{}
}

func (c Candidates) PassesHardFilters(model string) bool {
	if _, allowed := c.AllowedModels[model]; len(c.AllowedModels) > 0 && !allowed {
		return false
	}
	for _, excluded := range []map[string]struct{}{c.ExcludedModels, c.ContextWindowExcludedModels, c.UnsignedHistoryExcludedModels} {
		if _, found := excluded[model]; found {
			return false
		}
	}
	return true
}
