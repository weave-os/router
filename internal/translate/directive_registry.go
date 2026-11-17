package translate

type routerDirective struct {
	names []string
	parse func(text string) (found bool, stripped string)
}

var routerDirectives = [...]routerDirective{
	{names: []string{"force-model", "fm", "force-parent", "unforce-model", "ufm"}, parse: func(text string) (bool, string) {
		_, found, stripped := parseForceModelCommandFromSource(text, false)
		return found, stripped
	}},
	{names: []string{"router-feedback", "rf"}, parse: func(text string) (bool, string) {
		_, found, stripped := parseRouterFeedbackCommand(text)
		return found, stripped
	}},
	{names: []string{"router-session"}, parse: parseRouterSessionCommand},
	{names: []string{"router-models", "models"}, parse: parseRouterModelsCommand},
	{names: []string{"list-models"}, parse: parseListModelsCommand},
}
