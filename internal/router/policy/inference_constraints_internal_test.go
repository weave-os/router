package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func TestContextWindowConstraintDefersToOverflowAdmission(t *testing.T) {
	binding := plannedBinding{Binding: Binding{CatalogID: "claude-haiku-4-5"}}
	request := router.Request{EstimatedInputTokens: catalog.EffectiveContextWindowFor("claude-haiku-4-5") + 1}

	assert.NotEmpty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding))

	request.OverflowAdmittedModels = map[string]struct{}{"claude-haiku-4-5": {}}
	assert.Empty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding),
		"the plan's proof must agree with the resolver that kept the admitted model")
}
