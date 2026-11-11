package translate

// routerDirective is a command the router intercepts and acts on itself. names
// are the canonical names and aliases install/directives.tsv declares; parse is
// the parser that owns the directive's grammar (sigils, arguments, bare-only).
type routerDirective struct {
	names []string
	parse func(text string) (found bool, stripped string)
}

// routerDirectives is every directive the router acts on, so history cleanup
// covers a new one as soon as it is listed. directive_registry_internal_test.go fails
// when install/directives.tsv declares a directive that is neither listed here
// nor named there as client-only.
var routerDirectives = [...]routerDirective{
	{names: []string{"force-model", "fm", "force-parent", "unforce-model", "ufm"}, parse: parseForceModelHistoryDirective},
	{names: []string{"router-feedback", "rf"}, parse: parseRouterFeedbackHistoryDirective},
	{names: []string{"router-session"}, parse: parseRouterSessionCommand},
	{names: []string{"router-models", "models"}, parse: parseRouterModelsCommand},
	{names: []string{"list-models"}, parse: parseListModelsCommand},
}

func parseForceModelHistoryDirective(text string) (found bool, stripped string) {
	_, found, stripped = parseForceModelCommandFromSource(text, false)
	return found, stripped
}

func parseRouterFeedbackHistoryDirective(text string) (found bool, stripped string) {
	_, found, stripped = parseRouterFeedbackCommand(text)
	return found, stripped
}
