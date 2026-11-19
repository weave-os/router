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

func (c Candidates) HardFilterViolation(model string) (string, bool) {
	if _, allowed := c.AllowedModels[model]; len(c.AllowedModels) > 0 && !allowed {
		return "allowed_models", true
	}
	for _, filter := range []struct {
		name   string
		models map[string]struct{}
	}{
		{"excluded_models", c.ExcludedModels},
		{"context_window_excluded_models", c.ContextWindowExcludedModels},
		{"unsigned_history_excluded_models", c.UnsignedHistoryExcludedModels},
	} {
		if _, found := filter.models[model]; found {
			return filter.name, true
		}
	}
	return "", false
}
