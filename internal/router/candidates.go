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

type CandidateViolation string

const (
	ViolationAllowedModels                 CandidateViolation = "allowed_models"
	ViolationExcludedModels                CandidateViolation = "excluded_models"
	ViolationSafetyExcludedModels          CandidateViolation = "safety_excluded_models"
	ViolationContextWindowExcludedModels   CandidateViolation = "context_window_excluded_models"
	ViolationImageLimitExcludedModels      CandidateViolation = "image_limit_excluded_models"
	ViolationUnsignedHistoryExcludedModels CandidateViolation = "unsigned_history_excluded_models"
	ViolationAutomaticExcludedModels       CandidateViolation = "automatic_excluded_models"
)

func (c Candidates) HardFilterViolation(model string) (CandidateViolation, bool) {
	if _, allowed := c.AllowedModels[model]; len(c.AllowedModels) > 0 && !allowed {
		return ViolationAllowedModels, true
	}
	for _, filter := range []struct {
		violation CandidateViolation
		models    map[string]struct{}
	}{
		{ViolationExcludedModels, c.ExcludedModels},
		{ViolationSafetyExcludedModels, c.SafetyExcludedModels},
		{ViolationContextWindowExcludedModels, c.ContextWindowExcludedModels},
		{ViolationImageLimitExcludedModels, c.ImageLimitExcludedModels},
		{ViolationUnsignedHistoryExcludedModels, c.UnsignedHistoryExcludedModels},
	} {
		if _, found := filter.models[model]; found {
			return filter.violation, true
		}
	}
	return "", false
}

func (c Candidates) AutomaticallyExcludes(model string) bool {
	_, excluded := c.AutomaticExcludedModels[model]
	return excluded
}
