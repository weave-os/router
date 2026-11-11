package translate

// routerDirective is a command the router intercepts and acts on itself. parse
// is the parser that owns the directive's grammar (sigils, arguments,
// bare-only) and alone decides matching. names is metadata read only by the
// install/directives.tsv drift test.
type routerDirective struct {
	names []string
	parse func(text string) (found bool, stripped string)
}

// routerDirectives is every directive the router acts on, so history cleanup
// covers a new one as soon as it is listed. directive_registry_internal_test.go fails
// when install/directives.tsv declares a directive that is neither listed here
// nor exempted there in notRouterHandled.
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
