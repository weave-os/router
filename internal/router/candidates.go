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
