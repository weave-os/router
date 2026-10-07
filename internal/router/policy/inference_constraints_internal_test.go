package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func TestContextWindowConstraintDefersToOverflowAdmission(t *testing.T) {
	binding := plannedBinding{Binding: Binding{CatalogID: "claude-opus-4-8"}}
	request := router.Request{EstimatedInputTokens: catalog.ContextWindowFor("claude-opus-4-8") + 1}

	assert.NotEmpty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding))

	request.OverflowAdmittedModels = map[string]struct{}{"claude-opus-4-8": {}}
	assert.Empty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding),
		"the plan's proof must agree with the resolver that kept the admitted model")
}

func TestContextConstraintUsesBindingCapacityAndOutputReserve(t *testing.T) {
	const model = "claude-opus-4-7"
	request := router.Request{EnableExtendedContext: true, ContextInputTokens: 992_000, ContextOutputReserve: 8_000}
	direct := plannedBinding{Binding: Binding{CatalogID: model, Provider: providers.ProviderAnthropic}}
	assert.Empty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, direct))
	request.ContextInputTokens++
	assert.NotEmpty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, direct))
	request.ContextInputTokens = 250_000
	gateway := plannedBinding{Binding: Binding{CatalogID: model, Provider: providers.ProviderOpenAIGateway}}
	assert.NotEmpty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, gateway))
}
